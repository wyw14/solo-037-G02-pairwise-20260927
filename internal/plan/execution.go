package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

var (
	ErrNotFound = errors.New("plan record not found")
)

type Status string

const (
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusAborted     Status = "aborted"
	StatusRejected    Status = "rejected"
	StatusInterrupted Status = "interrupted"
)

// PlannedCommand is one expected setpoint at a fixed offset from plan start.
type PlannedCommand struct {
	OffsetMS int64   `json:"offset_ms"`
	Setpoint float64 `json:"setpoint"`
}

type CheckItem struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Preflight struct {
	RunID     model.RunID `json:"run_id"`
	Section   string      `json:"section"`
	SchemeID  string      `json:"scheme_id"`
	Allowed   bool        `json:"allowed"`
	Checks    []CheckItem `json:"checks"`
	CheckedAt time.Time   `json:"checked_at"`
}

// Execution is the auditable record of one dispatch: the scheme snapshot that
// was authorised, the preflight result, the planned timeline and the commands
// the wave controller actually issued.
type Execution struct {
	ID             string                `json:"id"`
	RunID          model.RunID           `json:"run_id"`
	Section        string                `json:"section"`
	SchemeSnapshot Scheme                `json:"scheme_snapshot"`
	Status         Status                `json:"status"`
	Epoch          uint64                `json:"epoch"`
	Fence          uint64                `json:"fence"`
	Preflight      Preflight             `json:"preflight"`
	Planned        []PlannedCommand      `json:"planned"`
	Actual         []model.DeviceCommand `json:"actual"`
	StartedAt      time.Time             `json:"started_at"`
	FinishedAt     time.Time             `json:"finished_at"`
	Reason         string                `json:"reason"`
}

type SetpointMismatch struct {
	Index   int     `json:"index"`
	Planned float64 `json:"planned"`
	Actual  float64 `json:"actual"`
}

// CommandDiff compares the planned timeline with what was issued. Missing
// commands indicate an early stop (abort/interruption); timing deltas show how
// far each actual command drifted from its planned instant.
type CommandDiff struct {
	PlannedCount       int                `json:"planned_count"`
	ActualCount        int                `json:"actual_count"`
	SetpointMismatches []SetpointMismatch `json:"setpoint_mismatches"`
	MissingCount       int                `json:"missing_count"`
	ExtraCount         int                `json:"extra_count"`
	MaxTimingDeltaMS   float64            `json:"max_timing_delta_ms"`
	MeanTimingDeltaMS  float64            `json:"mean_timing_delta_ms"`
	Completed          bool               `json:"completed"`
}

func (e Execution) Diff() CommandDiff {
	diff := CommandDiff{
		PlannedCount:       len(e.Planned),
		ActualCount:        len(e.Actual),
		Completed:          e.Status == StatusCompleted,
		SetpointMismatches: make([]SetpointMismatch, 0),
	}
	if diff.ActualCount < diff.PlannedCount {
		diff.MissingCount = diff.PlannedCount - diff.ActualCount
	} else if diff.ActualCount > diff.PlannedCount {
		diff.ExtraCount = diff.ActualCount - diff.PlannedCount
	}
	paired := diff.ActualCount
	if diff.PlannedCount < paired {
		paired = diff.PlannedCount
	}
	var timingSum float64
	for index := 0; index < paired; index++ {
		planned := e.Planned[index]
		actual := e.Actual[index]
		if planned.Setpoint != actual.Setpoint {
			diff.SetpointMismatches = append(diff.SetpointMismatches, SetpointMismatch{Index: index, Planned: planned.Setpoint, Actual: actual.Setpoint})
		}
		expected := e.StartedAt.Add(time.Duration(planned.OffsetMS) * time.Millisecond)
		deltaMS := actual.IssuedAt.Sub(expected).Seconds() * 1000
		timingSum += math.Abs(deltaMS)
		if abs := math.Abs(deltaMS); abs > diff.MaxTimingDeltaMS {
			diff.MaxTimingDeltaMS = abs
		}
	}
	if paired > 0 {
		diff.MeanTimingDeltaMS = timingSum / float64(paired)
	}
	return diff
}

