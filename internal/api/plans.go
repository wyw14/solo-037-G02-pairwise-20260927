package api

import (
    "encoding/json"
    "net/http"

    "github.com/go-chi/chi/v5"
    "github.com/wyw14/solo-037/internal/plan"
)

type PlanService interface {
    SaveWavePlan(plan.PlanInput) (plan.WavePlan, error)
    WavePlans() []plan.WavePlan
    WavePlan(string) (plan.WavePlan, bool)
    DeleteWavePlan(string) error
    PreflightWavePlan(string, string) (plan.PreflightResult, error)
    ExecuteWavePlan(string, string) (plan.Execution, error)
    PlanExecutions() []plan.Execution
    PlanExecution(string) (plan.Execution, bool)
}

type sectionRequest struct {
    Section string `json:"section"`
}

func (r *Router) wavePlans(w http.ResponseWriter, _ *http.Request) {
    writeJSON(w, r.dependencies.WavePlans())
}

func (r *Router) saveWavePlan(w http.ResponseWriter, request *http.Request) {
    var input plan.PlanInput
    if err := decodeMeasurementBody(request, &input); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    value, err := r.dependencies.SaveWavePlan(input)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    writeJSON(w, value)
}

func (r *Router) wavePlan(w http.ResponseWriter, request *http.Request) {
    value, ok := r.dependencies.WavePlan(chi.URLParam(request, "id"))
    if !ok {
        http.Error(w, "plan not found", http.StatusNotFound)
        return
    }
    writeJSON(w, value)
}

func (r *Router) deleteWavePlan(w http.ResponseWriter, request *http.Request) {
    if err := r.dependencies.DeleteWavePlan(chi.URLParam(request, "id")); err != nil {
        http.Error(w, err.Error(), http.StatusNotFound)
        return
    }
    w.WriteHeader(http.StatusNoContent)
}

func (r *Router) preflightWavePlan(w http.ResponseWriter, request *http.Request) {
    var input sectionRequest
    if err := decodeMeasurementBody(request, &input); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    value, err := r.dependencies.PreflightWavePlan(chi.URLParam(request, "id"), input.Section)
    if err != nil {
        http.Error(w, err.Error(), http.StatusNotFound)
        return
    }
    writeJSON(w, value)
}

func (r *Router) executeWavePlan(w http.ResponseWriter, request *http.Request) {
    var input sectionRequest
    if err := decodeMeasurementBody(request, &input); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    value, err := r.dependencies.ExecuteWavePlan(chi.URLParam(request, "id"), input.Section)
    if err != nil {
        http.Error(w, err.Error(), http.StatusNotFound)
        return
    }
    if value.Status == plan.StatusRejected {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusConflict)
        _ = json.NewEncoder(w).Encode(map[string]any{"status": "rejected", "data": value})
        return
    }
    writeJSON(w, value)
}

func (r *Router) planExecutions(w http.ResponseWriter, _ *http.Request) {
    writeJSON(w, r.dependencies.PlanExecutions())
}

func (r *Router) planExecution(w http.ResponseWriter, request *http.Request) {
    value, ok := r.dependencies.PlanExecution(chi.URLParam(request, "id"))
    if !ok {
        http.Error(w, "execution not found", http.StatusNotFound)
        return
    }
    writeJSON(w, value)
}

func (r *Router) plansPage(w http.ResponseWriter, request *http.Request) {
    http.ServeFile(w, request, "web/plans.html")
}
