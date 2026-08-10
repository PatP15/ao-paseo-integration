package autoresume

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The notice every fixture dies on unless it says otherwise. 22:46 is the reset
// it names, four hours after watchNow, so a scheduled resume is comfortably in
// the future and a test that expects immediate delivery has to say so.
const limitNotice = "You've hit your usage limit. Try again at 10:46 PM"

var watchNow = time.Date(2026, time.August, 10, 18, 30, 0, 0, time.UTC)

type watchStore struct {
	settings  domain.AutoResumeSettings
	sessions  map[domain.SessionID]domain.SessionRecord
	rows      []domain.AutoResumeSchedule
	messages  []domain.ExecutionSessionMessage
	remote    map[domain.SessionID]bool
	enqueueEr error
	scheduleE error
}

func newWatchStore() *watchStore {
	return &watchStore{
		settings: domain.AutoResumeSettings{Enabled: true},
		sessions: map[domain.SessionID]domain.SessionRecord{},
		remote:   map[domain.SessionID]bool{},
	}
}

func (s *watchStore) GetAutoResumeSettings(context.Context) (domain.AutoResumeSettings, error) {
	return s.settings, nil
}

func (s *watchStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	rec, ok := s.sessions[id]
	return rec, ok, nil
}

func (s *watchStore) AutoResumeSessionState(
	_ context.Context, id domain.SessionID,
) (domain.AutoResumeSessionState, error) {
	var state domain.AutoResumeSessionState
	for _, row := range s.rows {
		if row.SessionID != id {
			continue
		}
		state.Attempts++
		if row.DetectedAt.After(state.LastDetectedAt) {
			state.LastDetectedAt = row.DetectedAt
		}
		if row.State == domain.AutoResumePending {
			state.Pending, state.HasPending = row, true
		}
	}
	return state, nil
}

func (s *watchStore) ScheduleAutoResume(_ context.Context, row domain.AutoResumeSchedule) error {
	if s.scheduleE != nil {
		return s.scheduleE
	}
	s.rows = append(s.rows, row)
	return nil
}

func (s *watchStore) ListDueAutoResumes(
	_ context.Context, at time.Time, limit int,
) ([]domain.AutoResumeSchedule, error) {
	var due []domain.AutoResumeSchedule
	for _, row := range s.rows {
		if row.State == domain.AutoResumePending && !row.ResumeAt.After(at) {
			due = append(due, row)
		}
		if len(due) == limit {
			break
		}
	}
	return due, nil
}

func (s *watchStore) SettleAutoResume(
	_ context.Context, id string, state domain.AutoResumeState, detail string, at time.Time,
) (bool, error) {
	for i, row := range s.rows {
		if row.ID != id || row.State != domain.AutoResumePending {
			continue
		}
		s.rows[i].State, s.rows[i].Detail, s.rows[i].UpdatedAt = state, detail, at
		return true, nil
	}
	return false, nil
}

func (s *watchStore) CancelPendingAutoResume(
	_ context.Context, id domain.SessionID, detail string, at time.Time,
) (bool, error) {
	cancelled := false
	for i, row := range s.rows {
		if row.SessionID != id || row.State != domain.AutoResumePending {
			continue
		}
		s.rows[i].State, s.rows[i].Detail, s.rows[i].UpdatedAt = domain.AutoResumeCancelled, detail, at
		cancelled = true
	}
	return cancelled, nil
}

func (s *watchStore) EnqueueExecutionSessionMessage(
	_ context.Context, message domain.ExecutionSessionMessage,
) (domain.ExecutionCommand, error) {
	if s.enqueueEr != nil {
		return domain.ExecutionCommand{}, s.enqueueEr
	}
	if !s.remote[message.SessionID] {
		return domain.ExecutionCommand{}, fmt.Errorf("session %s: %w", message.SessionID, domain.ErrSessionNotRemote)
	}
	s.messages = append(s.messages, message)
	return domain.ExecutionCommand{ID: message.CommandID, SessionID: message.SessionID}, nil
}

func (s *watchStore) pending(id domain.SessionID) (domain.AutoResumeSchedule, bool) {
	for _, row := range s.rows {
		if row.SessionID == id && row.State == domain.AutoResumePending {
			return row, true
		}
	}
	return domain.AutoResumeSchedule{}, false
}

func (s *watchStore) list() []domain.SessionRecord {
	out := make([]domain.SessionRecord, 0, len(s.sessions))
	for _, rec := range s.sessions {
		out = append(out, rec)
	}
	return out
}

func (s *watchStore) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return s.list(), nil
}

type fakePane struct {
	output map[string]string
	err    error
}

func (p *fakePane) GetOutput(_ context.Context, handle ports.RuntimeHandle, _ int) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return p.output[handle.ID], nil
}

