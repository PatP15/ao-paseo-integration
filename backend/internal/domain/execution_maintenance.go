package domain

import "time"

// ExecutionHostSkill is one skill installed under ~/.claude/skills on a host,
// as the maintenance channel captured it.
type ExecutionHostSkill struct {
	HostID      ExecutionHostID
	Name        string
	Description string
	CapturedAt  time.Time
}

// ExecutionHostPrefs is the host's orchestration preferences file as the
// maintenance channel read it: content, its hex sha256, and whether the file
// existed (a missing file carries empty content and the empty-string hash).
type ExecutionHostPrefs struct {
	HostID      ExecutionHostID
	Content     string
	SHA256      string
	Exists      bool
	ConfirmedAt time.Time
}