// ExecutionArchive durably stores every dispatch (including rejected ones).
// On reload, executions that never reached a terminal state are marked
// interrupted: occupancy lives in the in-memory tank registry and is gone
// after a restart.
type ExecutionArchive struct {
	mu         sync.Mutex
	path       string
	executions map[string]Execution
	flushedAt  map[string]time.Time
}

// appendCommitEvery bounds how often intermediate command batches hit disk;
// the final state is committed unconditionally when execution finishes.
const appendCommitEvery = 200
const appendCommitInterval = 250 * time.Millisecond

func OpenExecutionArchive(directory string) (*ExecutionArchive, error) {
	archive := &ExecutionArchive{executions: make(map[string]Execution), flushedAt: make(map[string]time.Time)}
	if directory == "" {
		return archive, nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	archive.path = filepath.Join(directory, "wave-plan-executions.json")
	data, err := os.ReadFile(archive.path)
	if errors.Is(err, os.ErrNotExist) {
		return archive, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &archive.executions); err != nil {
		return nil, fmt.Errorf("read wave plan archive: %w", err)
	}
	if archive.executions == nil {
		return nil, errors.New("wave plan archive must contain an execution map")
	}
	for id, execution := range archive.executions {
		if execution.Status == StatusRunning {
			execution.Status = StatusInterrupted
			if execution.Reason == "" {
				execution.Reason = "service restarted before execution finished"
			}
			archive.executions[id] = execution
		}
	}
	return archive, nil
}

func (a *ExecutionArchive) commit(next map[string]Execution) error {
	if a.path != "" {
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(a.path), "wave-plans-*.tmp")
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
		if err := os.Rename(name, a.path); err != nil {
			return err
		}
	}
	a.executions = next
	return nil
}

// Put stores a snapshot of the execution.
func (a *ExecutionArchive) Put(execution Execution) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.clone()
	next[execution.ID] = cloneExecution(execution)
	return a.commit(next)
}

// AppendCommand records one issued command. Disk writes are throttled to at
// most one per appendCommitInterval (and every appendCommitEvery commands);
// callers must Put the terminal execution to force the final flush.
func (a *ExecutionArchive) AppendCommand(id string, command model.DeviceCommand) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	execution, exists := a.executions[id]
	if !exists {
		return ErrNotFound
	}
	execution = cloneExecution(execution)
	execution.Actual = append(execution.Actual, command)
	now := time.Now()
	last, seen := a.flushedAt[id]
	shouldCommit := !seen || len(execution.Actual)%appendCommitEvery == 0 || now.Sub(last) >= appendCommitInterval
	a.executions[id] = cloneExecution(execution)
	if shouldCommit {
		a.flushedAt[id] = now
		return a.commit(a.clone())
	}
	return nil
}

func (a *ExecutionArchive) Get(id string) (Execution, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	execution, exists := a.executions[id]
	return cloneExecution(execution), exists
}

func (a *ExecutionArchive) List() []Execution {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]Execution, 0, len(a.executions))
	for _, execution := range a.executions {
		result = append(result, cloneExecution(execution))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	return result
}

func (a *ExecutionArchive) clone() map[string]Execution {
	next := make(map[string]Execution, len(a.executions)+1)
	for id, execution := range a.executions {
		next[id] = cloneExecution(execution)
	}
	return next
}

func cloneExecution(value Execution) Execution {
	value.Planned = append([]PlannedCommand(nil), value.Planned...)
	value.Actual = append([]model.DeviceCommand(nil), value.Actual...)
	value.SchemeSnapshot.Sections = append([]string(nil), value.SchemeSnapshot.Sections...)
	value.Preflight.Checks = append([]CheckItem(nil), value.Preflight.Checks...)
	return value
}