type fakeLocal struct {
	sent []string
	err  error
}

func (l *fakeLocal) Resume(_ context.Context, id domain.SessionID, prompt string) error {
	if l.err != nil {
		return l.err
	}
	l.sent = append(l.sent, string(id)+"|"+prompt)
	return nil
}

type watchRig struct {
	store *watchStore
	pane  *fakePane
	local *fakeLocal
	w     *Watcher
	now   time.Time
	ids   int
}

func newRig(t *testing.T) *watchRig {
	t.Helper()
	rig := &watchRig{store: newWatchStore(), pane: &fakePane{output: map[string]string{}}, local: &fakeLocal{}, now: watchNow}
	rig.w = NewWatcher(rig.store, rig.store, rig.pane, rig.local, WatcherConfig{
		Clock:  func() time.Time { return rig.now },
		NewID:  func() string { rig.ids++; return fmt.Sprintf("id-%d", rig.ids) },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return rig
}

// exited registers a session that died with notice on its pane.
func (r *watchRig) exited(id domain.SessionID, notice string) {
	r.store.sessions[id] = domain.SessionRecord{
		ID:       id,
		Activity: domain.Activity{State: domain.ActivityExited},
		Metadata: domain.SessionMetadata{RuntimeHandleID: "handle-" + string(id), RuntimeLaunchID: "launch-1"},
	}
	r.pane.output["handle-"+string(id)] = "some earlier work\n" + notice
}

func (r *watchRig) poll(t *testing.T) {
	t.Helper()
	if err := r.w.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
}

func TestWatcherSchedulesAResumeAtTheParsedResetPlusGrace(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)

	rig.poll(t)

	row, ok := rig.store.pending("s1")
	if !ok {
		t.Fatal("no resume was scheduled")
	}
	want := time.Date(2026, time.August, 10, 22, 46, 0, 0, time.UTC).Add(domain.AutoResumeGrace)
	if !row.ResumeAt.Equal(want) {
		t.Fatalf("resumeAt = %s, want %s", row.ResumeAt, want)
	}
	if !row.ExactReset {
		t.Fatal("reset time came from the notice; ExactReset should say so")
	}
	if row.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", row.Attempt)
	}
	if row.LaunchID != "launch-1" {
		t.Fatalf("launchID = %q, want the launch that died", row.LaunchID)
	}
	// The notice is kept verbatim so a bad parse is diagnosable from the record.
	if !strings.Contains(row.Notice, "10:46 PM") {
		t.Fatalf("notice = %q, want the matched line", row.Notice)
	}
	// Not due yet, so nothing may have been sent.
	if len(rig.store.messages) != 0 || len(rig.local.sent) != 0 {
		t.Fatal("a resume was delivered before its reset time")
	}
}

func TestWatcherIgnoresAnExitThatIsNotAUsageLimit(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", "The agent finished and exited cleanly.")

	rig.poll(t)

	if _, ok := rig.store.pending("s1"); ok {
		t.Fatal("scheduled a resume for a session that did not hit a limit")
	}
}

func TestWatcherDoesNothingWhileTheToggleIsOff(t *testing.T) {
	rig := newRig(t)
	rig.store.settings.Enabled = false
	rig.exited("s1", limitNotice)

	rig.poll(t)

	if len(rig.store.rows) != 0 {
		t.Fatalf("scheduled %d resumes with the policy off", len(rig.store.rows))
	}
}

func TestWatcherSchedulesOnlyOnceWhileAResumeIsPending(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)

	rig.poll(t)
	rig.now = rig.now.Add(time.Hour)
	rig.poll(t)

	if len(rig.store.rows) != 1 {
		t.Fatalf("rows = %d, want 1: a pending resume must not be re-scheduled", len(rig.store.rows))
	}
}

func TestWatcherHoldsOffARetryUntilTheFloorHasPassed(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)
	rig.poll(t)
	// Settle the row as if delivery had already failed, leaving the session
	// exited with the same notice still on its pane.
	rig.store.rows[0].State = domain.AutoResumeFailed

	rig.now = rig.now.Add(domain.AutoResumeRetryFloor - time.Minute)
	rig.poll(t)
	if len(rig.store.rows) != 1 {
		t.Fatalf("rows = %d, want 1: the retry floor should hold a second attempt back", len(rig.store.rows))
	}

	rig.now = rig.now.Add(2 * time.Minute)
	rig.poll(t)
	if len(rig.store.rows) != 2 {
		t.Fatalf("rows = %d, want 2 once the floor has passed", len(rig.store.rows))
	}
	if rig.store.rows[1].Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", rig.store.rows[1].Attempt)
	}
}

