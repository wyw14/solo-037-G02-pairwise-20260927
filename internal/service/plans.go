package service

import (
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/plan"
)

func (r *Runtime) ListWaveSchemes() []plan.Scheme { return r.Plans.List() }

func (r *Runtime) GetWaveScheme(id string) (plan.Scheme, bool) { return r.Plans.Get(id) }

func (r *Runtime) CreateWaveScheme(input plan.SchemeInput) (plan.Scheme, error) {
	return r.Plans.Create(input)
}

func (r *Runtime) UpdateWaveScheme(id string, input plan.SchemeInput) (plan.Scheme, error) {
	return r.Plans.Update(id, input)
}

func (r *Runtime) DeleteWaveScheme(id string) bool { return r.Plans.Delete(id) }

func (r *Runtime) PreflightWavePlan(runID model.RunID, schemeID, section string) plan.Preflight {
	return r.PlanExecutor.Precheck(plan.DispatchInput{RunID: runID, SchemeID: schemeID, Section: section}, time.Now().UTC())
}

func (r *Runtime) DispatchWavePlan(input plan.DispatchInput) (plan.Execution, error) {
	return r.PlanExecutor.Dispatch(input, time.Now().UTC())
}

func (r *Runtime) AbortWavePlan(id string) (plan.Execution, error) {
	return r.PlanExecutor.Abort(id, time.Now().UTC())
}

func (r *Runtime) PlanExecutions() []plan.Execution { return r.PlanExecutor.List() }

func (r *Runtime) GetPlanExecution(id string) (plan.Execution, bool) {
	return r.PlanExecutor.Get(id)
}
