package paseo

import (
	"regexp"
	"strings"
)

var (
	passwordPattern = regexp.MustCompile(`(?i)(password=)[^&#\s"']*`)
	offerPattern    = regexp.MustCompile(`(?i)(#offer=)[A-Za-z0-9_-]+`)
)

func redact(value string, secrets ...string) string {
	value = passwordPattern.ReplaceAllString(value, "${1}REDACTED")
	value = offerPattern.ReplaceAllString(value, "${1}REDACTED")
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "REDACTED")
		}
	}
	return value
}

// Redact strips credentials from a string bound for a log or an error.
//
// Exported because the daemon logs failures from constructing a client, and the
// endpoint it was given may carry ?password= — SECURITY.md §9 forbids that
// reaching a log line. Callers outside this package cannot reach the unexported
// helper, and "the caller will remember" is not a control.
func Redact(value string, secrets ...string) string { return redact(value, secrets...) }
