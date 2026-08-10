package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var scheduleAt = time.Date(2026, time.August, 10, 18, 30, 0, 0, time.UTC)

func newSchedule(id string, session domain.SessionID, attempt int, resumeAt time.Time) domain.AutoResumeSchedule {
	return domain.AutoResumeSchedule{
		ID: id, SessionID: session, LaunchID: "launch-1", Attempt: attempt,
		State: domain.AutoResumePending, ResumeAt: resumeAt, ExactReset: true,
		Notice:     "You've hit your usage limit. Try again at 10:46 PM",
		DetectedAt: scheduleAt, UpdatedAt: scheduleAt,
	}
}

func TestAutoResumeScheduleRoundTripsAndReportsSessionState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	state, err := s.AutoResumeSessionState(ctx, "s1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Attempts != 0 || state.HasPending || !state.LastDetectedAt.IsZero() {
		t.Fatalf("fresh state = %#v, want empty", state)
	}

	due := scheduleAt.Add(4 * time.Hour)
	if err := s.ScheduleAutoResume(ctx, newSchedule("r1", "s1", 1, due)); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	state, err = s.AutoResumeSessionState(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Attempts != 1 || !state.HasPending {
		t.Fatalf("state = %#v, want one pending attempt", state)
	}
	if !state.LastDetectedAt.Equal(scheduleAt) {
		t.Fatalf("lastDetectedAt = %s, want %s", state.LastDetectedAt, scheduleAt)
	}
	row := state.Pending
	if row.ID != "r1" || row.LaunchID != "launch-1" || !row.ExactReset || !row.ResumeAt.Equal(due) {
		t.Fatalf("pending row = %#v", row)
	}
}

func TestAutoResumeScheduleAllowsOnlyOnePendingRowPerSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	due := scheduleAt.Add(time.Hour)
	if err := s.ScheduleAutoResume(ctx, newSchedule("r1", "s1", 1, due)); err != nil {
		t.Fatal(err)
	}

	// The partial unique index is the concurrency guard: two ticks racing on one
	// session must not both schedule, or the agent is nudged twice for one death.
	if err := s.ScheduleAutoResume(ctx, newSchedule("r2", "s1", 2, due)); err == nil {
		t.Fatal("a second pending resume was accepted for the same session")
	}

	// Settling the first frees the session for a genuine second attempt.
	if _, err := s.SettleAutoResume(ctx, "r1", domain.AutoResumeFailed, "host unreachable", due); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleAutoResume(ctx, newSchedule("r2", "s1", 2, due)); err != nil {
		t.Fatalf("second attempt refused after the first settled: %v", err)
	}
	state, err := s.AutoResumeSessionState(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	// Both rows count against the cap; the failed one still spent an act.
	if state.Attempts != 2 || state.Pending.ID != "r2" {
		t.Fatalf("state = %#v, want two attempts with r2 pending", state)
	}
}

func TestAutoResumeDueListOnlyReturnsPendingRowsThatHaveComeDue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	soon, later := scheduleAt.Add(time.Minute), scheduleAt.Add(time.Hour)
	if err := s.ScheduleAutoResume(ctx, newSchedule("r1", "s1", 1, soon)); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleAutoResume(ctx, newSchedule("r2", "s2", 1, later)); err != nil {
		t.Fatal(err)
	}

	due, err := s.ListDueAutoResumes(ctx, scheduleAt.Add(2*time.Minute), 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 1 || due[0].ID != "r1" {
		t.Fatalf("due = %#v, want only r1", due)
	}

	if _, err := s.SettleAutoResume(ctx, "r1", domain.AutoResumeResumed, "local agent relaunch", soon); err != nil {
		t.Fatal(err)
	}
	due, err = s.ListDueAutoResumes(ctx, later.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	// A settled row is history and is never picked up again, however overdue.
	if len(due) != 1 || due[0].ID != "r2" {
		t.Fatalf("due = %#v, want only the still-pending r2", due)
	}

	pending, err := s.ListPendingAutoResumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].SessionID != "s2" {
		t.Fatalf("pending = %#v, want only s2 waiting", pending)
	}
}

func TestAutoResumeSettleIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.ScheduleAutoResume(ctx, newSchedule("r1", "s1", 1, scheduleAt)); err != nil {
		t.Fatal(err)
	}

	settled, err := s.SettleAutoResume(ctx, "r1", domain.AutoResumeResumed, "outbox command c1", scheduleAt)
	if err != nil || !settled {
		t.Fatalf("first settle = %v, %v; want true, nil", settled, err)
	}
	// A racing tick must not overwrite a verdict that is already recorded.
	again, err := s.SettleAutoResume(ctx, "r1", domain.AutoResumeFailed, "second opinion", scheduleAt)
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("an already-settled row took a second verdict")
	}
	state, err := s.AutoResumeSessionState(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if state.HasPending {
		t.Fatal("a settled row is still reported as pending")
	}

	if _, err := s.SettleAutoResume(ctx, "r1", domain.AutoResumePending, "", scheduleAt); err == nil {
		t.Fatal("pending was accepted as a settled state")
	}
}

func TestAutoResumeCancelClearsOnlyTheSessionsPendingRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.ScheduleAutoResume(ctx, newSchedule("r1", "s1", 1, scheduleAt)); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleAutoResume(ctx, newSchedule("r2", "s2", 1, scheduleAt)); err != nil {
		t.Fatal(err)
	}

	cancelled, err := s.CancelPendingAutoResume(ctx, "s1", "agent is running again", scheduleAt)
	if err != nil || !cancelled {
		t.Fatalf("cancel = %v, %v; want true, nil", cancelled, err)
	}
	// Cancelling a session with nothing scheduled is a no-op, not an error: the
	// watcher calls it on every healthy session it sees.
	again, err := s.CancelPendingAutoResume(ctx, "s1", "agent is running again", scheduleAt)
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("cancel reported work on a session with no pending resume")
	}
	pending, err := s.ListPendingAutoResumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].SessionID != "s2" {
		t.Fatalf("pending = %#v, want s2 untouched", pending)
	}
}
