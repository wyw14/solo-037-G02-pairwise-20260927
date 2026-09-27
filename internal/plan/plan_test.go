package plan

import (
    "testing"
    "time"

    "github.com/wyw14/solo-037/internal/model"
    "github.com/wyw14/solo-037/internal/tank"
)

func testInput() PlanInput {
    return PlanInput{Name: "regular-1hz", Frequency: 1.0, Amplitude: 0.05, DurationMs: 40, Sections: []string{"section-a", "section-b"}}
}

func newExecutor(t *testing.T, directory string, occupancy *tank.Registry, tests func() []model.Operation) (*Library, *Executor) {
    t.Helper()
    library, err := OpenLibrary(directory)
    if err != nil {
        t.Fatalf("open library: %v", err)
    }
    executor, err := OpenExecutor(directory, library, occupancy, tests)
    if err != nil {
        t.Fatalf("open executor: %v", err)
    }
    return library, executor
}

func TestLibrarySaveAndReload(t *testing.T) {
    directory := t.TempDir()
    library, _ := newExecutor(t, directory, tank.NewRegistry(2.4), nil)
    saved, err := library.Save(testInput(), time.Unix(1700000000, 0))
    if err != nil {
        t.Fatalf("save: %v", err)
    }
    if saved.ID == "" || saved.Revision == "" {
        t.Fatalf("expected identity and revision, got %+v", saved)
    }
    reopened, err := OpenLibrary(directory)
    if err != nil {
        t.Fatalf("reopen: %v", err)
    }
    loaded, ok := reopened.Get(saved.ID)
    if !ok {
        t.Fatalf("plan %s missing after reload", saved.ID)
    }
    if loaded.Name != "regular-1hz" || loaded.DurationMs != 40 || len(loaded.Sections) != 2 {
        t.Fatalf("unexpected plan after reload: %+v", loaded)
    }
    updated, err := reopened.Save(PlanInput{ID: saved.ID, Name: "renamed", Frequency: 2, Amplitude: 0.02, DurationMs: 20, Sections: []string{"section-b"}}, time.Unix(1700000100, 0))
    if err != nil {
        t.Fatalf("update: %v", err)
    }
    if updated.ID != saved.ID || updated.Revision == saved.Revision || updated.CreatedAt != saved.CreatedAt {
        t.Fatalf("update should keep identity and creation time, got %+v", updated)
    }
}

func TestLibraryValidation(t *testing.T) {
    library, _ := newExecutor(t, "", tank.NewRegistry(2.4), nil)
    for _, input := range []PlanInput{
        {Name: "", Frequency: 1, Amplitude: 0.1, DurationMs: 10, Sections: []string{"a"}},
        {Name: "x", Frequency: 0, Amplitude: 0.1, DurationMs: 10, Sections: []string{"a"}},
        {Name: "x", Frequency: 1, Amplitude: -1, DurationMs: 10, Sections: []string{"a"}},
        {Name: "x", Frequency: 1, Amplitude: 0.1, DurationMs: 0, Sections: []string{"a"}},
        {Name: "x", Frequency: 1, Amplitude: 0.1, DurationMs: 10},
    } {
        if _, err := library.Save(input, time.Now()); err == nil {
            t.Fatalf("expected validation error for %+v", input)
        }
    }
}

func waitCompleted(t *testing.T, executor *Executor, id string) Execution {
    t.Helper()
    deadline := time.Now().Add(5 * time.Second)
    for time.Now().Before(deadline) {
        execution, ok := executor.Get(id)
        if !ok {
            t.Fatalf("execution %s missing", id)
        }
        if execution.Status != StatusRunning {
            return execution
        }
        time.Sleep(5 * time.Millisecond)
    }
    t.Fatalf("execution %s still running", id)
    return Execution{}
}

