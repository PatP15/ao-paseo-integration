package autoresume

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// UsageLimit is what AO read out of a provider's "you are out of quota" notice.
type UsageLimit struct {
	// ResetAt is when the limit is expected to lift, expressed in the zone of
	// the clock the caller passed in — the machine's local zone in production.
	ResetAt time.Time
	// Exact reports whether ResetAt came from the text. When it is false the
	// notice was recognised but carried no reset time AO could read, and
	// ResetAt is the blind fallback instead.
	Exact bool
}

// usageLimitStaleWindow is how far into the past a clock-only reset time
// ("resets 7pm", with no date) may point before AO reads it as tomorrow.
//
// Both mistakes here are asymmetric. Resuming too early costs one of the five
// attempts and self-corrects — the agent hits the limit again and AO reschedules
// from the fresh notice. Waiting a spurious extra day strands the session with
// nobody watching. So a reset time that has just passed is taken at face value.
const usageLimitStaleWindow = time.Hour

// ParseUsageLimit reports whether text is a provider usage-limit notice and,
// when it is, when AO should try the session again.
//
// The second return distinguishes "not a usage limit" from "a usage limit AO
// could not time": only the former means the caller should leave the session
// alone. An unreadable reset time still yields a usable ResetAt
// (now + domain.AutoResumeFallbackDelay), flagged with Exact false.
func ParseUsageLimit(text string, now time.Time) (UsageLimit, bool) {
	clean := normalizeNotice(text)
	if !limitPhraseRE.MatchString(clean) {
		return UsageLimit{}, false
	}
	if at, ok := parseResetTime(clean, now); ok {
		return UsageLimit{ResetAt: at.In(now.Location()), Exact: true}, true
	}
	return UsageLimit{ResetAt: now.Add(domain.AutoResumeFallbackDelay)}, true
}

// ansiRE matches the escape sequences a TUI agent leaves in captured output.
// The notice usually arrives as a scraped terminal line, coloured red.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// normalizeNotice flattens a captured line into something the patterns below
// can match: no escape sequences, no exotic spaces, one space between words.
func normalizeNotice(text string) string {
	s := ansiRE.ReplaceAllString(text, "")
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', ' ', ' ', ' ': // nbsp and friends
			return ' '
		case '‘', '’': // curly apostrophes in "You've"
			return '\''
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// limitPhraseRE decides whether a message is about a quota at all. It is
// deliberately phrase-based rather than a bare "limit": an agent discussing a
// rate limiter in code should not get its session restarted.
var limitPhraseRE = regexp.MustCompile(`(?i)` +
	`\b(?:usage|session|rate|quota|weekly|daily|hourly|token|message)\s+limits?\b` +
	`|\b\d+\s*-?\s*hours?\s+limit\b` +
	`|\blimits?\s+(?:reached|exceeded|hit)\b` +
	`|\bhit\s+your\s+limit\b` +
	`|\brate[_-]?limited\b` +
	`|\bquota\s+exceeded\b` +
	`|\bout\s+of\s+(?:usage|credits?|quota)\b`)

func parseResetTime(s string, now time.Time) (time.Time, bool) {
	if at, ok := parseISOStamp(s, now); ok {
		return at, true
	}
	loc := zoneFrom(s, now.Location())
	if hour, minute, sec, ok := findClock(s); ok {
		if year, month, day, ok := findDate(s, now.In(loc), hour, minute); ok {
			return time.Date(year, month, day, hour, minute, sec, 0, loc), true
		}
		return nextOccurrence(hour, minute, sec, now, loc), true
	}
	if d, ok := findDuration(s); ok {
		return now.Add(d), true
	}
	return time.Time{}, false
}

// isoStampRE matches a machine-readable stamp: "2026-08-10T15:49:00Z",
// "2026-08-10 15:49". Its own offset wins over any zone named elsewhere.
var isoStampRE = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2}))?\s*(Z|[+-]\d{2}:?\d{2})?`)

func parseISOStamp(s string, now time.Time) (time.Time, bool) {
	m := isoStampRE.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	loc := now.Location()
	if m[7] != "" {
		if z, ok := offsetZone(m[7]); ok {
			loc = z
		}
	} else {
		loc = zoneFrom(s, loc)
	}
	return time.Date(atoi(m[1]), time.Month(atoi(m[2])), atoi(m[3]),
		atoi(m[4]), atoi(m[5]), atoi(m[6]), 0, loc), true
}

// clock12RE matches "10:46 PM", "3:49AM", "7pm", "10 p.m.".
var clock12RE = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?(?::(\d{2}))?\s*([ap])\.?m\.?\b`)

