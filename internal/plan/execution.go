package plan

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "sync"
    "time"

    "github.com/google/uuid"
    "github.com/wyw14/solo-037/internal/model"
    "github.com/wyw14/solo-037/internal/tank"
    "github.com/wyw14/solo-037/internal/wave"
)

// Execution statuses.
const (
    StatusRunning   = "running"
    StatusCompleted = "completed"
    StatusRejected  = "rejected"
)

// maxDiffSamples bounds how many per-tick comparisons are kept with an
// execution record; aggregate counters always cover the full command stream.
const maxDiffSamples = 200

// Check is one preflight verdict recorded before a plan is issued.
type Check struct {
    Name   string `json:"name"`
    Passed bool   `json:"passed"`
    Detail string `json:"detail"`
}

// PreflightResult is the outcome of checking a plan against the current
// tests and equipment occupancy without issuing any command.
type PreflightResult struct {
    Plan    WavePlan `json:"plan"`
    Section string   `json:"section"`
    Ready   bool     `json:"ready"`
    Checks  []Check  `json:"checks"`
}

// DiffSample compares the planned and actual setpoint for one tick.
type DiffSample struct {
    Index   int     `json:"index"`
    Planned float64 `json:"planned"`
    Actual  float64 `json:"actual"`
    Error   float64 `json:"error"`
}

// CommandDiff summarizes how the commands actually issued by the wave
// controller deviate from the plan's ideal curve.
type CommandDiff struct {
    PlannedCount int          `json:"planned_count"`
    ActualCount  int          `json:"actual_count"`
    Missing      int          `json:"missing"`
    Extra        int          `json:"extra"`
    MaxAbsError  float64      `json:"max_abs_error"`
    MeanAbsError float64      `json:"mean_abs_error"`
    Samples      []DiffSample `json:"samples"`
}

// Execution records one attempt to run a plan: the preflight checks, the
// plan snapshot, and — once finished — the diff between planned and actual
// commands.
type Execution struct {
    ID         string       `json:"id"`
    PlanID     string       `json:"plan_id"`
    Plan       WavePlan     `json:"plan"`
    Section    string       `json:"section"`
    Run        model.RunID  `json:"run"`
    Status     string       `json:"status"`
    Checks     []Check      `json:"checks"`
    Diff       *CommandDiff `json:"diff,omitempty"`
    CreatedAt  time.Time    `json:"created_at"`
    FinishedAt time.Time    `json:"finished_at,omitempty"`
}

// OccupancyRegistry is the equipment view the executor checks and claims.
type OccupancyRegistry interface {
    Claim(section string, run model.RunID, carriage string) (tank.Occupancy, error)
    Release(section string, run model.RunID, fence uint64) bool
    Occupancy(section string) (tank.Occupancy, bool)
}

// Executor issues wave plans against the wave controller after checking
// current tests and equipment occupancy, and keeps every execution for
// later review.
type Executor struct {
    mu          sync.Mutex
    path        string
    library     *Library
    occupancy   OccupancyRegistry
    activeTests func() []model.Operation
    newWaves    func() *wave.Controller
    running     bool
    executions  map[string]Execution
}

func OpenExecutor(directory string, library *Library, occupancy OccupancyRegistry, activeTests func() []model.Operation) (*Executor, error) {
    executor := &Executor{
        library:     library,
        occupancy:   occupancy,
        activeTests: activeTests,
        newWaves:    wave.NewController,
        executions:  make(map[string]Execution),
    }
    if directory == "" {
        return executor, nil
    }
    if err := os.MkdirAll(directory, 0o750); err != nil {
        return nil, err
    }
    executor.path = filepath.Join(directory, "wave-plan-executions.json")
    data, err := os.ReadFile(executor.path)
    if errors.Is(err, os.ErrNotExist) {
        return executor, nil
    }
    if err != nil {
        return nil, err
    }
    if err := json.Unmarshal(data, &executor.executions); err != nil {
        return nil, fmt.Errorf("read wave plan executions: %w", err)
    }
    if executor.executions == nil {
        return nil, errors.New("wave plan executions must contain a map")
    }
    // A restart interrupts whatever was running; mark it so the record is
    // not mistaken for a finished run.
    for id, execution := range executor.executions {
        if execution.Status == StatusRunning {
            execution.Status = StatusRejected
            execution.Checks = append(execution.Checks, Check{Name: "interrupted", Passed: false, Detail: "service restarted while executing"})
            executor.executions[id] = execution
        }
    }
    return executor, nil
}

