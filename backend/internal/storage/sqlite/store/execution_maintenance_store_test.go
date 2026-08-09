package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestHostMaintenanceRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 9, 6, 0, 0, 0, time.UTC)
	seedBoundSession(t, s, at) // registers worker-1

	if err := s.ReplaceExecutionHostSkills(ctx, "worker-1", []domain.ExecutionHostSkill{
		{HostID: "worker-1", Name: "deploy", Description: "Deploy safely"},
		{HostID: "worker-1", Name: "review", Description: "Review a PR"},
	}, at); err != nil {
		t.Fatal(err)
	}
	// Replacement is whole: a skill removed on the host disappears here too.
	if err := s.ReplaceExecutionHostSkills(ctx, "worker-1", []domain.ExecutionHostSkill{
		{HostID: "worker-1", Name: "deploy", Description: "Deploy safely v2"},
	}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	skills, err := s.ListExecutionHostSkills(ctx, "worker-1")
	if err != nil || len(skills) != 1 {
		t.Fatalf("skills = (%#v, %v)", skills, err)
	}
	if skills[0].Description != "Deploy safely v2" || !skills[0].CapturedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("skill = %#v", skills[0])
	}

	if _, found, err := s.GetExecutionHostPrefs(ctx, "worker-1"); err != nil || found {
		t.Fatalf("prefs before write = found=%v err=%v", found, err)
	}
	prefs := domain.ExecutionHostPrefs{
		HostID: "worker-1", Content: `{"providers":{}}`, SHA256: "abc123", Exists: true, ConfirmedAt: at,
	}
	if err := s.UpsertExecutionHostPrefs(ctx, prefs); err != nil {
		t.Fatal(err)
	}
	stored, found, err := s.GetExecutionHostPrefs(ctx, "worker-1")
	if err != nil || !found || stored.Content != prefs.Content || stored.SHA256 != "abc123" ||
		!stored.Exists || !stored.ConfirmedAt.Equal(at) {
		t.Fatalf("prefs = (%#v, %v, %v)", stored, found, err)
	}
}