func TestExecuteRecordsDiffAndReleasesOccupancy(t *testing.T) {
    occupancy := tank.NewRegistry(2.4)
    library, executor := newExecutor(t, t.TempDir(), occupancy, func() []model.Operation { return nil })
    saved, err := library.Save(testInput(), time.Now())
    if err != nil {
        t.Fatalf("save: %v", err)
    }
    execution, err := executor.Execute(saved.ID, "section-a", time.Now())
    if err != nil {
        t.Fatalf("execute: %v", err)
    }
    if execution.Status != StatusRunning {
        t.Fatalf("expected running, got %s", execution.Status)
    }
    if _, taken := occupancy.Occupancy("section-a"); !taken {
        t.Fatal("section should be occupied while executing")
    }
    finished := waitCompleted(t, executor, execution.ID)
    if finished.Status != StatusCompleted {
        t.Fatalf("expected completed, got %s", finished.Status)
    }
    if finished.Diff == nil {
        t.Fatal("expected a planned-vs-actual diff")
    }
    if finished.Diff.PlannedCount != 8 { // 40ms at the 5ms tick
        t.Fatalf("expected 8 planned commands, got %d", finished.Diff.PlannedCount)
    }
    if finished.Diff.ActualCount == 0 {
        t.Fatal("expected actual commands from the wave controller")
    }
    if _, taken := occupancy.Occupancy("section-a"); taken {
        t.Fatal("section should be released after execution")
    }
}

func TestPreflightRejectsOccupiedAndNotApplicable(t *testing.T) {
    occupancy := tank.NewRegistry(2.4)
    other := model.NewRunID()
    if _, err := occupancy.Claim("section-a", other, "carriage-9"); err != nil {
        t.Fatalf("claim: %v", err)
    }
    library, executor := newExecutor(t, "", occupancy, func() []model.Operation { return nil })
    saved, err := library.Save(testInput(), time.Now())
    if err != nil {
        t.Fatalf("save: %v", err)
    }
    execution, err := executor.Execute(saved.ID, "section-a", time.Now())
    if err != nil {
        t.Fatalf("execute: %v", err)
    }
    if execution.Status != StatusRejected {
        t.Fatalf("expected rejected, got %s", execution.Status)
    }
    failed := map[string]bool{}
    for _, check := range execution.Checks {
        failed[check.Name] = check.Passed
    }
    if passed, ok := failed["equipment_free"]; !ok || passed {
        t.Fatalf("expected equipment_free to fail, got %+v", execution.Checks)
    }
    result := executor.Preflight(saved, "section-c")
    if result.Ready {
        t.Fatal("section-c is not covered by the plan")
    }
}

func TestPreflightRejectsConflictingTest(t *testing.T) {
    operation := model.NewOperation("seakeeping", "hull", "section-a", "carriage-1", time.Now())
    operation, err := operation.Advance(model.StageArming, time.Now())
    if err != nil {
        t.Fatalf("advance: %v", err)
    }
    library, executor := newExecutor(t, "", tank.NewRegistry(2.4), func() []model.Operation { return []model.Operation{operation} })
    saved, err := library.Save(testInput(), time.Now())
    if err != nil {
        t.Fatalf("save: %v", err)
    }
    result := executor.Preflight(saved, "section-a")
    if result.Ready {
        t.Fatal("expected conflict with armed test on section-a")
    }
}

func TestDiffCommandsCountsMissingAndExtra(t *testing.T) {
    plan := WavePlan{Frequency: 1, Amplitude: 0.1, DurationMs: 20}
    commands := []model.DeviceCommand{{Setpoint: 0}, {Setpoint: 0}, {Setpoint: 0}}
    diff := DiffCommands(plan, commands)
    if diff.PlannedCount != 4 || diff.ActualCount != 3 || diff.Missing != 1 || diff.Extra != 0 {
        t.Fatalf("unexpected diff counts: %+v", diff)
    }
    diff = DiffCommands(plan, []model.DeviceCommand{{}, {}, {}, {}, {}, {}})
    if diff.Extra != 2 || diff.Missing != 0 {
        t.Fatalf("unexpected extra/missing: %+v", diff)
    }
}