func (e *Executor) commit(next map[string]Execution) error {
    if e.path != "" {
        data, err := json.MarshalIndent(next, "", "  ")
        if err != nil {
            return err
        }
        file, err := os.CreateTemp(filepath.Dir(e.path), "wave-plan-executions-*.tmp")
        if err != nil {
            return err
        }
        name := file.Name()
        defer os.Remove(name)
        if _, err = file.Write(data); err == nil {
            err = file.Sync()
        }
        closeErr := file.Close()
        if err != nil {
            return err
        }
        if closeErr != nil {
            return closeErr
        }
        if err := os.Rename(name, e.path); err != nil {
            return err
        }
    }
    e.executions = next
    return nil
}

func (e *Executor) clone() map[string]Execution {
    next := make(map[string]Execution, len(e.executions))
    for id, execution := range e.executions {
        next[id] = execution
    }
    return next
}

// conflicting reports whether an operation counts as an in-flight test on
// the section: anything armed or beyond. Configured-but-not-armed tests do
// not hold equipment.
func conflicting(operation model.Operation, section string) bool {
    return operation.Section == section && operation.Active() &&
        model.StageIndex(operation.Stage) >= model.StageIndex(model.StageArming)
}

// Preflight checks a plan against section applicability, equipment
// occupancy, in-flight tests, and executor availability. It issues nothing.
func (e *Executor) Preflight(plan WavePlan, section string) PreflightResult {
    e.mu.Lock()
    running := e.running
    e.mu.Unlock()
    checks := make([]Check, 0, 4)
    if plan.Applicable(section) {
        checks = append(checks, Check{Name: "section_applicable", Passed: true, Detail: "plan allows " + section})
    } else {
        checks = append(checks, Check{Name: "section_applicable", Passed: false, Detail: "plan does not cover " + section})
    }
    if occupancy, taken := e.occupancy.Occupancy(section); taken {
        checks = append(checks, Check{Name: "equipment_free", Passed: false, Detail: fmt.Sprintf("section held by run %s", occupancy.Run)})
    } else {
        checks = append(checks, Check{Name: "equipment_free", Passed: true, Detail: "section is free"})
    }
    conflict := ""
    if e.activeTests != nil {
        for _, operation := range e.activeTests() {
            if conflicting(operation, section) {
                conflict = fmt.Sprintf("test %s (%s) is %s on %s", operation.ID, operation.Name, operation.Stage, section)
                break
            }
        }
    }
    if conflict != "" {
        checks = append(checks, Check{Name: "no_conflicting_test", Passed: false, Detail: conflict})
    } else {
        checks = append(checks, Check{Name: "no_conflicting_test", Passed: true, Detail: "no active test on " + section})
    }
    if running {
        checks = append(checks, Check{Name: "wave_maker_idle", Passed: false, Detail: "another plan is executing"})
    } else {
        checks = append(checks, Check{Name: "wave_maker_idle", Passed: true, Detail: "wave maker is idle"})
    }
    result := PreflightResult{Plan: plan, Section: section, Ready: true, Checks: checks}
    for _, check := range checks {
        if !check.Passed {
            result.Ready = false
            break
        }
    }
    return result
}

