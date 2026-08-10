package autoresume

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Watcher defaults.
const (
	// DefaultWatchInterval is the poll period. It is the resolution of the
	// resume time, not of the detection: a limit resets on the hour and the
	// grace period already absorbs half a minute of slack.
	DefaultWatchInterval = 30 * time.Second
	// DefaultNoticeLines is how much of the pane's tail is scanned. A usage
	// limit is the last thing an agent prints before it dies, so the notice is
	// within a few lines of the bottom; reading further back only widens the
	// window for an old notice to be mistaken for a fresh one.
	DefaultNoticeLines = 40
	// dueBatch bounds one delivery pass. Every send touches a runtime or a
	// host, so a backlog is worked through over several ticks rather than in
	// one burst.
	dueBatch = 25
)

// SessionSource lists the sessions the watcher inspects.
type SessionSource interface {
	ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
}

// OutputReader reads the tail of a session's terminal. The daemon supplies the
// runtime router, so this resolves to the local pane for a local session and to
// the host's terminal for a remote one — the watcher never learns which.
type OutputReader interface {
	GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error)
}

// ScheduleStore is the durable surface the watcher reads and writes.
//
// There is deliberately nothing here that kills, terminates, or reconfigures a
// session. The only outward effect this package can have is a prompt, and the
// only rows it writes are its own schedule and the outbox command that carries
// that prompt.
type ScheduleStore interface {
	GetAutoResumeSettings(context.Context) (domain.AutoResumeSettings, error)
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	AutoResumeSessionState(context.Context, domain.SessionID) (domain.AutoResumeSessionState, error)
	ScheduleAutoResume(context.Context, domain.AutoResumeSchedule) error
	ListDueAutoResumes(context.Context, time.Time, int) ([]domain.AutoResumeSchedule, error)
	SettleAutoResume(context.Context, string, domain.AutoResumeState, string, time.Time) (bool, error)
	CancelPendingAutoResume(context.Context, domain.SessionID, string, time.Time) (bool, error)
	// EnqueueExecutionSessionMessage refuses a session with no execution
	// binding (domain.ErrSessionNotRemote) before it writes anything, which is
	// what lets the watcher use it to tell remote sessions from local ones.
	EnqueueExecutionSessionMessage(context.Context, domain.ExecutionSessionMessage) (domain.ExecutionCommand, error)
}

// LocalResumer delivers a resume to a session that runs on this machine.
//
// A local agent that hit a usage limit has exited, so its pane has no process
// to type at: the implementation relaunches the persisted harness session and
// then sends the prompt, which is exactly the two steps a human takes in the
// UI. Keeping both behind one method leaves this package free of session
// manager vocabulary.
type LocalResumer interface {
	Resume(ctx context.Context, id domain.SessionID, prompt string) error
}

// WatcherConfig tunes the watcher. Every field has a working default.
type WatcherConfig struct {
	Tick        time.Duration
	NoticeLines int
	Clock       func() time.Time
	NewID       func() string
	Logger      *slog.Logger
}

// Watcher restarts sessions whose agent was killed by a provider usage limit.
//
// One pass does two independent things: it schedules a resume for every session
// that has newly died on a limit, and it delivers every resume whose time has
// come. They are separate because the gap between them is the whole point —
// hours, usually — and because a daemon restart in between must not lose the
// appointment. The schedule is durable; nothing here is held in memory.
type Watcher struct {
	store       ScheduleStore
	sessions    SessionSource
	runtime     OutputReader
	local       LocalResumer
	tick        time.Duration
	noticeLines int
	now         func() time.Time
	newID       func() string
	logger      *slog.Logger
}

// NewWatcher builds the watcher. It performs no I/O.
func NewWatcher(
	store ScheduleStore, sessions SessionSource, runtime OutputReader, local LocalResumer, cfg WatcherConfig,
) *Watcher {
	w := &Watcher{
		store: store, sessions: sessions, runtime: runtime, local: local,
		tick: cfg.Tick, noticeLines: cfg.NoticeLines, now: cfg.Clock, newID: cfg.NewID, logger: cfg.Logger,
	}
	if w.tick <= 0 {
		w.tick = DefaultWatchInterval
	}
	if w.noticeLines <= 0 {
		w.noticeLines = DefaultNoticeLines
	}
	if w.now == nil {
		w.now = time.Now
	}
	if w.newID == nil {
		w.newID = uuid.NewString
	}
	if w.logger == nil {
		w.logger = slog.Default()
	}
	return w
}

