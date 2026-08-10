package domain

import (
	"strings"
	"time"
)

// DefaultAutoResumePrompt is what AO sends a session it restarts after a
// provider usage limit, unless the operator stored their own text.
//
// It deliberately does not restate the task: AO does not know it, and inventing
// one would send the agent off in a direction nobody asked for. It points the
// agent back at the instructions and state it already has.
const DefaultAutoResumePrompt = "You were interrupted by a provider usage limit. " +
	"Re-read your last instructions and any state/checklist file you maintain, " +
	"then continue exactly where you left off."

// MaxAutoResumesPerSession caps how many times AO restarts one session on its
// own. A session that keeps hitting the limit is a budget problem a human needs
// to see, not one AO should keep spending on unattended.
const MaxAutoResumesPerSession = 5

// MaxAutoResumePromptLen bounds the stored prompt. It matches the message limit
// the send path enforces: a longer prompt could not be delivered anyway.
const MaxAutoResumePromptLen = 4096

// AutoResumeFallbackDelay is how long AO waits when a usage-limit message
// carries no parsable reset time. Guessing sooner risks burning a retry while
// the limit is still in force; guessing later strands the session.
const AutoResumeFallbackDelay = 30 * time.Minute

// AutoResumeGrace is added to a parsed reset time before AO sends the resume.
// Providers publish the reset to the minute, and a request that lands on the
// boundary is refused as if nothing had reset at all.
const AutoResumeGrace = 2 * time.Minute

// AutoResumeRetryFloor is the minimum gap between two auto-resumes of the same
// session. It exists because a resume that fails leaves the session exited with
// the same notice still on screen: without a floor the watcher would re-read it
// on the next tick and burn all five attempts inside a minute. With it, the cap
// covers roughly twenty minutes of retrying, which is long enough for a
// transient host or relaunch failure to clear.
const AutoResumeRetryFloor = 5 * time.Minute

// AutoResumeStaleAfter is how far past its due time a scheduled resume may be
// delivered. A resume that came due while AO was stopped — or while the toggle
// was off — is about a limit that lifted long ago, and sending it would wake an
// agent nobody is watching for reasons nobody remembers. Past this, the row is
// cancelled instead.
const AutoResumeStaleAfter = 6 * time.Hour

// AutoResumeState is where one scheduled resume ended up. Only AutoResumePending
// is ever acted on; the other three are immutable history.
type AutoResumeState string

// The lifecycle of one scheduled auto-resume.
const (
	// AutoResumePending is scheduled but not yet delivered.
	AutoResumePending AutoResumeState = "pending"
	// AutoResumeResumed means the prompt reached the session — typed into a
	// local agent, or queued on the execution outbox for a remote one.
	AutoResumeResumed AutoResumeState = "resumed"
	// AutoResumeFailed means delivery was attempted and refused. Detail says why.
	AutoResumeFailed AutoResumeState = "failed"
	// AutoResumeCancelled means the resume stopped applying before it was sent:
	// the session revived on its own, was terminated, or the row went stale.
	AutoResumeCancelled AutoResumeState = "cancelled"
)

// AutoResumeSchedule is one auto-resume AO decided to perform: the notice it
// read, when it expects the limit to lift, and what became of the attempt.
//
// It is both the schedule and the audit record. A local session has no
// execution_events row to write to (those require a host), so this row is the
// durable evidence that AO — not a human — restarted the agent.
type AutoResumeSchedule struct {
	ID        string
	SessionID SessionID
	// LaunchID pins the row to the agent launch that died, so a resume
	// scheduled for one launch is not read as covering the next one.
	LaunchID string
	// Attempt is 1-based and counts every act on this session, so it can be
	// compared straight against MaxAutoResumesPerSession.
	Attempt int
	State   AutoResumeState
	// ResumeAt already includes AutoResumeGrace.
	ResumeAt time.Time
	// ExactReset reports whether ResumeAt came from the notice or from the
	// blind AutoResumeFallbackDelay. Operators need to tell those apart when a
	// resume fires at an odd time.
	ExactReset bool
	// Notice is the line AO matched, kept verbatim so a bad parse can be
	// diagnosed from the record rather than reproduced.
	Notice     string
	Detail     string
	DetectedAt time.Time
	UpdatedAt  time.Time
}

// AutoResumeSessionState is everything the watcher needs to know about one
// session's auto-resume history before it schedules another.
type AutoResumeSessionState struct {
	// Attempts counts every row for the session, settled or not.
	Attempts int
	// Pending is the session's unsent resume, valid only when HasPending.
	Pending    AutoResumeSchedule
	HasPending bool
	// LastDetectedAt is when AO last acted on this session, zero if never.
	LastDetectedAt time.Time
}

// AutoResumeSettings is the app-wide policy for restarting a session whose
// agent was killed by a provider usage limit. There is exactly one of these;
// it is not per-project and not per-session.
type AutoResumeSettings struct {
	Enabled bool
	// ResumePrompt is the operator's own wording, empty when they have not
	// customised it. Read it through Prompt rather than directly: an empty
	// value means "use the current default", not "send nothing".
	ResumePrompt string
	UpdatedAt    time.Time
}

// Prompt returns the text to send on a resume, falling back to the shipped
// default when the operator stored nothing of their own.
func (s AutoResumeSettings) Prompt() string {
	if strings.TrimSpace(s.ResumePrompt) == "" {
		return DefaultAutoResumePrompt
	}
	return s.ResumePrompt
}
