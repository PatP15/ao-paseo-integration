package domain

import (
	"strings"
	"unicode"
)

// tagBlockStart and tagBlockEnd bound Unicode's TAG block, which encodes plain
// ASCII in codepoints that render as nothing at all: U+E0041 is an invisible
// "A". The whole range is stripped by number rather than by category because
// the unassigned holes in it (U+E0000, U+E0002–U+E001F) are Cn, not Cf, and a
// future Unicode revision could assign them.
const (
	tagBlockStart = 0xE0000
	tagBlockEnd   = 0xE007F
)

// SanitizeControlChars removes characters that are unsafe to deliver into a
// live terminal pane or an agent's prompt, while preserving the whitespace that
// legitimate multi-line text relies on (newline, carriage return, tab).
//
// Any text that reaches an agent's PTY must pass through here. The session
// runtime pastes messages straight into the live pane, so an unfiltered escape
// sequence (cursor control, screen clear, OSC) embedded in attacker-influenced
// content — a GitHub reviewer comment, a CI job log tail — would be interpreted
// by the terminal instead of read as plain text. Both the HTTP send endpoint
// and the lifecycle nudge path share this one definition so neither can drift
// into delivering raw control bytes.
//
// Category Cf and the TAG block go with them, because unicode.IsControl covers
// only Cc. Everything in those two sets is invisible to the human reviewing a
// PR comment and fully legible to the model reading it, which is the exact
// asymmetry a prompt injection needs: U+202E reorders the rendered line, U+200B
// splits a keyword past a filter, and the TAG block smuggles entire ASCII
// sentences that leave no trace on screen.
//
// The cost is that ZWJ emoji sequences degrade to their component glyphs and
// explicit RTL marks are dropped from bidirectional text. That is deliberate:
// this function only ever runs on machine-fetched external content and on
// messages bound for a terminal pane, where "renders differently than it reads"
// is a hazard rather than a feature.
func SanitizeControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		if r >= tagBlockStart && r <= tagBlockEnd {
			return -1
		}
		return r
	}, s)
}
