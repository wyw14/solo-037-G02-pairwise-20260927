package plan

import (
	"errors"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/tank"
	"github.com/wyw14/solo-037/internal/wave"
)

type fakeOperations struct {
	ops map[model.RunID]model.Operation
}

func (f *fakeOperations) GetOperation(id model.RunID) (model.Operation, bool) {
	op, ok := f.ops[id]
	return op, ok
}

func validSchemeInput(sections ...string) SchemeInput {
	return SchemeInput{Name: "regular", FrequencyHz: 0.8, Amplitude: 0.15, DurationMS: 200, Sections: sections}
}

type testRig struct {
	executor *Executor
	library  *Library
	archive  *ExecutionArchive
	registry *tank.Registry
	sections *tank.Sections
	runID    model.RunID
	ops      *fakeOperations
}

func newTestRig(t *testing.T) testRig {
	t.Helper()
	directory := t.TempDir()
	library, err := OpenLibrary(directory)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := OpenExecutionArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	registry := tank.NewRegistry(2.4)
	sections := tank.NewSections([]string{"section-a", "section-b"}, 2.4)
	operation := model.NewOperation("tow A", "hull A", "section-a", "carriage-1", time.Now().UTC())
	ops := &fakeOperations{ops: map[model.RunID]model.Operation{operation.ID: operation}}
	executor := NewExecutor(library, archive, ops, sections, registry, wave.NewController())
	return testRig{executor: executor, library: library, archive: archive, registry: registry, sections: sections, runID: operation.ID, ops: ops}
}

func (r testRig) createScheme(t *testing.T, input SchemeInput) Scheme {
	t.Helper()
	scheme, err := r.library.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	return scheme
}

