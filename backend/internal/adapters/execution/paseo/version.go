package paseo

import (
	"fmt"
	"regexp"
	"strings"
)

// SupportedVersion is the fixture-verified Paseo CLI contract.
const SupportedVersion = "0.2.5"

var versionPattern = regexp.MustCompile(`(?:^|\s)(\d+\.\d+\.\d+)(?:\s|$)`)

func parseVersion(output string) (string, error) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(output))
	if len(match) != 2 {
		return "", fmt.Errorf("parse Paseo version: unexpected output %q", redact(output))
	}
	return match[1], nil
}

func checkVersion(version string) error {
	if version != SupportedVersion {
		return &Error{
			Kind:    ErrorUnsupportedVersion,
			Message: fmt.Sprintf("unsupported Paseo version %q (supported: %s)", redact(version), SupportedVersion),
		}
	}
	return nil
}
