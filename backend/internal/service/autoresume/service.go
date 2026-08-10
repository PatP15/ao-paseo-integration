// Package autoresume owns AO's policy for restarting a session whose agent was
// killed by a provider usage limit: the app-wide toggle, the operator's resume
// prompt, and (from U12-3) the scheduler that acts on them.
//
// It makes no remote calls. A resume is delivered by the same paths a human
// uses — the session runtime locally, the execution outbox remotely — so this
// package never learns what a host is.
package autoresume

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Store is the durable policy this service reads and writes, plus the pending
// side of the schedule the watcher keeps.
type Store interface {
	GetAutoResumeSettings(context.Context) (domain.AutoResumeSettings, error)
	PutAutoResumeSettings(context.Context, domain.AutoResumeSettings, time.Time) (domain.AutoResumeSettings, error)
	ListPendingAutoResumes(context.Context) ([]domain.AutoResumeSchedule, error)
}

// Service answers auto-resume settings reads and writes for the HTTP API.
type Service struct {
	store Store
	now   func() time.Time
}

// New returns the settings service.
func New(store Store) *Service { return newService(store, time.Now) }

func newService(store Store, now func() time.Time) *Service {
	return &Service{store: store, now: now}
}

// Settings returns the current policy.
func (s *Service) Settings(ctx context.Context) (domain.AutoResumeSettings, error) {
	settings, err := s.store.GetAutoResumeSettings(ctx)
	if err != nil {
		return domain.AutoResumeSettings{}, fmt.Errorf("read auto-resume settings: %w", err)
	}
	return settings, nil
}

// Pending returns every session whose resume is scheduled but not yet sent,
// oldest due first.
//
// It is one read for the whole app rather than a per-session probe: the badge
// that consumes it is drawn on a list of session cards, and a probe per card
// would put the query count on the size of the board.
func (s *Service) Pending(ctx context.Context) ([]domain.AutoResumeSchedule, error) {
	rows, err := s.store.ListPendingAutoResumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("read pending auto-resumes: %w", err)
	}
	return rows, nil
}

// SettingsInput is a complete replacement of the policy. Both fields are
// always written: a partial update would make "cleared the prompt" and "did not
// mention the prompt" the same request.
type SettingsInput struct {
	Enabled      bool
	ResumePrompt string
}

// Save replaces the policy.
//
// The prompt is trimmed but never defaulted: storing the shipped text on the
// operator's behalf would freeze this install on today's wording, and an empty
// stored value is the durable way to say "use whatever default ships".
func (s *Service) Save(ctx context.Context, in SettingsInput) (domain.AutoResumeSettings, error) {
	prompt := strings.TrimSpace(in.ResumePrompt)
	if len(prompt) > domain.MaxAutoResumePromptLen {
		return domain.AutoResumeSettings{}, apierr.Invalid("RESUME_PROMPT_TOO_LONG",
			fmt.Sprintf("resumePrompt must be at most %d characters", domain.MaxAutoResumePromptLen), nil)
	}
	// A line break submits at the agent's prompt, exactly as it does for a
	// human's message, so a multi-line resume prompt would arrive truncated
	// every time it fired — and unattended, with nobody watching it happen.
	if strings.ContainsAny(prompt, "\r\n") {
		return domain.AutoResumeSettings{}, apierr.Invalid("RESUME_PROMPT_SINGLE_LINE",
			"resumePrompt must be a single line: a line break submits at the agent's prompt and would send it truncated", nil)
	}
	saved, err := s.store.PutAutoResumeSettings(ctx,
		domain.AutoResumeSettings{Enabled: in.Enabled, ResumePrompt: prompt}, s.now().UTC())
	if err != nil {
		return domain.AutoResumeSettings{}, fmt.Errorf("save auto-resume settings: %w", err)
	}
	return saved, nil
}
