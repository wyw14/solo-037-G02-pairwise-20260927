package plan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/tank"
	"github.com/wyw14/solo-037/internal/wave"
)

// CommandStep is the fixed paddle command interval for plan execution.
const CommandStep = 20 * time.Millisecond

const wavemakerDevice = "wavemaker"

var (
	ErrPlanRejected = errors.New("wave plan rejected by preflight")
	ErrNotRunning   = errors.New("wave plan is not running")
)

// OperationLookup lets the executor verify the current experiment without
// depending on the service package (avoids an import cycle).
type OperationLookup interface {
	GetOperation(model.RunID) (model.Operation, bool)
}

type DispatchInput struct {
	RunID    model.RunID `json:"run_id"`
	SchemeID string      `json:"scheme_id"`
	Section  string      `json:"section"`
}

type Executor struct {
	mu         sync.Mutex
	library    *Library
	executions *ExecutionArchive
	operations OperationLookup
	sections   *tank.Sections
	registry   *tank.Registry
	wave       *wave.Controller
	epoch      uint64
	cancels    map[string]context.CancelFunc
}

func NewExecutor(library *Library, executions *ExecutionArchive, operations OperationLookup, sections *tank.Sections, registry *tank.Registry, waveController *wave.Controller) *Executor {
	return &Executor{
		library:    library,
		executions: executions,
		operations: operations,
		sections:   sections,
		registry:   registry,
		wave:       waveController,
		cancels:    make(map[string]context.CancelFunc),
	}
}

// Precheck verifies the experiment, scheme, section and device occupancy
// without driving anything. It is safe to call from the UI before dispatch.
func (e *Executor) Precheck(input DispatchInput, at time.Time) Preflight {
	preflight := Preflight{RunID: input.RunID, Section: input.Section, SchemeID: input.SchemeID, CheckedAt: at.UTC(), Checks: make([]CheckItem, 0, 8)}
	add := func(name string, ok bool, detail string) {
		preflight.Checks = append(preflight.Checks, CheckItem{Name: name, OK: ok, Detail: detail})
	}

	scheme, schemeExists := e.library.Get(input.SchemeID)
	add("scheme_exists", schemeExists, missing("scheme", input.SchemeID, schemeExists))
	if schemeExists {
		add("scheme_valid", scheme.Validate() == nil, errText(scheme.Validate()))
	} else {
		add("scheme_valid", false, "scheme missing")
	}

	operation, operationExists := e.operations.GetOperation(input.RunID)
	add("operation_exists", operationExists, missing("operation", string(input.RunID), operationExists))
	add("operation_active", operationExists && operation.Active(), stageText(operationExists, operation))

	_, sectionExists := e.sections.Get(input.Section)
	add("section_exists", sectionExists, missing("section", input.Section, sectionExists))
	sectionAllowed := schemeExists && sectionExists && scheme.AppliesTo(input.Section)
	if sectionAllowed {
		add("section_allowed", true, "section within scheme scope")
	} else {
		add("section_allowed", false, allowedText(schemeExists, sectionExists, scheme))
	}

	if occupancy, occupied := e.registry.Occupancy(input.Section); occupied {
		add("section_free", false, fmt.Sprintf("occupied by run %s (fence %d)", occupancy.Run, occupancy.Fence))
	} else {
		add("section_free", sectionExists, occupiedText(sectionExists))
	}
	if e.wave.Active() {
		add("wavemaker_idle", false, "wave controller is already running a plan")
	} else {
		add("wavemaker_idle", true, "wavemaker idle")
	}

	preflight.Allowed = true
	for _, check := range preflight.Checks {
		if !check.OK {
			preflight.Allowed = false
			break
		}
	}
	return preflight
}