func TestWatcherStopsAtTheCap(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)
	for range domain.MaxAutoResumesPerSession {
		rig.poll(t)
		// Retire each row the way a failed delivery would, then step past the
		// retry floor so only the cap can stop the next attempt.
		rig.store.rows[len(rig.store.rows)-1].State = domain.AutoResumeFailed
		rig.now = rig.now.Add(domain.AutoResumeRetryFloor + time.Minute)
	}
	if len(rig.store.rows) != domain.MaxAutoResumesPerSession {
		t.Fatalf("rows = %d, want %d", len(rig.store.rows), domain.MaxAutoResumesPerSession)
	}

	rig.poll(t)

	if len(rig.store.rows) != domain.MaxAutoResumesPerSession {
		t.Fatalf("rows = %d, want the cap to hold at %d",
			len(rig.store.rows), domain.MaxAutoResumesPerSession)
	}
}

func TestWatcherSendsARemoteResumeThroughTheOutbox(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)
	rig.store.remote["s1"] = true
	rig.poll(t)

	rig.now = time.Date(2026, time.August, 10, 22, 50, 0, 0, time.UTC)
	rig.poll(t)

	if len(rig.store.messages) != 1 {
		t.Fatalf("outbox messages = %d, want 1", len(rig.store.messages))
	}
	msg := rig.store.messages[0]
	if msg.Message != domain.DefaultAutoResumePrompt {
		t.Fatalf("message = %q, want the default resume prompt", msg.Message)
	}
	if msg.SentBy != AutoResumeActor {
		t.Fatalf("sentBy = %q, want %q so the timeline names AO as the sender", msg.SentBy, AutoResumeActor)
	}
	if len(rig.local.sent) != 0 {
		t.Fatal("a remote session was also resumed locally")
	}
	row := rig.store.rows[0]
	if row.State != domain.AutoResumeResumed {
		t.Fatalf("state = %q, want %q", row.State, domain.AutoResumeResumed)
	}
	if !strings.Contains(row.Detail, msg.CommandID) {
		t.Fatalf("detail = %q, want the outbox command id as the audit trail", row.Detail)
	}
}

func TestWatcherSendsALocalResumeThroughTheSessionRuntime(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)
	rig.store.settings.ResumePrompt = "pick up where you left off"
	rig.poll(t)

	rig.now = time.Date(2026, time.August, 10, 22, 50, 0, 0, time.UTC)
	rig.poll(t)

	if want := []string{"s1|pick up where you left off"}; len(rig.local.sent) != 1 || rig.local.sent[0] != want[0] {
		t.Fatalf("local sends = %v, want %v", rig.local.sent, want)
	}
	if len(rig.store.messages) != 0 {
		t.Fatal("a local session was queued on the execution outbox")
	}
	if rig.store.rows[0].State != domain.AutoResumeResumed {
		t.Fatalf("state = %q, want %q", rig.store.rows[0].State, domain.AutoResumeResumed)
	}
}

func TestWatcherRecordsAFailedDeliveryWithoutRetryingTheRow(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)
	rig.poll(t)
	rig.local.err = errors.New("tmux pane is gone")

	rig.now = time.Date(2026, time.August, 10, 22, 50, 0, 0, time.UTC)
	rig.poll(t)

	row := rig.store.rows[0]
	if row.State != domain.AutoResumeFailed {
		t.Fatalf("state = %q, want %q", row.State, domain.AutoResumeFailed)
	}
	if !strings.Contains(row.Detail, "tmux pane is gone") {
		t.Fatalf("detail = %q, want the delivery error", row.Detail)
	}

	// The settled row is never redelivered. A retry is a fresh detection off the
	// notice still on the pane — a new row, paced by the floor and counted
	// against the cap — not a second pass over this one.
	rig.local.err = nil
	rig.now = rig.now.Add(time.Minute)
	rig.poll(t)
	if got := rig.store.rows[0]; got.State != domain.AutoResumeFailed || got.Detail != row.Detail {
		t.Fatalf("failed row was rewritten: %+v", got)
	}
	if len(rig.store.rows) != 2 || rig.store.rows[1].Attempt != 2 {
		t.Fatalf("rows = %+v, want a second attempt scheduled from a fresh detection", rig.store.rows)
	}
	// The reset it names has already passed, so the new attempt is due at once
	// and this pass delivers it — the retry is real, not just recorded.
	if len(rig.local.sent) != 1 {
		t.Fatalf("local sends = %v, want the second attempt delivered", rig.local.sent)
	}
}

