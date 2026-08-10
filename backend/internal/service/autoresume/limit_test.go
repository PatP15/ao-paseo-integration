package autoresume

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The parser reads wall-clock times, so every case is anchored to a fixed
// "now" in a zone with a non-zero offset: a bug that silently works in UTC
// (reading the reset as UTC and calling it local) fails here.
var testZone = time.FixedZone("TEST-0400", -4*3600)

func at(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, testZone)
}

func TestParseUsageLimitReadsResetTime(t *testing.T) {
	now := at(2026, time.August, 10, 15, 30)

	tests := []struct {
		name string
		text string
		want time.Time
	}{
		{
			name: "clock later today",
			text: "You've hit your usage limit. Try again at 10:46 PM",
			want: at(2026, time.August, 10, 22, 46),
		},
		{
			name: "explicit date and clock",
			text: "You've hit your usage limit. Try again at Aug 10th, 2026 3:49 AM.",
			want: at(2026, time.August, 10, 3, 49),
		},
		{
			name: "bare hour rolls to tomorrow",
			text: "You've hit your session limit · resets 7pm",
			want: at(2026, time.August, 10, 19, 0),
		},
		{
			name: "morning reset means tomorrow",
			text: "You've hit your session limit · resets 7am",
			want: at(2026, time.August, 11, 7, 0),
		},
		{
			name: "five hour window",
			text: "5-hour limit reached ∙ resets 8pm",
			want: at(2026, time.August, 10, 20, 0),
		},
		{
			name: "twenty four hour clock",
			text: "Claude usage limit reached. Your limit will reset at 19:15.",
			want: at(2026, time.August, 10, 19, 15),
		},
		{
			name: "zone named in parentheses converts to local",
			text: "Claude usage limit reached. Your limit will reset at 10pm (UTC).",
			want: at(2026, time.August, 10, 18, 0),
		},
		{
			name: "bare utc suffix converts to local",
			text: "Usage limit exceeded; retry at 23:00 UTC",
			want: at(2026, time.August, 10, 19, 0),
		},
		{
			name: "iso stamp with offset",
			text: "Rate limit exceeded. Try again at 2026-08-11T02:30:00Z.",
			want: at(2026, time.August, 10, 22, 30),
		},
		{
			name: "iso stamp without offset is read in local time",
			text: "Quota exceeded. Retry at 2026-08-11 06:05.",
			want: at(2026, time.August, 11, 6, 5),
		},
		{
			name: "day before month",
			text: "You've hit your usage limit. Try again at 11 Aug 2026 3:49 AM.",
			want: at(2026, time.August, 11, 3, 49),
		},
		{
			name: "tomorrow with a clock",
			text: "Weekly limit reached. Try again tomorrow at 9am.",
			want: at(2026, time.August, 11, 9, 0),
		},
		{
			name: "relative minutes",
			text: "You've hit your usage limit. Try again in 12 minutes.",
			want: now.Add(12 * time.Minute),
		},
		{
			name: "relative seconds",
			text: "Rate limit exceeded; retry after 900 seconds",
			want: now.Add(15 * time.Minute),
		},
		{
			name: "compound relative duration",
			text: "You've hit your usage limit. Resets in 2h 30m.",
			want: now.Add(2*time.Hour + 30*time.Minute),
		},
		{
			name: "punctuation and casing do not matter",
			text: "YOU'VE HIT YOUR USAGE LIMIT — TRY AGAIN AT 10:46 P.M.",
			want: at(2026, time.August, 10, 22, 46),
		},
		{
			name: "ansi coloured terminal capture",
			text: "\x1b[31mYou've hit your usage limit. Try again at 10:46 PM\x1b[0m",
			want: at(2026, time.August, 10, 22, 46),
		},
		{
			name: "message wrapped across lines",
			text: "You've hit your usage limit.\n  Try again at\n  10:46 PM",
			want: at(2026, time.August, 10, 22, 46),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseUsageLimit(tc.text, now)
			if !ok {
				t.Fatalf("ParseUsageLimit(%q) did not recognise a usage limit", tc.text)
			}
			if !got.Exact {
				t.Fatalf("ParseUsageLimit(%q) fell back to %s instead of reading a reset time", tc.text, got.ResetAt)
			}
			if !got.ResetAt.Equal(tc.want) {
				t.Fatalf("ParseUsageLimit(%q) reset at %s, want %s", tc.text, got.ResetAt, tc.want)
			}
			if got.ResetAt.Location() != now.Location() {
				t.Fatalf("reset time is in %s, want the caller's zone %s", got.ResetAt.Location(), now.Location())
			}
		})
	}
}

