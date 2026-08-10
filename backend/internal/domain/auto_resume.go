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
