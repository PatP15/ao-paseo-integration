package domain

import "testing"

// TestSkillPolicyGatedCoversRealSkillManifests pins the gate against the
// descriptions skills actually ship, not invented ones. Every case below is a
// verbatim frontmatter description read from an installed ~/.claude/skills
// manifest, because the gate's whole job is to classify what a live host
// inventory reports.
func TestSkillPolicyGatedCoversRealSkillManifests(t *testing.T) {
	for _, test := range []struct {
		name        string
		description string
		want        bool
	}{
		{
			// The miss this test was written for: an agent-spawning skill that
			// matched no marker, so dispatch offered it as ordinary.
			name: "paseo-advisor",
			description: "Spin up a single agent as an advisor — second opinion on the current task. " +
				"Use when the user says \"advisor\", \"second opinion\", \"what does X think\", or wants " +
				"an outside take without delegating the work itself.",
			want: true,
		},
		{
			name:        "paseo",
			description: "Paseo reference for managing workspaces, workspace scripts, agents, schedules, and heartbeats.",
			want:        true,
		},
		{
			name: "paseo-loop",
			description: "Run an agent loop until an exit condition is met. Use when the user says " +
				"\"loop\", \"babysit\", \"keep trying until\", \"check every X\", \"watch\", or wants " +
				"iterative autonomous execution.",
			want: true,
		},
		{
			name: "paseo-committee",
			description: "Form a committee of two high-reasoning agents to step back, do root cause " +
				"analysis, and produce a plan.",
			want: true,
		},
		{
			name: "paseo-handoff",
			description: "Hand off the current task to another agent with full context. Use when the " +
				"user says \"handoff\", \"hand off\", \"hand this to\", or wants to pass work to another agent.",
			want: true,
		},
		{
			// Edits a file and spawns nothing: gating it would train operators
			// to ignore the badge.
			name: "paseo-prefs",
			description: "Review and update ~/.paseo/orchestration-preferences.json — the global " +
				"provider/model map every Paseo skill reads.",
			want: false,
		},
		{
			name:        "demo-skill",
			description: "Small isolated skill fixture for AO remote inventory and synchronization E2E coverage.",
			want:        false,
		},
		{
			// "delegate" appears negated in gate-free skills, so it must stay
			// out of the marker list.
			name:        "code-review",
			description: "Review the current diff for correctness bugs without delegating to another agent.",
			want:        false,
		},
		{
			// A differently-named skill with the same spawning behaviour must
			// be caught by the marker, not only by the name list.
			name:        "second-opinion",
			description: "Spins up an agent to give an independent read on the current task.",
			want:        true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := SkillPolicyGated(test.name, test.description); got != test.want {
				t.Fatalf("SkillPolicyGated(%q, …) = %v, want %v", test.name, got, test.want)
			}
		})
	}
}
