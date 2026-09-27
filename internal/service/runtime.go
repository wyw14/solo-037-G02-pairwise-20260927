package service

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/plan"
	"github.com/wyw14/solo-037/internal/tank"
	"github.com/wyw14/solo-037/internal/wave"
)

type Runtime struct {
	mu             sync.Mutex
	started        time.Time
	operations     map[model.RunID]model.Operation
	incidents      []model.Incident
	Measurements   *MeasurementArchive
	Tank           *tank.Registry
	Sections       *tank.Sections
	Wave           *wave.Controller
	Plans          *plan.Library
	PlanArchive    *plan.ExecutionArchive
	PlanExecutor   *plan.Executor
}

func NewRuntime() *Runtime {
	archive, _ := OpenMeasurementArchive("")
	library, _ := plan.OpenLibrary("")
	executions, _ := plan.OpenExecutionArchive("")
	return newRuntime(archive, library, executions)
}

func OpenRuntime(directory string) (*Runtime, error) {
	archive, err := OpenMeasurementArchive(directory)
	if err != nil {
		return nil, err
	}
	library, err := plan.OpenLibrary(directory)
	if err != nil {
		return nil, err
	}
	executions, err := plan.OpenExecutionArchive(directory)
	if err != nil {
		return nil, err
	}
	return newRuntime(archive, library, executions), nil
}

func newRuntime(archive *MeasurementArchive, library *plan.Library, executions *plan.ExecutionArchive) *Runtime {
	registry := tank.NewRegistry(2.4)
	sections := tank.NewSections([]string{"section-a", "section-b"}, 2.4)
	waveController := wave.NewController()
	runtime := &Runtime{
		started:        time.Now().UTC(),
		operations:     make(map[model.RunID]model.Operation),
		Measurements:   archive,
		Tank:           registry,
		Sections:       sections,
		Wave:           waveController,
		Plans:          library,
		PlanArchive:    executions,
	}
	runtime.PlanExecutor = plan.NewExecutor(library, executions, runtime, sections, registry, waveController)
	operation := model.NewOperation("commissioning", "benchmark-hull", "section-a", "carriage-1", runtime.started)
	runtime.operations[operation.ID] = operation
	coordinator := NewCoordinator(runtime)
	coordinator.Start(model.NewOperation("readiness", "calibration-body", "section-b", "carriage-2", runtime.started))
	_ = coordinator.Generation()
	return runtime
}

// GetOperation implements plan.OperationLookup for dispatch preflight checks.
// Operator-created experiments live in the measurement archive, so it is used
// as a fallback when the id is not present in the in-memory operation map.
func (r *Runtime) GetOperation(id model.RunID) (model.Operation, bool) {
	r.mu.Lock()
	operation, exists := r.operations[id]
	r.mu.Unlock()
	if exists {
		return operation, true
	}
	if run, ok := r.Measurements.Get(id); ok {
		return run.Operation, true
	}
	return model.Operation{}, false
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
