package service

import (
    "errors"
    "time"

    "github.com/wyw14/solo-037/internal/plan"
)

var errPlanNotFound = errors.New("plan not found")

func (r *Runtime) SaveWavePlan(input plan.PlanInput) (plan.WavePlan, error) {
    return r.Plans.Save(input, time.Now().UTC())
}

func (r *Runtime) WavePlans() []plan.WavePlan { return r.Plans.List() }

func (r *Runtime) WavePlan(id string) (plan.WavePlan, bool) { return r.Plans.Get(id) }

func (r *Runtime) DeleteWavePlan(id string) error { return r.Plans.Delete(id) }

// PreflightWavePlan checks a plan against current tests and equipment
// occupancy without issuing any command.
func (r *Runtime) PreflightWavePlan(id string, section string) (plan.PreflightResult, error) {
    saved, ok := r.Plans.Get(id)
    if !ok {
        return plan.PreflightResult{}, errPlanNotFound
    }
    return r.PlanExecutor.Preflight(saved, section), nil
}

// ExecuteWavePlan issues the plan to the wave controller after preflight;
// the returned execution carries the checks and, once finished, the diff
// between planned and actual commands.
func (r *Runtime) ExecuteWavePlan(id string, section string) (plan.Execution, error) {
    return r.PlanExecutor.Execute(id, section, time.Now().UTC())
}

func (r *Runtime) PlanExecutions() []plan.Execution { return r.PlanExecutor.List() }

func (r *Runtime) PlanExecution(id string) (plan.Execution, bool) { return r.PlanExecutor.Get(id) }