// Start runs the watch loop until the context is cancelled.
func (w *Watcher) Start(ctx context.Context) <-chan struct{} {
	return observe.StartPollLoop(ctx, w.tick, w.Poll, w.logger, "auto-resume watcher")
}

// Poll performs one detection and one delivery pass.
//
// The policy is read once per pass, so turning the toggle off stops both halves
// at the next tick. Rows already scheduled are left pending rather than
// cancelled: the operator may be toggling it back on, and a row that goes stale
// meanwhile is cancelled on its own terms by deliverDue.
func (w *Watcher) Poll(ctx context.Context) error {
	settings, err := w.store.GetAutoResumeSettings(ctx)
	if err != nil {
		return fmt.Errorf("auto-resume: read settings: %w", err)
	}
	if !settings.Enabled {
		return nil
	}
	now := w.now()
	sessions, err := w.sessions.ListAllSessions(ctx)
	if err != nil {
		return fmt.Errorf("auto-resume: list sessions: %w", err)
	}
	for _, rec := range sessions {
		w.inspect(ctx, rec, now)
	}
	return w.deliverDue(ctx, settings.Prompt(), now)
}

// inspect decides whether one session has newly died on a usage limit.
func (w *Watcher) inspect(ctx context.Context, rec domain.SessionRecord, now time.Time) {
	if rec.IsTerminated {
		w.cancelPending(ctx, rec.ID, "session was terminated", now)
		return
	}
	if rec.Activity.State != domain.ActivityExited {
		// The agent is running again — a human resumed it, or an earlier
		// auto-resume landed. Either way the appointment is moot.
		w.cancelPending(ctx, rec.ID, "agent is running again", now)
		return
	}
	if rec.Metadata.RuntimeHandleID == "" {
		return
	}
	state, err := w.store.AutoResumeSessionState(ctx, rec.ID)
	if err != nil {
		w.logger.Error("auto-resume: session history unreadable", "session", rec.ID, "err", err)
		return
	}
	if state.HasPending {
		return
	}
	if state.Attempts >= domain.MaxAutoResumesPerSession {
		// Deliberately not an error and not a warning per tick: a session that
		// keeps hitting the limit is a budget decision for a human, and AO has
		// already said so once per attempt.
		w.logger.Debug("auto-resume: session is at its cap",
			"session", rec.ID, "attempts", state.Attempts, "cap", domain.MaxAutoResumesPerSession)
		return
	}
	if !state.LastDetectedAt.IsZero() && now.Sub(state.LastDetectedAt) < domain.AutoResumeRetryFloor {
		return
	}
	output, err := w.runtime.GetOutput(ctx, ports.RuntimeHandle{ID: rec.Metadata.RuntimeHandleID}, w.noticeLines)
	if err != nil {
		// An unreadable pane is not evidence about why the agent died, so it
		// says nothing either way and the next tick tries again.
		w.logger.Debug("auto-resume: terminal output unavailable", "session", rec.ID, "err", err)
		return
	}
	limit, notice, ok := FindUsageLimit(output, now)
	if !ok {
		return
	}
	row := domain.AutoResumeSchedule{
		ID: w.newID(), SessionID: rec.ID, LaunchID: rec.Metadata.RuntimeLaunchID,
		Attempt: state.Attempts + 1, State: domain.AutoResumePending,
		ResumeAt: limit.ResetAt.Add(domain.AutoResumeGrace), ExactReset: limit.Exact,
		Notice: notice, DetectedAt: now, UpdatedAt: now,
	}
	if err := w.store.ScheduleAutoResume(ctx, row); err != nil {
		w.logger.Error("auto-resume: could not schedule", "session", rec.ID, "err", err)
		return
	}
	w.logger.Info("auto-resume: session hit a provider usage limit; scheduled a resume",
		"session", rec.ID, "attempt", row.Attempt, "resumeAt", row.ResumeAt,
		"exactReset", row.ExactReset, "notice", notice)
}