// Execute runs preflight and, when clear, claims the section and drives the
// wave controller for the plan duration. A rejected preflight is still
// recorded so staff can review why the plan was not issued.
func (e *Executor) Execute(planID string, section string, at time.Time) (Execution, error) {
    plan, ok := e.library.Get(planID)
    if !ok {
        return Execution{}, errors.New("plan not found")
    }
    preflight := e.Preflight(plan, section)
    execution := Execution{
        ID:        uuid.NewString(),
        PlanID:    plan.ID,
        Plan:      plan,
        Section:   section,
        Run:       model.NewRunID(),
        Status:    StatusRunning,
        Checks:    preflight.Checks,
        CreatedAt: at.UTC(),
    }
    if !preflight.Ready {
        execution.Status = StatusRejected
        execution.FinishedAt = at.UTC()
        e.mu.Lock()
        defer e.mu.Unlock()
        if err := e.commitWithRecord(execution); err != nil {
            return Execution{}, err
        }
        return execution, nil
    }
    e.mu.Lock()
    if e.running {
        e.mu.Unlock()
        return Execution{}, errors.New("another plan is executing")
    }
    e.running = true
    occupancy, err := e.occupancy.Claim(section, execution.Run, "wave-maker")
    if err != nil {
        e.running = false
        execution.Status = StatusRejected
        execution.FinishedAt = at.UTC()
        execution.Checks = append(execution.Checks, Check{Name: "equipment_free", Passed: false, Detail: err.Error()})
        if commitErr := e.commitWithRecord(execution); commitErr != nil {
            e.mu.Unlock()
            return Execution{}, commitErr
        }
        e.mu.Unlock()
        return execution, nil
    }
    if err := e.commitWithRecord(execution); err != nil {
        e.running = false
        e.occupancy.Release(occupancy.Section, occupancy.Run, occupancy.Fence)
        e.mu.Unlock()
        return Execution{}, err
    }
    e.mu.Unlock()
    waves := e.newWaves()
    ctx, cancel := context.WithCancel(context.Background())
    profile := wave.NewProfile(plan.Frequency, plan.Amplitude, plan.Duration())
    waves.Start(ctx, execution.Run, profile, 1, at)
    go e.finish(execution.ID, plan, occupancy, waves, cancel)
    return execution, nil
}

func (e *Executor) commitWithRecord(execution Execution) error {
    next := e.clone()
    next[execution.ID] = execution
    return e.commit(next)
}

func (e *Executor) finish(id string, plan WavePlan, occupancy tank.Occupancy, waves *wave.Controller, cancel context.CancelFunc) {
    timer := time.NewTimer(plan.Duration())
    <-timer.C
    waves.Stop()
    cancel()
    deadline := time.Now().Add(2 * time.Second)
    for waves.Active() && time.Now().Before(deadline) {
        time.Sleep(time.Millisecond)
    }
    diff := DiffCommands(plan, waves.Commands())
    e.occupancy.Release(occupancy.Section, occupancy.Run, occupancy.Fence)
    e.mu.Lock()
    defer e.mu.Unlock()
    execution, ok := e.executions[id]
    if ok {
        execution.Status = StatusCompleted
        execution.FinishedAt = time.Now().UTC()
        execution.Diff = diff
        _ = e.commitWithRecord(execution)
    }
    e.running = false
}

// DiffCommands compares the plan's ideal curve, sampled at the controller
// tick, with the commands the controller actually issued.
func DiffCommands(plan WavePlan, commands []model.DeviceCommand) *CommandDiff {
    profile := wave.NewProfile(plan.Frequency, plan.Amplitude, plan.Duration())
    planned := make([]float64, 0)
    for elapsed := time.Duration(0); elapsed < plan.Duration(); elapsed += wave.TickInterval {
        planned = append(planned, profile.Sample(elapsed))
    }
    diff := &CommandDiff{PlannedCount: len(planned), ActualCount: len(commands), Samples: []DiffSample{}}
    compared := len(planned)
    if len(commands) < compared {
        compared = len(commands)
    }
    diff.Missing = len(planned) - compared
    diff.Extra = len(commands) - compared
    var total float64
    for index := 0; index < compared; index++ {
        actual := commands[index].Setpoint
        errorAbs := actual - planned[index]
        if errorAbs < 0 {
            errorAbs = -errorAbs
        }
        if errorAbs > diff.MaxAbsError {
            diff.MaxAbsError = errorAbs
        }
        total += errorAbs
        if index < maxDiffSamples {
            diff.Samples = append(diff.Samples, DiffSample{Index: index, Planned: planned[index], Actual: actual, Error: actual - planned[index]})
        }
    }
    if compared > 0 {
        diff.MeanAbsError = total / float64(compared)
    }
    return diff
}

func (e *Executor) Get(id string) (Execution, bool) {
    e.mu.Lock()
    defer e.mu.Unlock()
    execution, exists := e.executions[id]
    return execution, exists
}

func (e *Executor) List() []Execution {
    e.mu.Lock()
    defer e.mu.Unlock()
    result := make([]Execution, 0, len(e.executions))
    for _, execution := range e.executions {
        result = append(result, execution)
    }
    sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
    return result
}
