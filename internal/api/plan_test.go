package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/service"
)

func createSchemeViaHTTP(t *testing.T, server http.Handler, body string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/wave-schemes", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("create scheme: %d %s", recorder.Code, recorder.Body)
	}
	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data.ID
}

func createMeasuredRunHTTP(t *testing.T, server http.Handler) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	body := `{"name":"tow plan","model_name":"hull A","section":"section-a","carriage":"carriage-1"}`
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("create run: %d %s", recorder.Code, recorder.Body)
	}
	var response struct {
		Data struct {
			Operation struct {
				ID string `json:"id"`
			} `json:"operation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data.Operation.ID
}

func TestWavePlanHTTPLifecycle(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()

	shortID := createSchemeViaHTTP(t, server, `{"name":"regular","frequency_hz":0.8,"amplitude":0.15,"duration_ms":200,"sections":["section-a"]}`)
	longID := createSchemeViaHTTP(t, server, `{"name":"long","frequency_hz":0.8,"amplitude":0.15,"duration_ms":3000,"sections":["section-a"]}`)
	runID := createMeasuredRunHTTP(t, server)

	preflight := httptest.NewRecorder()
	longBody := `{"run_id":"` + runID + `","scheme_id":"` + longID + `","section":"section-a"}`
	server.ServeHTTP(preflight, httptest.NewRequest(http.MethodPost, "/api/wave-plans/preflight", strings.NewReader(longBody)))
	if preflight.Code != http.StatusOK || !strings.Contains(preflight.Body.String(), `"allowed":true`) {
		t.Fatalf("preflight: %d %s", preflight.Code, preflight.Body)
	}

	// Long plan occupies the device/section; an immediate second dispatch is a
	// hard 409, then the long plan is aborted.
	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/wave-plans", strings.NewReader(longBody)))
	if first.Code != http.StatusOK {
		t.Fatalf("dispatch long: %d %s", first.Code, first.Body)
	}
	var firstEnvelope struct {
		Data struct {
			Execution struct {
				ID string `json:"id"`
			} `json:"execution"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstEnvelope); err != nil {
		t.Fatal(err)
	}
	conflict := httptest.NewRecorder()
	server.ServeHTTP(conflict, httptest.NewRequest(http.MethodPost, "/api/wave-plans", strings.NewReader(longBody)))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("second dispatch: code = %d, want 409 (%s)", conflict.Code, conflict.Body)
	}
	abort := httptest.NewRecorder()
	server.ServeHTTP(abort, httptest.NewRequest(http.MethodPost, "/api/wave-plans/"+firstEnvelope.Data.Execution.ID+"/abort", nil))
	if abort.Code != http.StatusOK {
		t.Fatalf("abort: %d %s", abort.Code, abort.Body)
	}
	waitStatus(t, server, firstEnvelope.Data.Execution.ID, "aborted", "completed")

	// Short plan runs to completion; planned and actual counts must match.
	shortBody := `{"run_id":"` + runID + `","scheme_id":"` + shortID + `","section":"section-a"}`
	dispatch := httptest.NewRecorder()
	server.ServeHTTP(dispatch, httptest.NewRequest(http.MethodPost, "/api/wave-plans", strings.NewReader(shortBody)))
	if dispatch.Code != http.StatusOK {
		t.Fatalf("dispatch: %d %s", dispatch.Code, dispatch.Body)
	}
	var dispatched struct {
		Data struct {
			Execution struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"execution"`
		} `json:"data"`
	}
	if err := json.Unmarshal(dispatch.Body.Bytes(), &dispatched); err != nil {
		t.Fatal(err)
	}
	planID := dispatched.Data.Execution.ID

	detail := waitStatus(t, server, planID, "completed")
	var envelope struct {
		Data struct {
			Diff struct {
				PlannedCount int `json:"planned_count"`
				ActualCount  int `json:"actual_count"`
			} `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Diff.PlannedCount != envelope.Data.Diff.ActualCount || envelope.Data.Diff.PlannedCount == 0 {
		t.Fatalf("diff counts wrong: %s", detail.Body.String())
	}

	diff := httptest.NewRecorder()
	server.ServeHTTP(diff, httptest.NewRequest(http.MethodGet, "/api/wave-plans/"+planID+"/diff", nil))
	if diff.Code != http.StatusOK || !strings.Contains(diff.Body.String(), `"completed":true`) {
		t.Fatalf("diff endpoint: %d %s", diff.Code, diff.Body)
	}
}

func waitStatus(t *testing.T, server http.Handler, planID string, anyOf ...string) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		detail := httptest.NewRecorder()
		server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/wave-plans/"+planID, nil))
		var envelope struct {
			Data struct {
				Execution struct {
					Status string `json:"status"`
				} `json:"execution"`
			} `json:"data"`
		}
		if err := json.Unmarshal(detail.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		for _, status := range anyOf {
			if envelope.Data.Execution.Status == status {
				return detail
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("plan %s did not reach %v in time", planID, anyOf)
	return nil
}

func TestWavePlanHTTPRejectsUnknownExperiment(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	schemeID := createSchemeViaHTTP(t, server, `{"name":"regular","frequency_hz":0.8,"amplitude":0.15,"duration_ms":200}`)
	body := `{"run_id":"00000000-0000-0000-0000-000000000000","scheme_id":"` + schemeID + `","section":"section-a"}`
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/wave-plans", strings.NewReader(body)))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409: %s", recorder.Code, recorder.Body)
	}
}

func TestWaveSchemeValidationHTTP(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/wave-schemes", strings.NewReader(`{"name":"bad","frequency_hz":0,"amplitude":0.15,"duration_ms":200}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", recorder.Code, recorder.Body)
	}
}

func TestWaveSchemesPersistAcrossRestart(t *testing.T) {
	directory := t.TempDir()
	runtime, err := service.OpenRuntime(directory)
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	createSchemeViaHTTP(t, server, `{"name":"kept","frequency_hz":1.2,"amplitude":0.2,"duration_ms":300,"sections":[]}`)

	reopened, err := service.OpenRuntime(directory)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewRouter(reopened).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/wave-schemes", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"kept"`) {
		t.Fatalf("scheme not persisted: %d %s", recorder.Code, recorder.Body)
	}
}