func (w *Watcher) cancelPending(ctx context.Context, id domain.SessionID, reason string, now time.Time) {
	cancelled, err := w.store.CancelPendingAutoResume(ctx, id, reason, now)
	if err != nil {
		w.logger.Error("auto-resume: could not cancel a scheduled resume", "session", id, "err", err)
		return
	}
	if cancelled {
		w.logger.Info("auto-resume: cancelled a scheduled resume", "session", id, "reason", reason)
	}
}

// deliverDue sends every resume whose time has come.
func (w *Watcher) deliverDue(ctx context.Context, prompt string, now time.Time) error {
	due, err := w.store.ListDueAutoResumes(ctx, now, dueBatch)
	if err != nil {
		return fmt.Errorf("auto-resume: list due resumes: %w", err)
	}
	for _, row := range due {
		w.deliver(ctx, row, prompt, now)
	}
	return nil
}

func (w *Watcher) deliver(ctx context.Context, row domain.AutoResumeSchedule, prompt string, now time.Time) {
	if now.Sub(row.ResumeAt) > domain.AutoResumeStaleAfter {
		w.settle(ctx, row, domain.AutoResumeCancelled,
			fmt.Sprintf("resume came due %s ago, past the %s staleness bound",
				now.Sub(row.ResumeAt).Round(time.Minute), domain.AutoResumeStaleAfter), now)
		return
	}
	rec, found, err := w.store.GetSession(ctx, row.SessionID)
	if err != nil {
		w.logger.Error("auto-resume: session lookup failed", "session", row.SessionID, "err", err)
		return
	}
	if !found {
		w.settle(ctx, row, domain.AutoResumeCancelled, "session no longer exists", now)
		return
	}
	if rec.IsTerminated {
		w.settle(ctx, row, domain.AutoResumeCancelled, "session was terminated", now)
		return
	}
	detail, err := w.send(ctx, row, prompt, now)
	if err != nil {
		w.logger.Warn("auto-resume: resume could not be delivered",
			"session", row.SessionID, "attempt", row.Attempt, "err", err)
		w.settle(ctx, row, domain.AutoResumeFailed, err.Error(), now)
		return
	}
	w.settle(ctx, row, domain.AutoResumeResumed, detail, now)
	w.logger.Info("auto-resume: resumed a session after its provider usage limit reset",
		"session", row.SessionID, "attempt", row.Attempt, "delivery", detail)
}

// send delivers the prompt and returns a short description of how.
//
// The outbox is tried first and its refusal is the routing decision: the store
// resolves the session's execution binding inside the same transaction it would
// write, so asking it is the only way to route that cannot disagree with what
// the queue actually accepts. It refuses before writing, so a local session
// leaves no half-queued command behind.
func (w *Watcher) send(
	ctx context.Context, row domain.AutoResumeSchedule, prompt string, now time.Time,
) (string, error) {
	command, err := w.store.EnqueueExecutionSessionMessage(ctx, domain.ExecutionSessionMessage{
		CommandID: w.newID(), EventID: w.newID(), SessionID: row.SessionID,
		Message: prompt, SentBy: AutoResumeActor, SentAt: now.UTC(),
	})
	if err == nil {
		return "outbox command " + command.ID, nil
	}
	if !errors.Is(err, domain.ErrSessionNotRemote) {
		return "", fmt.Errorf("queue remote resume: %w", err)
	}
	if w.local == nil {
		return "", errors.New("local resumes are not wired on this daemon")
	}
	if err := w.local.Resume(ctx, row.SessionID, prompt); err != nil {
		return "", fmt.Errorf("resume local agent: %w", err)
	}
	return "local agent relaunch", nil
}

// AutoResumeActor is the sender recorded on a resume AO sent by itself. It
// reads back on the session timeline next to human senders, so nobody has to
// wonder who woke the agent up at 3am.
const AutoResumeActor = "ao-auto-resume"

func (w *Watcher) settle(
	ctx context.Context, row domain.AutoResumeSchedule, state domain.AutoResumeState, detail string, now time.Time,
) {
	if _, err := w.store.SettleAutoResume(ctx, row.ID, state, detail, now); err != nil {
		w.logger.Error("auto-resume: could not record the outcome",
			"session", row.SessionID, "schedule", row.ID, "state", state, "err", err)
	}
}
