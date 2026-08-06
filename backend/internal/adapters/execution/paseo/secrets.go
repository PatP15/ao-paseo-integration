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