// Dispatch reruns the preflight under lock (closing the check-then-act gap)
// and, on success, claims the section and starts the bounded wave run.
func (e *Executor) Dispatch(input DispatchInput, at time.Time) (Execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	preflight := e.Precheck(input, at)
	if !preflight.Allowed {
		reason := firstFailure(preflight)
		rejected := Execution{
			ID:        uuid.NewString(),
			RunID:     input.RunID,
			Section:   input.Section,
			Status:    StatusRejected,
			Preflight: preflight,
			Planned:   []PlannedCommand{},
			Actual:    []model.DeviceCommand{},
			StartedAt: at.UTC(),
			Reason:    reason,
		}
		_ = e.executions.Put(rejected)
		return rejected, fmt.Errorf("%w: %s", ErrPlanRejected, reason)
	}

	scheme, _ := e.library.Get(input.SchemeID)
	occupancy, err := e.registry.Claim(input.Section, input.RunID, wavemakerDevice)
	if err != nil {
		return Execution{}, fmt.Errorf("%w: %s", ErrPlanRejected, err.Error())
	}
	e.epoch++
	profile := scheme.Profile()
	execution := Execution{
		ID:             uuid.NewString(),
		RunID:          input.RunID,
		Section:        input.Section,
		SchemeSnapshot: scheme,
		Status:         StatusRunning,
		Epoch:          e.epoch,
		Fence:          occupancy.Fence,
		Preflight:      preflight,
		Planned:        plannedTimeline(profile, CommandStep),
		Actual:         []model.DeviceCommand{},
		StartedAt:      at.UTC(),
	}
	if err := e.executions.Put(execution); err != nil {
		e.registry.Release(input.Section, input.RunID, occupancy.Fence)
		return Execution{}, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.cancels[execution.ID] = cancel
	startedAt := at.UTC()
	e.wave.StartBounded(ctx, input.RunID, profile, e.epoch, startedAt, CommandStep, func(command model.DeviceCommand) {
		command.Fence = occupancy.Fence
		_ = e.executions.AppendCommand(execution.ID, command)
	})
	go e.finish(execution.ID, input, occupancy.Fence, profile, len(execution.Planned))
	return execution, nil
}

func (e *Executor) finish(id string, input DispatchInput, fence uint64, profile wave.Profile, plannedCount int) {
	stopped := e.wave.WaitStopped(profile.Duration + 5*time.Second)

	e.mu.Lock()
	defer e.mu.Unlock()
	if cancel, exists := e.cancels[id]; exists {
		cancel()
		delete(e.cancels, id)
	}
	e.registry.Release(input.Section, input.RunID, fence)

	execution, exists := e.executions.Get(id)
	if !exists {
		return
	}
	execution.FinishedAt = time.Now().UTC()
	switch {
	case len(execution.Actual) >= plannedCount:
		execution.Status = StatusCompleted
	case stopped:
		execution.Status = StatusAborted
		execution.Reason = "execution stopped early"
	default:
		e.wave.Stop()
		execution.Status = StatusAborted
		execution.Reason = "wave controller did not stop within the grace period"
	}
	_ = e.executions.Put(execution)
}

// Abort stops a running plan; the wave controller's stop path (shared with the
// emergency brake) makes the bounded loop exit and release its section.
func (e *Executor) Abort(id string, at time.Time) (Execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	execution, exists := e.executions.Get(id)
	if !exists {
		return Execution{}, ErrNotFound
	}
	if execution.Status != StatusRunning {
		return execution, fmt.Errorf("%w: status is %s", ErrNotRunning, execution.Status)
	}
	e.wave.Stop()
	return execution, nil
}

func (e *Executor) Get(id string) (Execution, bool) { return e.executions.Get(id) }

func (e *Executor) List() []Execution { return e.executions.List() }

func plannedTimeline(profile wave.Profile, step time.Duration) []PlannedCommand {
	result := make([]PlannedCommand, 0, int(profile.Duration/step)+1)
	for elapsed := time.Duration(0); elapsed <= profile.Duration; elapsed += step {
		result = append(result, PlannedCommand{OffsetMS: elapsed.Milliseconds(), Setpoint: profile.Sample(elapsed)})
	}
	return result
}

func firstFailure(preflight Preflight) string {
	for _, check := range preflight.Checks {
		if !check.OK {
			return check.Name + ": " + check.Detail
		}
	}
	return "preflight failed"
}

func missing(kind, id string, exists bool) string {
	if exists {
		return ""
	}
	return kind + " " + id + " not found"
}

func occupiedText(exists bool) string {
	if exists {
		return "section is free"
	}
	return "section missing"
}

func allowedText(schemeExists, sectionExists bool, scheme Scheme) string {
	switch {
	case !schemeExists:
		return "scheme missing"
	case !sectionExists:
		return "section missing"
	default:
		return "scheme does not apply to this section (allowed: " + joinSections(scheme.Sections) + ")"
	}
}

func joinSections(sections []string) string {
	if len(sections) == 0 {
		return "any"
	}
	result := ""
	for index, section := range sections {
		if index > 0 {
			result += ", "
		}
		result += section
	}
	return result
}

func stageText(exists bool, operation model.Operation) string {
	if !exists {
		return "operation missing"
	}
	return "operation stage is " + string(operation.Stage)
}

func errText(err error) string {
	if err == nil {
		return "scheme parameters valid"
	}
	return err.Error()
}
