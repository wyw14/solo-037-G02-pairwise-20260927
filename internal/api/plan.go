package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/plan"
)

type PlanService interface {
	ListWaveSchemes() []plan.Scheme
	GetWaveScheme(id string) (plan.Scheme, bool)
	CreateWaveScheme(input plan.SchemeInput) (plan.Scheme, error)
	UpdateWaveScheme(id string, input plan.SchemeInput) (plan.Scheme, error)
	DeleteWaveScheme(id string) bool
	PreflightWavePlan(runID model.RunID, schemeID, section string) plan.Preflight
	DispatchWavePlan(input plan.DispatchInput) (plan.Execution, error)
	AbortWavePlan(id string) (plan.Execution, error)
	PlanExecutions() []plan.Execution
	GetPlanExecution(id string) (plan.Execution, bool)
}

type planDispatchRequest struct {
	RunID    model.RunID `json:"run_id"`
	SchemeID string      `json:"scheme_id"`
	Section  string      `json:"section"`
}

type planExecutionResponse struct {
	Execution plan.Execution   `json:"execution"`
	Diff      plan.CommandDiff `json:"diff"`
}

// planErrorStatus maps domain failures onto the hard-reject semantics agreed
// for dispatch: failed preflight/non-running plans are conflicts, unknown
// records are 404 and everything else (validation) is a bad request.
func planErrorStatus(err error) int {
	switch {
	case errors.Is(err, plan.ErrPlanRejected), errors.Is(err, plan.ErrNotRunning):
		return http.StatusConflict
	case errors.Is(err, plan.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func (r *Router) listWaveSchemes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.ListWaveSchemes())
}

func (r *Router) createWaveScheme(w http.ResponseWriter, request *http.Request) {
	var input plan.SchemeInput
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scheme, err := r.dependencies.CreateWaveScheme(input)
	if err != nil {
		http.Error(w, err.Error(), planErrorStatus(err))
		return
	}
	writeJSON(w, scheme)
}

func (r *Router) getWaveScheme(w http.ResponseWriter, request *http.Request) {
	scheme, ok := r.dependencies.GetWaveScheme(chi.URLParam(request, "id"))
	if !ok {
		http.Error(w, "scheme not found", http.StatusNotFound)
		return
	}
	writeJSON(w, scheme)
}

func (r *Router) updateWaveScheme(w http.ResponseWriter, request *http.Request) {
	var input plan.SchemeInput
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scheme, err := r.dependencies.UpdateWaveScheme(chi.URLParam(request, "id"), input)
	if err != nil {
		http.Error(w, err.Error(), planErrorStatus(err))
		return
	}
	writeJSON(w, scheme)
}

func (r *Router) deleteWaveScheme(w http.ResponseWriter, request *http.Request) {
	if !r.dependencies.DeleteWaveScheme(chi.URLParam(request, "id")) {
		http.Error(w, "scheme not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodePlanDispatch(request *http.Request) (planDispatchRequest, error) {
	var input planDispatchRequest
	if err := decodeMeasurementBody(request, &input); err != nil {
		return input, err
	}
	return input, nil
}

func (r *Router) preflightWavePlan(w http.ResponseWriter, request *http.Request) {
	input, err := decodePlanDispatch(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, r.dependencies.PreflightWavePlan(input.RunID, input.SchemeID, input.Section))
}

func (r *Router) dispatchWavePlan(w http.ResponseWriter, request *http.Request) {
	input, err := decodePlanDispatch(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	execution, err := r.dependencies.DispatchWavePlan(plan.DispatchInput{RunID: input.RunID, SchemeID: input.SchemeID, Section: input.Section})
	if err != nil {
		http.Error(w, err.Error(), planErrorStatus(err))
		return
	}
	writeJSON(w, planExecutionResponse{Execution: execution, Diff: execution.Diff()})
}

func (r *Router) listPlanExecutions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.PlanExecutions())
}

func (r *Router) getPlanExecution(w http.ResponseWriter, request *http.Request) {
	execution, ok := r.dependencies.GetPlanExecution(chi.URLParam(request, "id"))
	if !ok {
		http.Error(w, "plan execution not found", http.StatusNotFound)
		return
	}
	writeJSON(w, planExecutionResponse{Execution: execution, Diff: execution.Diff()})
}

func (r *Router) planExecutionDiff(w http.ResponseWriter, request *http.Request) {
	execution, ok := r.dependencies.GetPlanExecution(chi.URLParam(request, "id"))
	if !ok {
		http.Error(w, "plan execution not found", http.StatusNotFound)
		return
	}
	writeJSON(w, execution.Diff())
}

func (r *Router) abortPlanExecution(w http.ResponseWriter, request *http.Request) {
	execution, err := r.dependencies.AbortWavePlan(chi.URLParam(request, "id"))
	if err != nil {
		http.Error(w, err.Error(), planErrorStatus(err))
		return
	}
	writeJSON(w, execution)
}

func (r *Router) plansPage(w http.ResponseWriter, request *http.Request) {
	http.ServeFile(w, request, "web/plans.html")
}