// clock24RE matches "15:49" and "15:49:30". A bare hour is not accepted here:
// "reset 7" is as likely to be a count as a time.
var clock24RE = regexp.MustCompile(`\b([01]?\d|2[0-3]):([0-5]\d)(?::([0-5]\d))?\b`)

func findClock(s string) (hour, minute, sec int, ok bool) {
	if m := clock12RE.FindStringSubmatch(s); m != nil {
		hour = atoi(m[1])
		if hour < 1 || hour > 12 {
			return 0, 0, 0, false
		}
		hour %= 12
		if strings.EqualFold(m[4], "p") {
			hour += 12
		}
		return hour, atoi(m[2]), atoi(m[3]), true
	}
	if m := clock24RE.FindStringSubmatch(s); m != nil {
		return atoi(m[1]), atoi(m[2]), atoi(m[3]), true
	}
	return 0, 0, 0, false
}

// monthFirstRE matches "Aug 10th, 2026" and "August 10 2026"; dayFirstRE the
// "10 Aug 2026" ordering. Purely numeric dates ("8/10") are deliberately not
// read: month-first and day-first are both plausible and guessing wrong moves
// the resume by months.
var (
	monthFirstRE = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2})(?:st|nd|rd|th)?\b(?:\s*,?\s*(\d{4})\b)?`)
	dayFirstRE   = regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)?\s+(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\b(?:\s*,?\s*(\d{4})\b)?`)
	isoDateRE    = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	tomorrowRE   = regexp.MustCompile(`(?i)\btomorrow\b`)
	todayRE      = regexp.MustCompile(`(?i)\btoday\b|\btonight\b`)
)

var monthByPrefix = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March,
	"apr": time.April, "may": time.May, "jun": time.June,
	"jul": time.July, "aug": time.August, "sep": time.September,
	"oct": time.October, "nov": time.November, "dec": time.December,
}

// findDate reads the calendar day the reset falls on, if the notice names one.
// nowInZone is already in the reset's own zone, so "tomorrow" and an omitted
// year are resolved against the provider's day, not the machine's.
func findDate(s string, nowInZone time.Time, hour, minute int) (year int, month time.Month, day int, ok bool) {
	if m := isoDateRE.FindStringSubmatch(s); m != nil {
		return atoi(m[1]), time.Month(atoi(m[2])), atoi(m[3]), true
	}
	if m := monthFirstRE.FindStringSubmatch(s); m != nil {
		return dateFromParts(m[1], m[2], m[3], nowInZone, hour, minute)
	}
	if m := dayFirstRE.FindStringSubmatch(s); m != nil {
		return dateFromParts(m[2], m[1], m[3], nowInZone, hour, minute)
	}
	if tomorrowRE.MatchString(s) {
		t := nowInZone.AddDate(0, 0, 1)
		return t.Year(), t.Month(), t.Day(), true
	}
	if todayRE.MatchString(s) {
		return nowInZone.Year(), nowInZone.Month(), nowInZone.Day(), true
	}
	return 0, 0, 0, false
}

func dateFromParts(monthText, dayText, yearText string, nowInZone time.Time, hour, minute int) (int, time.Month, int, bool) {
	month, known := monthByPrefix[strings.ToLower(monthText[:3])]
	if !known {
		return 0, 0, 0, false
	}
	day := atoi(dayText)
	if day < 1 || day > 31 {
		return 0, 0, 0, false
	}
	if yearText != "" {
		return atoi(yearText), month, day, true
	}
	// No year given. A limit resets within days, so pick the year that puts the
	// reset nearest to now — which is what makes "Jan 2nd" read correctly when
	// the notice arrives on New Year's Eve.
	best, bestGap := nowInZone.Year(), time.Duration(1<<62)
	for _, year := range []int{nowInZone.Year() - 1, nowInZone.Year(), nowInZone.Year() + 1} {
		gap := time.Date(year, month, day, hour, minute, 0, 0, nowInZone.Location()).Sub(nowInZone)
		if gap < 0 {
			gap = -gap
		}
		if gap < bestGap {
			best, bestGap = year, gap
		}
	}
	return best, month, day, true
}

