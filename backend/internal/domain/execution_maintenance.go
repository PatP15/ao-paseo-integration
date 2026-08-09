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

// ExecutionHostPrefs is one AO-managed file on the host as the maintenance
// channel read it — the orchestration preferences or the machine-scope
// CLAUDE.md: content, its hex sha256, and whether the file existed (a missing
// file carries empty content and the empty-string hash).
type ExecutionHostPrefs struct {
	HostID      ExecutionHostID
	Content     string
	SHA256      string
	Exists      bool
	ConfirmedAt time.Time
}

// ExecutionRepoFile is one instruction file's content hash at a checkout's
// base branch, as the maintenance channel reported it.
type ExecutionRepoFile struct {
	Path   string
	SHA256 string
}

// ExecutionRepoStatus is one host checkout's instruction-file state.
type ExecutionRepoStatus struct {
	Head  string
	Files []ExecutionRepoFile
}

// ExecutionSkillFile is one complete, hash-verified file of a skill directory
// in transit between machines.
type ExecutionSkillFile struct {
	Path    string
	Content []byte
}
