package domain

import (
	"strings"
	"time"
)

// ExecutionHostSkill is one skill installed under ~/.claude/skills on a host,
// as the maintenance channel captured it.
type ExecutionHostSkill struct {
	HostID      ExecutionHostID
	Name        string
	Description string
	CapturedAt  time.Time
}

// SkillPolicyGated reports whether a skill orchestrates THROUGH Paseo —
// spawning agents, scheduling, delegating — which decision D6 gates: AO owns
// scheduling and drives daemons running without MCP tool injection, so such a
// skill can only mislead an agent into asking for what the host refuses.
// Deliberately conservative and centralized so every surface (dispatch
// affordances, host detail badges) gates the same set.
func SkillPolicyGated(name, description string) bool {
	switch name {
	case "paseo-loop", "paseo-handoff", "paseo-committee", "paseo-advisor":
		return true
	}
	lowered := strings.ToLower(description)
	if !strings.Contains(lowered, "agent") {
		return false
	}
	// Markers are the orchestration verbs themselves — not softer words like
	// "delegate" or "hand off", which appear NEGATED in perfectly gate-free
	// skills ("…without delegating the work itself"). The named list above
	// already covers the canonical paseo-* orchestrators.
	//
	// "spin up" earns its place from a real skill: paseo-advisor announces
	// itself as "Spin up a single agent as an advisor", which spawns through
	// Paseo exactly as handoff does while matching none of the other markers.
	for _, marker := range []string{"spawn", "spin up", "spins up", "schedule", "orchestrat", "committee", "loop until"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
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