// A reset time that has only just passed is taken at face value: the notice was
// captured moments ago, so "resets 3pm" at 15:00:20 means now, not tomorrow.
func TestParseUsageLimitTreatsJustPassedResetAsDue(t *testing.T) {
	now := time.Date(2026, time.August, 10, 15, 20, 0, 0, testZone)

	got, ok := ParseUsageLimit("You've hit your session limit · resets 3pm", now)
	if !ok || !got.Exact {
		t.Fatalf("ParseUsageLimit did not read a reset time: %+v", got)
	}
	if want := at(2026, time.August, 10, 15, 0); !got.ResetAt.Equal(want) {
		t.Fatalf("reset at %s, want %s (today, already due)", got.ResetAt, want)
	}
}

func TestParseUsageLimitFallsBackWhenTimeIsUnreadable(t *testing.T) {
	now := at(2026, time.August, 10, 15, 30)

	tests := []string{
		"You've hit your usage limit.",
		"Claude usage limit reached. Your limit will reset later.",
		"You've hit your session limit · resets soon",
		"Rate limit exceeded. Please try again later.",
		// A bare hour is not a time: "reset 7" could as easily be a count.
		"You've hit your usage limit. Try again at 7",
		// An abbreviation Go cannot resolve to an offset is dropped along with
		// the reset time rather than guessed at.
		"You've hit your usage limit. Try again at 25:99",
	}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			got, ok := ParseUsageLimit(text, now)
			if !ok {
				t.Fatalf("ParseUsageLimit(%q) did not recognise a usage limit", text)
			}
			if got.Exact {
				t.Fatalf("ParseUsageLimit(%q) claimed to read a reset time: %s", text, got.ResetAt)
			}
			if want := now.Add(domain.AutoResumeFallbackDelay); !got.ResetAt.Equal(want) {
				t.Fatalf("fallback reset at %s, want %s", got.ResetAt, want)
			}
		})
	}
}

func TestParseUsageLimitIgnoresUnrelatedText(t *testing.T) {
	now := at(2026, time.August, 10, 15, 30)

	tests := []string{
		"",
		"Build succeeded in 12 minutes.",
		"The tests pass. Try again at 10:46 PM if you want to rerun them.",
		"I added a rate limiter to the upload handler at 10:46 PM.",
		"Refactored limitPhraseRE to be phrase-based.",
	}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			if got, ok := ParseUsageLimit(text, now); ok {
				t.Fatalf("ParseUsageLimit(%q) read a usage limit: %+v", text, got)
			}
		})
	}
}

// A named IANA zone is honoured when the host ships tzdata. London is used
// rather than a US zone because its August offset differs from the test zone's,
// so dropping the zone would change the answer.
func TestParseUsageLimitConvertsNamedZone(t *testing.T) {
	if _, err := time.LoadLocation("Europe/London"); err != nil {
		t.Skipf("host has no tzdata: %v", err)
	}
	now := at(2026, time.August, 10, 15, 30)

	got, ok := ParseUsageLimit("Claude usage limit reached. Your limit will reset at 9:00 PM (Europe/London).", now)
	if !ok || !got.Exact {
		t.Fatalf("ParseUsageLimit did not read a reset time: %+v", got)
	}
	if want := at(2026, time.August, 10, 16, 0); !got.ResetAt.Equal(want) {
		t.Fatalf("reset at %s, want %s (21:00 BST is 16:00 at UTC-4)", got.ResetAt, want)
	}
}

// An unresolvable abbreviation must not silently shift the reset by hours: the
// clock is read in the machine's own zone instead.
func TestParseUsageLimitIgnoresAmbiguousZoneAbbreviations(t *testing.T) {
	now := at(2026, time.August, 10, 15, 30)

	got, ok := ParseUsageLimit("You've hit your usage limit. Try again at 10:46 PM (PST)", now)
	if !ok || !got.Exact {
		t.Fatalf("ParseUsageLimit did not read a reset time: %+v", got)
	}
	if want := at(2026, time.August, 10, 22, 46); !got.ResetAt.Equal(want) {
		t.Fatalf("reset at %s, want %s (read in the caller's zone)", got.ResetAt, want)
	}
}

// The parser must work off the clock it is handed, not the process zone, so the
// same notice resolves differently for two machines.
func TestParseUsageLimitUsesTheCallersZone(t *testing.T) {
	east := time.FixedZone("EAST+0900", 9*3600)
	now := time.Date(2026, time.August, 10, 15, 30, 0, 0, east)

	got, ok := ParseUsageLimit("You've hit your usage limit. Try again at 10:46 PM", now)
	if !ok || !got.Exact {
		t.Fatalf("ParseUsageLimit did not read a reset time: %+v", got)
	}
	want := time.Date(2026, time.August, 10, 22, 46, 0, 0, east)
	if !got.ResetAt.Equal(want) {
		t.Fatalf("reset at %s, want %s", got.ResetAt, want)
	}
}