func TestWatcherCancelsAScheduledResumeWhenTheAgentComesBack(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)
	rig.poll(t)

	rec := rig.store.sessions["s1"]
	rec.Activity.State = domain.ActivityActive
	rig.store.sessions["s1"] = rec
	rig.poll(t)

	if _, ok := rig.store.pending("s1"); ok {
		t.Fatal("a running session still has a resume scheduled")
	}
	if rig.store.rows[0].State != domain.AutoResumeCancelled {
		t.Fatalf("state = %q, want %q", rig.store.rows[0].State, domain.AutoResumeCancelled)
	}
}

func TestWatcherCancelsAScheduledResumeForATerminatedSession(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)
	rig.poll(t)

	rec := rig.store.sessions["s1"]
	rec.IsTerminated = true
	rig.store.sessions["s1"] = rec
	rig.poll(t)

	if rig.store.rows[0].State != domain.AutoResumeCancelled {
		t.Fatalf("state = %q, want %q", rig.store.rows[0].State, domain.AutoResumeCancelled)
	}
	if len(rig.local.sent)+len(rig.store.messages) != 0 {
		t.Fatal("a terminated session was resumed")
	}
}

func TestWatcherCancelsAResumeThatWentStaleWhileAOWasDown(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)
	rig.poll(t)

	// AO comes back a day later. The limit it was waiting on lifted long ago and
	// nobody is watching; sending now would wake an agent for a forgotten reason.
	rig.now = rig.store.rows[0].ResumeAt.Add(domain.AutoResumeStaleAfter + time.Minute)
	rig.poll(t)

	if rig.store.rows[0].State != domain.AutoResumeCancelled {
		t.Fatalf("state = %q, want %q", rig.store.rows[0].State, domain.AutoResumeCancelled)
	}
	if len(rig.local.sent)+len(rig.store.messages) != 0 {
		t.Fatal("a stale resume was delivered")
	}
}

func TestWatcherFallsBackToThirtyMinutesWhenTheNoticeHasNoResetTime(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", "Error: usage limit reached for this account.")

	rig.poll(t)

	row, ok := rig.store.pending("s1")
	if !ok {
		t.Fatal("an untimed usage limit was ignored")
	}
	want := watchNow.Add(domain.AutoResumeFallbackDelay).Add(domain.AutoResumeGrace)
	if !row.ResumeAt.Equal(want) {
		t.Fatalf("resumeAt = %s, want the fallback %s", row.ResumeAt, want)
	}
	if row.ExactReset {
		t.Fatal("ExactReset should be false for a guessed reset time")
	}
}

func TestWatcherLeavesASessionAloneWhenItsPaneCannotBeRead(t *testing.T) {
	rig := newRig(t)
	rig.exited("s1", limitNotice)
	rig.pane.err = errors.New("host unreachable")

	rig.poll(t)

	if len(rig.store.rows) != 0 {
		t.Fatal("an unreadable pane was treated as evidence about why the agent died")
	}
}

func TestFindUsageLimitReadsTheNoticeOffTheBottomOfThePane(t *testing.T) {
	pane := strings.Join([]string{
		"$ ao spawn --issue 42",
		"Running tests at 09:15 ...",
		"You've hit your session limit · resets 7pm",
		"",
	}, "\n")

	limit, notice, ok := FindUsageLimit(pane, watchNow)

	if !ok {
		t.Fatal("the notice was not found")
	}
	if !limit.Exact {
		t.Fatal("the reset time is in the notice and should have been read")
	}
	// 09:15 sits above the notice and must not be mistaken for the reset time.
	want := time.Date(2026, time.August, 10, 19, 0, 0, 0, time.UTC)
	if !limit.ResetAt.Equal(want) {
		t.Fatalf("resetAt = %s, want %s", limit.ResetAt, want)
	}
	if !strings.Contains(notice, "session limit") {
		t.Fatalf("notice = %q, want the matched line", notice)
	}
}

func TestFindUsageLimitRejoinsANoticeTheTerminalWrapped(t *testing.T) {
	pane := "You've hit your usage limit. Try again at\n10:46 PM"

	limit, notice, ok := FindUsageLimit(pane, watchNow)

	if !ok {
		t.Fatal("the wrapped notice was not found")
	}
	if !limit.Exact {
		t.Fatal("the continuation line carries the reset time and should have been read")
	}
	want := time.Date(2026, time.August, 10, 22, 46, 0, 0, time.UTC)
	if !limit.ResetAt.Equal(want) {
		t.Fatalf("resetAt = %s, want %s", limit.ResetAt, want)
	}
	if !strings.Contains(notice, "10:46 PM") {
		t.Fatalf("notice = %q, want both halves of the wrapped line", notice)
	}
}

func TestFindUsageLimitIgnoresAPaneWithNoNotice(t *testing.T) {
	if _, _, ok := FindUsageLimit("all tests passed\nready for review", watchNow); ok {
		t.Fatal("a quiet pane was read as a usage limit")
	}
}