// nextOccurrence places a clock-only reset time on a calendar day: today if it
// is still ahead (or only just behind — see usageLimitStaleWindow), tomorrow
// otherwise.
func nextOccurrence(hour, minute, sec int, now time.Time, loc *time.Location) time.Time {
	nowInZone := now.In(loc)
	at := time.Date(nowInZone.Year(), nowInZone.Month(), nowInZone.Day(), hour, minute, sec, 0, loc)
	if at.Before(nowInZone.Add(-usageLimitStaleWindow)) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// parenZoneRE picks the zone out of "resets at 10am (UTC)" or "(Europe/London)".
var parenZoneRE = regexp.MustCompile(`\(([^)]{1,40})\)`)

// bareZoneRE catches an unbracketed "15:00 UTC" / "GMT+2".
var bareZoneRE = regexp.MustCompile(`(?i)\b(?:UTC|GMT)([+-]\d{1,2}(?::?\d{2})?)?\b`)

// zoneFrom resolves the zone the notice states its reset time in. Bare
// abbreviations (PST, CEST) are ignored on purpose: Go cannot resolve them to
// an offset without guessing, and a wrong guess moves the resume by hours.
func zoneFrom(s string, fallback *time.Location) *time.Location {
	for _, m := range parenZoneRE.FindAllStringSubmatch(s, -1) {
		if loc, ok := namedZone(strings.TrimSpace(m[1])); ok {
			return loc
		}
	}
	if m := bareZoneRE.FindStringSubmatch(s); m != nil {
		if m[1] != "" {
			if loc, ok := offsetZone(m[1]); ok {
				return loc
			}
		}
		return time.UTC
	}
	return fallback
}

func namedZone(text string) (*time.Location, bool) {
	upper := strings.ToUpper(text)
	if upper == "UTC" || upper == "GMT" || upper == "Z" {
		return time.UTC, true
	}
	if rest, found := strings.CutPrefix(upper, "UTC"); found {
		return offsetZone(rest)
	}
	if rest, found := strings.CutPrefix(upper, "GMT"); found {
		return offsetZone(rest)
	}
	if strings.Contains(text, "/") {
		// LoadLocation needs the tzdata the host ships; if it is missing, the
		// caller's zone is a better answer than a made-up one.
		if loc, err := time.LoadLocation(text); err == nil {
			return loc, true
		}
	}
	return nil, false
}

var offsetRE = regexp.MustCompile(`^([+-])(\d{1,2}):?(\d{2})?$`)

func offsetZone(text string) (*time.Location, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "Z" || trimmed == "z" {
		return time.UTC, true
	}
	m := offsetRE.FindStringSubmatch(trimmed)
	if m == nil {
		return nil, false
	}
	seconds := atoi(m[2])*3600 + atoi(m[3])*60
	if m[1] == "-" {
		seconds = -seconds
	}
	return time.FixedZone(trimmed, seconds), true
}

// durationRE matches the relative form — "try again in 12 minutes", "retry
// after 900 seconds", "resets in 2h 30m". The leading cue word is required so a
// stray "5 hour" inside "5-hour limit" is not read as a wait.
var durationRE = regexp.MustCompile(`(?i)\b(?:in|after|within)\s+((?:\d+\s*(?:hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b[\s,]*(?:and\s+)?)+)`)

var durationPartRE = regexp.MustCompile(`(?i)(\d+)\s*(hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b`)

func findDuration(s string) (time.Duration, bool) {
	m := durationRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	var total time.Duration
	for _, part := range durationPartRE.FindAllStringSubmatch(m[1], -1) {
		unit := strings.ToLower(part[2])
		switch {
		case strings.HasPrefix(unit, "h"):
			total += time.Duration(atoi(part[1])) * time.Hour
		case strings.HasPrefix(unit, "m"):
			total += time.Duration(atoi(part[1])) * time.Minute
		default:
			total += time.Duration(atoi(part[1])) * time.Second
		}
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

// atoi reads a regexp capture that the pattern already constrained to digits;
// an absent optional group reads as zero, which is what every caller wants.
func atoi(text string) int {
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return n
}
