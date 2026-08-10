package controllers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	autoresumesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/autoresume"
)

type fakeAutoResumeService struct {
	settings domain.AutoResumeSettings
	saved    autoresumesvc.SettingsInput
	pending  []domain.AutoResumeSchedule
	err      error
}

func (f *fakeAutoResumeService) Pending(context.Context) ([]domain.AutoResumeSchedule, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.pending, nil
}

func (f *fakeAutoResumeService) Settings(context.Context) (domain.AutoResumeSettings, error) {
	if f.err != nil {
		return domain.AutoResumeSettings{}, f.err
	}
	return f.settings, nil
}

func (f *fakeAutoResumeService) Save(
	_ context.Context, in autoresumesvc.SettingsInput,
) (domain.AutoResumeSettings, error) {
	f.saved = in
	if f.err != nil {
		return domain.AutoResumeSettings{}, f.err
	}
	f.settings = domain.AutoResumeSettings{Enabled: in.Enabled, ResumePrompt: in.ResumePrompt}
	return f.settings, nil
}

// The daemon's default text is sent alongside the stored one so the UI can show
// it as placeholder copy without keeping a duplicate that drifts.
func TestAutoResumeSettingsRoundTripCarriesTheDaemonsDefault(t *testing.T) {
	svc := &fakeAutoResumeService{}
	srv := executionServer(t, httpd.APIDeps{AutoResume: svc})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/settings/auto-resume", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", resp.StatusCode, body)
	}
	var out controllers.AutoResumeSettingsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if out.Enabled || out.ResumePrompt != "" {
		t.Fatalf("fresh settings = %+v, want off with no stored prompt", out)
	}
	if out.DefaultResumePrompt != domain.DefaultAutoResumePrompt ||
		out.MaxResumesPerSession != domain.MaxAutoResumesPerSession {
		t.Fatalf("settings = %+v, want the daemon's default and cap", out)
	}

	resp, body = doJSON(t, http.MethodPut, srv.URL+"/api/v1/settings/auto-resume",
		`{"enabled":true,"resumePrompt":"carry on"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d, body = %s", resp.StatusCode, body)
	}
	if !svc.saved.Enabled || svc.saved.ResumePrompt != "carry on" {
		t.Fatalf("saved = %#v", svc.saved)
	}
}

func TestAutoResumeSettingsRefuseAnUnknownField(t *testing.T) {
	svc := &fakeAutoResumeService{}
	srv := executionServer(t, httpd.APIDeps{AutoResume: svc})

	// The body is a complete replacement, so an unknown key is a caller who
	// believes they are setting something the policy does not have.
	resp, body := doJSON(t, http.MethodPut, srv.URL+"/api/v1/settings/auto-resume",
		`{"enabled":true,"delayMinutes":5}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, body)
	}
	if svc.saved.Enabled {
		t.Fatal("a refused body was still saved")
	}
}

func TestAutoResumeSettingsSurfaceServiceRefusals(t *testing.T) {
	svc := &fakeAutoResumeService{
		err: apierr.Invalid("RESUME_PROMPT_SINGLE_LINE", "resumePrompt must be a single line", nil),
	}
	srv := executionServer(t, httpd.APIDeps{AutoResume: svc})
	resp, body := doJSON(t, http.MethodPut, srv.URL+"/api/v1/settings/auto-resume",
		`{"enabled":true,"resumePrompt":"a"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, body)
	}
}

// The surface stays mounted on a daemon with no service wired, answering the
// OpenAPI-backed 501 rather than a 404 that would read as "no such feature".
func TestAutoResumeRoutesReport501WithoutService(t *testing.T) {
	srv := executionServer(t, httpd.APIDeps{})
	for _, route := range []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPut, `{}`},
	} {
		resp, body := doJSON(t, route.method, srv.URL+"/api/v1/settings/auto-resume", route.body)
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s = %d, want 501 (body %s)", route.method, resp.StatusCode, body)
		}
	}
}

// The badge asks one question for a whole board of sessions, so the response
// has to carry enough per-row detail to render without a follow-up call.
func TestListPendingAutoResumesCarriesEachSessionsSchedule(t *testing.T) {
	resumeAt := time.Date(2026, time.August, 10, 22, 48, 0, 0, time.UTC)
	svc := &fakeAutoResumeService{pending: []domain.AutoResumeSchedule{
		{ID: "row-1", SessionID: "session-1", Attempt: 2, ResumeAt: resumeAt, ExactReset: true,
			Notice: "You've hit your usage limit. Try again at 10:46 PM"},
	}}
	srv := executionServer(t, httpd.APIDeps{AutoResume: svc})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/settings/auto-resume/pending", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var out controllers.ListPendingAutoResumesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Pending) != 1 {
		t.Fatalf("pending = %+v, want one row", out.Pending)
	}
	row := out.Pending[0]
	if row.SessionID != "session-1" || row.Attempt != 2 || !row.ExactReset || !row.ResumeAt.Equal(resumeAt) {
		t.Fatalf("row = %+v", row)
	}
	if out.MaxResumesPerSession != domain.MaxAutoResumesPerSession {
		t.Fatalf("maxResumesPerSession = %d, want %d", out.MaxResumesPerSession, domain.MaxAutoResumesPerSession)
	}
	// The matched notice is raw provider text kept for diagnosis; publishing it
	// would render untrusted output on every session card.
	if strings.Contains(string(body), "usage limit. Try again") {
		t.Fatalf("the matched notice leaked into the response: %s", body)
	}
}

// An empty schedule answers with a list, not null: the renderer maps over it.
func TestListPendingAutoResumesAnswersAnEmptyList(t *testing.T) {
	srv := executionServer(t, httpd.APIDeps{AutoResume: &fakeAutoResumeService{}})
	_, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/settings/auto-resume/pending", "")
	if !strings.Contains(string(body), `"pending":[]`) {
		t.Fatalf("body = %s, want an empty array", body)
	}
}

func TestListPendingAutoResumesReports501WithoutService(t *testing.T) {
	srv := executionServer(t, httpd.APIDeps{})
	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/settings/auto-resume/pending", "")
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", resp.StatusCode, body)
	}
}