func waitTerminal(t *testing.T, executor *Executor, id string) Execution {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		execution, ok := executor.Get(id)
		if ok && execution.Status != StatusRunning {
			return execution
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("execution did not finish in time")
	return Execution{}
}

func TestSchemeValidation(t *testing.T) {
	cases := []struct {
		name  string
		input SchemeInput
	}{
		{"missing name", SchemeInput{FrequencyHz: 0.8, Amplitude: 0.15, DurationMS: 200}},
		{"zero frequency", SchemeInput{Name: "x", FrequencyHz: 0, Amplitude: 0.15, DurationMS: 200}},
		{"frequency too high", SchemeInput{Name: "x", FrequencyHz: 11, Amplitude: 0.15, DurationMS: 200}},
		{"zero amplitude", SchemeInput{Name: "x", FrequencyHz: 0.8, Amplitude: 0, DurationMS: 200}},
		{"duration too short", SchemeInput{Name: "x", FrequencyHz: 0.8, Amplitude: 0.15, DurationMS: 50}},
		{"duration too long", SchemeInput{Name: "x", FrequencyHz: 0.8, Amplitude: 0.15, DurationMS: 3600001}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := tc.input.toScheme("id", time.Now())
			if err := scheme.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	scheme := validSchemeInput().toScheme("id", time.Now())
	if err := scheme.Validate(); err != nil {
		t.Fatalf("valid scheme rejected: %v", err)
	}
	if !scheme.AppliesTo("section-a") || !scheme.AppliesTo("section-b") {
		t.Fatal("unrestricted scheme should apply to every section")
	}
	restricted := validSchemeInput("section-b").toScheme("id", time.Now())
	if restricted.AppliesTo("section-a") || !restricted.AppliesTo("section-b") {
		t.Fatal("section restriction not honoured")
	}
}

func TestLibraryPersistsAcrossReopen(t *testing.T) {
	directory := t.TempDir()
	library, err := OpenLibrary(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := library.Create(validSchemeInput("section-a")); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLibrary(directory)
	if err != nil {
		t.Fatal(err)
	}
	schemes := reopened.List()
	if len(schemes) != 1 || schemes[0].Name != "regular" {
		t.Fatalf("scheme not reloaded: %+v", schemes)
	}
	if _, err := reopened.Update(schemes[0].ID, SchemeInput{Name: "renamed", FrequencyHz: 1, Amplitude: 0.1, DurationMS: 300}); err != nil {
		t.Fatal(err)
	}
	if !reopened.Delete(schemes[0].ID) || len(reopened.List()) != 0 {
		t.Fatal("delete failed")
	}
	if _, exists := reopened.Get(schemes[0].ID); exists {
		t.Fatal("deleted scheme still present")
	}
}

func TestPreflightFailures(t *testing.T) {
	rig := newTestRig(t)
	schemeA := rig.createScheme(t, validSchemeInput("section-a"))
	schemeAny := rig.createScheme(t, validSchemeInput())

	abortedOperation := model.NewOperation("aborted", "hull", "section-b", "carriage-1", time.Now()).Abort(time.Now())
	rig.ops.ops[abortedOperation.ID] = abortedOperation

	cases := []struct {
		name    string
		runID   model.RunID
		scheme  string
		section string
		failed  string
	}{
		{"unknown operation", model.NewRunID(), schemeAny.ID, "section-a", "operation_exists"},
		{"terminal operation", abortedOperation.ID, schemeAny.ID, "section-a", "operation_active"},
		{"unknown section", rig.runID, schemeAny.ID, "section-z", "section_exists"},
		{"section not allowed", rig.runID, schemeA.ID, "section-b", "section_allowed"},
		{"unknown scheme", rig.runID, "missing-scheme", "section-a", "scheme_exists"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			preflight := rig.executor.Precheck(DispatchInput{RunID: tc.runID, SchemeID: tc.scheme, Section: tc.section}, time.Now())
			if preflight.Allowed {
				t.Fatal("preflight should fail")
			}
			found := false
			for _, check := range preflight.Checks {
				if check.Name == tc.failed && !check.OK {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected failing check %q in %+v", tc.failed, preflight.Checks)
			}
		})
	}

	// Occupy section-a with a different run; the busy-section and busy-device
	// checks are exercised through dispatch in later tests.
	if _, err := rig.registry.Claim("section-a", model.NewRunID(), "other"); err != nil {
		t.Fatal(err)
	}
	preflight := rig.executor.Precheck(DispatchInput{RunID: rig.runID, SchemeID: schemeAny.ID, Section: "section-a"}, time.Now())
	for _, check := range preflight.Checks {
		if check.Name == "section_free" && check.OK {
			t.Fatal("occupied section reported free")
		}
	}
}

func TestDispatchRunsToCompletionAndMatchesPlan(t *testing.T) {
	rig := newTestRig(t)
	scheme := rig.createScheme(t, validSchemeInput())
	execution, err := rig.executor.Dispatch(DispatchInput{RunID: rig.runID, SchemeID: scheme.ID, Section: "section-a"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(execution.Planned) != 11 { // 0,20,...,200 ms
		t.Fatalf("planned timeline length = %d, want 11", len(execution.Planned))
	}
	final := waitTerminal(t, rig.executor, execution.ID)
	if final.Status != StatusCompleted {
		t.Fatalf("status = %s (%s), want completed", final.Status, final.Reason)
	}
	diff := final.Diff()
	if diff.PlannedCount != diff.ActualCount || diff.MissingCount != 0 || diff.ExtraCount != 0 {
		t.Fatalf("counts diverge: %+v", diff)
	}
	if len(diff.SetpointMismatches) != 0 {
		t.Fatalf("setpoint mismatches: %+v", diff.SetpointMismatches)
	}
	if !diff.Completed {
		t.Fatal("diff not marked completed")
	}
	if !rig.registry.ObserveFree("section-a") {
		t.Fatal("section not released after completion")
	}
	if rig.executor.wave.Active() {
		t.Fatal("wave controller still active after completion")
	}
}

func TestDispatchConflictIsHardRejected(t *testing.T) {
	rig := newTestRig(t)
	scheme := rig.createScheme(t, SchemeInput{Name: "long", FrequencyHz: 0.8, Amplitude: 0.15, DurationMS: 1000})
	first, err := rig.executor.Dispatch(DispatchInput{RunID: rig.runID, SchemeID: scheme.ID, Section: "section-a"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = rig.executor.Dispatch(DispatchInput{RunID: rig.runID, SchemeID: scheme.ID, Section: "section-a"}, time.Now().Add(time.Millisecond))
	if !errors.Is(err, ErrPlanRejected) {
		t.Fatalf("second dispatch err = %v, want ErrPlanRejected", err)
	}
	rejected := rig.executor.List()
	var sawRejected bool
	for _, execution := range rejected {
		if execution.Status == StatusRejected {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Fatal("rejected dispatch was not recorded")
	}
	if _, err := rig.executor.Abort(first.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, rig.executor, first.ID)
}

func TestAbortTruncatesTimelineAndReleasesSection(t *testing.T) {
	rig := newTestRig(t)
	scheme := rig.createScheme(t, SchemeInput{Name: "long", FrequencyHz: 0.8, Amplitude: 0.15, DurationMS: 2000})
	execution, err := rig.executor.Dispatch(DispatchInput{RunID: rig.runID, SchemeID: scheme.ID, Section: "section-a"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.executor.Abort(execution.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, rig.executor, execution.ID)
	if final.Status != StatusAborted {
		t.Fatalf("status = %s, want aborted", final.Status)
	}
	if final.Diff().MissingCount == 0 {
		t.Fatal("aborted plan should have missing planned commands")
	}
	if !rig.registry.ObserveFree("section-a") {
		t.Fatal("section not released after abort")
	}
	if _, err := rig.executor.Abort(execution.ID, time.Now()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("re-abort err = %v, want ErrNotRunning", err)
	}
}

func TestRunningExecutionMarkedInterruptedOnReload(t *testing.T) {
	directory := t.TempDir()
	archive, err := OpenExecutionArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	running := Execution{ID: "x", Status: StatusRunning, Planned: []PlannedCommand{}, Actual: []model.DeviceCommand{}}
	if err := archive.Put(running); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenExecutionArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	execution, ok := reloaded.Get("x")
	if !ok || execution.Status != StatusInterrupted {
		t.Fatalf("reloaded status = %s, want interrupted", execution.Status)
	}
}
