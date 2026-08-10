package autoresume

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

var testNow = time.Date(2026, time.August, 10, 18, 0, 0, 0, time.UTC)

type fakeStore struct {
	settings domain.AutoResumeSettings
	savedAt  time.Time
	pending  []domain.AutoResumeSchedule
	listErr  error
}

func (f *fakeStore) GetAutoResumeSettings(context.Context) (domain.AutoResumeSettings, error) {
	return f.settings, nil
}

func (f *fakeStore) PutAutoResumeSettings(
	_ context.Context, settings domain.AutoResumeSettings, at time.Time,
) (domain.AutoResumeSettings, error) {
	settings.UpdatedAt = at
	f.settings, f.savedAt = settings, at
	return settings, nil
}

func (f *fakeStore) ListPendingAutoResumes(context.Context) ([]domain.AutoResumeSchedule, error) {
	return f.pending, f.listErr
}

func newTestService(store Store) *Service {
	return newService(store, func() time.Time { return testNow })
}

func errCode(t *testing.T, err error) string {
	t.Helper()
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an apierr.Error", err)
	}
	return apiErr.Code
}

func TestAutoResumeIsOffWithNoStoredPrompt(t *testing.T) {
	settings, err := newTestService(&fakeStore{}).Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Enabled {
		t.Fatal("auto-resume defaulted to on")
	}
	if settings.ResumePrompt != "" {
		t.Fatalf("stored prompt = %q, want empty", settings.ResumePrompt)
	}
	// Empty storage still resolves to a usable prompt — that is what makes the
	// default improvable without migrating every install.
	if settings.Prompt() != domain.DefaultAutoResumePrompt {
		t.Fatalf("prompt = %q, want the shipped default", settings.Prompt())
	}
}

func TestSaveKeepsAnEmptyPromptEmptyRatherThanFreezingTodaysDefault(t *testing.T) {
	store := &fakeStore{}
	saved, err := newTestService(store).Save(context.Background(), SettingsInput{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Enabled {
		t.Fatal("enabled was not persisted")
	}
	if store.settings.ResumePrompt != "" {
		t.Fatalf("stored prompt = %q, want the default left unfrozen", store.settings.ResumePrompt)
	}
	if saved.Prompt() != domain.DefaultAutoResumePrompt {
		t.Fatalf("prompt = %q", saved.Prompt())
	}
	if store.savedAt != testNow {
		t.Fatalf("savedAt = %v, want %v", store.savedAt, testNow)
	}
}

func TestSaveStoresTheOperatorsOwnWording(t *testing.T) {
	store := &fakeStore{}
	saved, err := newTestService(store).Save(context.Background(), SettingsInput{
		Enabled: true, ResumePrompt: "  resume the migration checklist  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ResumePrompt != "resume the migration checklist" {
		t.Fatalf("prompt = %q, want it trimmed", saved.ResumePrompt)
	}
	if saved.Prompt() != "resume the migration checklist" {
		t.Fatalf("Prompt() = %q, want the operator's text", saved.Prompt())
	}
}

func TestSaveCanTurnAutoResumeBackOffWithoutLosingThePrompt(t *testing.T) {
	store := &fakeStore{settings: domain.AutoResumeSettings{Enabled: true, ResumePrompt: "keep going"}}
	saved, err := newTestService(store).Save(context.Background(), SettingsInput{
		Enabled: false, ResumePrompt: "keep going",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Enabled || saved.ResumePrompt != "keep going" {
		t.Fatalf("settings = %#v", saved)
	}
}

func TestSaveRefusesAPromptTheAgentCouldNotReceive(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{
			name:   "over the length cap",
			prompt: strings.Repeat("a", domain.MaxAutoResumePromptLen+1),
			want:   "RESUME_PROMPT_TOO_LONG",
		},
		{
			// This one fires unattended, so a truncating line break would be
			// discovered long after it had been silently doing damage.
			name:   "multi-line",
			prompt: "resume\nthe work",
			want:   "RESUME_PROMPT_SINGLE_LINE",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{}
			_, err := newTestService(store).Save(context.Background(), SettingsInput{
				Enabled: true, ResumePrompt: test.prompt,
			})
			if got := errCode(t, err); got != test.want {
				t.Fatalf("code = %q, want %q", got, test.want)
			}
			if !store.savedAt.IsZero() {
				t.Fatal("a refused prompt was still persisted")
			}
		})
	}
}

func TestPendingPassesThroughTheStoresOrder(t *testing.T) {
	// The store already returns these oldest-due-first; re-sorting here would
	// give the badge a different order than the watcher acts in.
	store := &fakeStore{pending: []domain.AutoResumeSchedule{
		{ID: "a", SessionID: "session-1", Attempt: 1, ResumeAt: testNow.Add(2 * time.Minute), ExactReset: true},
		{ID: "b", SessionID: "session-2", Attempt: 3, ResumeAt: testNow.Add(30 * time.Minute)},
	}}
	rows, err := newTestService(store).Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].SessionID != "session-1" || rows[1].SessionID != "session-2" {
		t.Fatalf("rows = %+v, want the store's order preserved", rows)
	}
}

func TestPendingReportsAnUnreadableSchedule(t *testing.T) {
	store := &fakeStore{listErr: errors.New("database is locked")}
	if _, err := newTestService(store).Pending(context.Background()); err == nil {
		t.Fatal("an unreadable schedule was reported as no pending resumes")
	}
}
