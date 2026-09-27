package service

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/plan"
	"github.com/wyw14/solo-037/internal/tank"
)

type Runtime struct {
	mu           sync.Mutex
	started      time.Time
	operations   map[model.RunID]model.Operation
	incidents    []model.Incident
	Measurements *MeasurementArchive
	Plans        *plan.Library
	PlanExecutor *plan.Executor
	occupancy    *tank.Registry
}

func NewRuntime() *Runtime {
	runtime, _ := openRuntime("")
	return runtime
}

func OpenRuntime(directory string) (*Runtime, error) {
	return openRuntime(directory)
}

func openRuntime(directory string) (*Runtime, error) {
	archive, err := OpenMeasurementArchive(directory)
	if err != nil {
		return nil, err
	}
	library, err := plan.OpenLibrary(directory)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{started: time.Now().UTC(), operations: make(map[model.RunID]model.Operation), Measurements: archive, Plans: library, occupancy: tank.NewRegistry(2.4)}
	executor, err := plan.OpenExecutor(directory, library, runtime.occupancy, runtime.activeOperations)
	if err != nil {
		return nil, err
	}
	runtime.PlanExecutor = executor
	operation := model.NewOperation("commissioning", "benchmark-hull", "section-a", "carriage-1", runtime.started)
	runtime.operations[operation.ID] = operation
	coordinator := NewCoordinator(runtime)
	coordinator.Start(model.NewOperation("readiness", "calibration-body", "section-b", "carriage-2", runtime.started))
	_ = coordinator.Generation()
	return runtime, nil
}

// activeOperations lists non-terminal tests, used by the plan executor to
// spot conflicts before issuing a plan.
func (r *Runtime) activeOperations() []model.Operation {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]model.Operation, 0, len(r.operations))
	for _, operation := range r.operations {
		if operation.Active() {
			result = append(result, operation)
		}
	}
	return result
}

func (r *Runtime) Create(operation model.Operation) (model.Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.operations[operation.ID] = operation
	return operation, nil
}

func (r *Runtime) Abort(run model.RunID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	operation, ok := r.operations[run]
	if !ok {
		return nil
	}
	r.operations[run] = operation.Abort(at)
	r.incidents = append(r.incidents, model.NewIncident(run, "warning", "operation aborted", at))
	return nil
}

func (r *Runtime) Operations() any {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]model.Operation, 0, len(r.operations))
	for _, operation := range r.operations {
		result = append(result, operation)
	}
	return result
}

func (r *Runtime) Equipment() any {
	diagnostics := BuildDiagnostics()
	return map[string]any{"analysis": diagnostics["analysis"], "emergency": diagnostics["emergency"], "reflection_ready": diagnostics["reflection_ready"], "metrics": diagnostics["metrics"]}
}
func (r *Runtime) Interlocks() any {
	diagnostics := BuildDiagnostics()
	return map[string]any{"interlocks": diagnostics["interlocks"], "audit": diagnostics["audit"], "permit": diagnostics["permit"]}
}

func (r *Runtime) Incidents() any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.Incident{}, r.incidents...)
}

func (r *Runtime) Health(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "started_at": r.started})
}

func (r *Runtime) Snapshot() map[string]any {
	return map[string]any{"operations": r.Operations(), "equipment": r.Equipment(), "interlocks": r.Interlocks(), "incidents": r.Incidents(), "laboratory": BuildDiagnostics()}
}
