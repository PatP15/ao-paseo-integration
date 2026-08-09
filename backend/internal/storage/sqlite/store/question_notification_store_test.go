package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func questionNotification(id, sessionID, questionID string, at time.Time) domain.NotificationRecord {
	return domain.NotificationRecord{
		ID: id, SessionID: domain.SessionID(sessionID), ProjectID: "project",
		Type: domain.NotificationNeedsInput, Title: "session needs input", Body: "Rebase or merge?",
		Status: domain.NotificationUnread, CreatedAt: at,
		WorkItemID: "work-1", QuestionID: questionID,
	}
}

func TestQuestionNotificationsDedupePerQuestionAndResolvePerQuestion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 9, 4, 0, 0, 0, time.UTC)
	sessionID := seedBoundSession(t, s, at)

	first, inserted, err := s.CreateNotification(ctx, questionNotification("ntf-1", string(sessionID), "q-1", at))
	if err != nil || !inserted {
		t.Fatalf("first create = (%#v, %v, %v)", first, inserted, err)
	}
	// A second open question on the same session is a second answerable item:
	// it must not be swallowed by the session-level dedupe.
	second, inserted, err := s.CreateNotification(ctx, questionNotification("ntf-2", string(sessionID), "q-2", at.Add(time.Second)))
	if err != nil || !inserted {
		t.Fatalf("second question create = (%#v, %v, %v)", second, inserted, err)
	}
	// Re-announcing the same question dedupes.
	if _, inserted, err := s.CreateNotification(ctx, questionNotification("ntf-3", string(sessionID), "q-1", at.Add(2*time.Second))); err != nil || inserted {
		t.Fatalf("duplicate question create inserted=%v err=%v", inserted, err)
	}

	resolvedAt := at.Add(time.Minute)
	resolved, err := s.ResolveQuestionNotifications(ctx, "q-1", resolvedAt)
	if err != nil || len(resolved) != 1 || resolved[0].ID != "ntf-1" || !resolved[0].ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("resolve q-1 = (%#v, %v)", resolved, err)
	}
	if resolved[0].QuestionID != "q-1" || resolved[0].WorkItemID != "work-1" {
		t.Fatalf("resolved row lost its question identity: %#v", resolved[0])
	}

	// The other question's notification stays open.
	page, err := s.ListNotifications(ctx, domain.NotificationListUnresolved, time.Time{}, "", 10)
	if err != nil || len(page) != 1 || page[0].ID != "ntf-2" || page[0].QuestionID != "q-2" {
		t.Fatalf("unresolved after q-1 = (%#v, %v)", page, err)
	}
}
