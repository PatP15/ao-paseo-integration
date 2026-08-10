package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestAutoResumeSettingsStartOffAndRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// The migration seeds the singleton, so a fresh install reads a real row
	// rather than a "not found" every caller would have to branch on.
	settings, err := s.GetAutoResumeSettings(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if settings.Enabled || settings.ResumePrompt != "" || !settings.UpdatedAt.IsZero() {
		t.Fatalf("fresh settings = %#v, want the off default", settings)
	}

	at := time.Date(2026, time.August, 10, 19, 0, 0, 0, time.UTC)
	if _, err := s.PutAutoResumeSettings(ctx,
		domain.AutoResumeSettings{Enabled: true, ResumePrompt: "pick the checklist back up"}, at); err != nil {
		t.Fatalf("put: %v", err)
	}
	stored, err := s.GetAutoResumeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Enabled || stored.ResumePrompt != "pick the checklist back up" || !stored.UpdatedAt.Equal(at) {
		t.Fatalf("stored = %#v", stored)
	}

	// Clearing the prompt must persist as empty: that is how the operator says
	// "use whatever default ships", and writing today's text in its place would
	// strand this install on it.
	later := at.Add(time.Hour)
	if _, err := s.PutAutoResumeSettings(ctx,
		domain.AutoResumeSettings{Enabled: true, ResumePrompt: ""}, later); err != nil {
		t.Fatal(err)
	}
	cleared, err := s.GetAutoResumeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ResumePrompt != "" {
		t.Fatalf("cleared prompt = %q, want empty", cleared.ResumePrompt)
	}
	if cleared.Prompt() != domain.DefaultAutoResumePrompt {
		t.Fatalf("resolved prompt = %q, want the shipped default", cleared.Prompt())
	}
}

func TestAutoResumeSettingsStaySingleton(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, time.August, 10, 20, 0, 0, 0, time.UTC)
	for i := range 3 {
		if _, err := s.PutAutoResumeSettings(ctx,
			domain.AutoResumeSettings{Enabled: i%2 == 0}, at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// Every write lands on the same row, so no reader ever has to choose which
	// of several policies is in force.
	settings, err := s.GetAutoResumeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled {
		t.Fatalf("settings = %#v, want the last write to win", settings)
	}
}
