package controllers

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	autoresumesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/autoresume"
)

// AutoResumeSettingsService is the controller-facing contract for the app-wide
// usage-limit auto-resume policy.
type AutoResumeSettingsService interface {
	Settings(ctx context.Context) (domain.AutoResumeSettings, error)
	Save(ctx context.Context, in autoresumesvc.SettingsInput) (domain.AutoResumeSettings, error)
	Pending(ctx context.Context) ([]domain.AutoResumeSchedule, error)
}

// AutoResumeController owns the auto-resume settings routes.
type AutoResumeController struct {
	Svc AutoResumeSettingsService
}

// Register mounts the auto-resume settings routes.
func (c *AutoResumeController) Register(r chi.Router) {
	r.Get("/settings/auto-resume", c.get)
	r.Put("/settings/auto-resume", c.put)
	r.Get("/settings/auto-resume/pending", c.pending)
}

// AutoResumeSettingsResponse is the app-wide auto-resume policy.
//
// ResumePrompt is what is stored — empty when the operator has not customised
// it — and DefaultResumePrompt is what an empty value resolves to. Both are
// sent so the UI can show the default as placeholder text without hardcoding a
// copy of it that would drift from the daemon's.
type AutoResumeSettingsResponse struct {
	Enabled              bool   `json:"enabled"`
	ResumePrompt         string `json:"resumePrompt"`
	DefaultResumePrompt  string `json:"defaultResumePrompt" description:"What an empty resumePrompt resolves to."`
	MaxResumesPerSession int    `json:"maxResumesPerSession" description:"Automatic resumes AO will perform for one session before it stops."`
}

// PutAutoResumeSettingsRequest replaces the whole policy. There is no partial
// update: without one, clearing the prompt and omitting it would be the same
// request.
type PutAutoResumeSettingsRequest struct {
	Enabled      bool   `json:"enabled"`
	ResumePrompt string `json:"resumePrompt" description:"Single-line text. Empty means use the daemon's default."`
}

// PendingAutoResume is one session AO is holding until a provider limit lifts.
//
// The notice AO matched is deliberately not published here: it is a raw
// terminal line kept for diagnosis, and a session list is the wrong place to
// render untrusted provider text.
type PendingAutoResume struct {
	SessionID  string    `json:"sessionId"`
	ResumeAt   time.Time `json:"resumeAt" description:"When AO will send the resume, already past the reset by a grace period."`
	Attempt    int       `json:"attempt" description:"1-based count of this session's automatic resumes, including this one."`
	ExactReset bool      `json:"exactReset" description:"False when the notice carried no readable reset time and AO fell back to a fixed wait."`
}

// ListPendingAutoResumesResponse is every unsent resume, oldest due first. It
// answers the whole session list in one read rather than one probe per card.
type ListPendingAutoResumesResponse struct {
	Pending              []PendingAutoResume `json:"pending"`
	MaxResumesPerSession int                 `json:"maxResumesPerSession" description:"Automatic resumes AO will perform for one session before it stops."`
}

func (c *AutoResumeController) pending(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/settings/auto-resume/pending")
		return
	}
	rows, err := c.Svc.Pending(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := ListPendingAutoResumesResponse{
		Pending:              make([]PendingAutoResume, 0, len(rows)),
		MaxResumesPerSession: domain.MaxAutoResumesPerSession,
	}
	for _, row := range rows {
		out.Pending = append(out.Pending, PendingAutoResume{
			SessionID:  string(row.SessionID),
			ResumeAt:   row.ResumeAt,
			Attempt:    row.Attempt,
			ExactReset: row.ExactReset,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

func (c *AutoResumeController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/settings/auto-resume")
		return
	}
	settings, err := c.Svc.Settings(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, autoResumeResponse(settings))
}

func (c *AutoResumeController) put(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPut, "/api/v1/settings/auto-resume")
		return
	}
	var in PutAutoResumeSettingsRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON",
			"Invalid JSON body: auto-resume settings accept only enabled and resumePrompt", nil)
		return
	}
	settings, err := c.Svc.Save(r.Context(), autoresumesvc.SettingsInput{
		Enabled: in.Enabled, ResumePrompt: in.ResumePrompt,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, autoResumeResponse(settings))
}

func autoResumeResponse(settings domain.AutoResumeSettings) AutoResumeSettingsResponse {
	return AutoResumeSettingsResponse{
		Enabled:              settings.Enabled,
		ResumePrompt:         settings.ResumePrompt,
		DefaultResumePrompt:  domain.DefaultAutoResumePrompt,
		MaxResumesPerSession: domain.MaxAutoResumesPerSession,
	}
}
