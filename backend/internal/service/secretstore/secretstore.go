// Package secretstore writes the credentials that execution-host secret refs
// name. It is the missing write half of the daemon's secretResolver: the
// register API deliberately refuses an inline credential and demands a
// reference, and until now only a shell could create the file a reference
// resolves to.
//
// The store never returns, logs, or persists a value anywhere except the
// 0600 file itself, so a credential cannot land in a task row, a log line,
// or telemetry. Files under a 0700 directory are the v1 store — the same
// posture, and the same directory, as the resolver in
// daemon/execution_wiring.go.
package secretstore

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// maxValueLen bounds a stored credential. Paseo passwords and offer URLs are
// short; anything beyond this is a caller mistake (a pasted file, a key blob)
// worth refusing rather than silently storing.
const maxValueLen = 4 * 1024

// Store writes and lists named credentials under <dataDir>/secrets.
type Store struct {
	dir string
}

// New returns a store rooted at <dataDir>/secrets, matching the resolver.
func New(dataDir string) *Store {
	return &Store{dir: filepath.Join(dataDir, "secrets")}
}

// SaveInput names one credential write.
type SaveInput struct {
	Name    string
	Value   string
	Replace bool
}

// Save stores the value and returns the ref that names it. An existing ref is
// refused unless Replace is set, so a typo cannot silently rotate a working
// credential.
func (s *Store) Save(in SaveInput) (string, error) {
	name := strings.TrimSpace(in.Name)
	if err := validateName(name); err != nil {
		return "", err
	}
	value := strings.TrimSpace(in.Value)
	if value == "" {
		return "", apierr.Invalid("SECRET_VALUE_REQUIRED", "value is required", nil)
	}
	if len(value) > maxValueLen {
		return "", apierr.Invalid("SECRET_VALUE_TOO_LONG",
			fmt.Sprintf("value exceeds %d bytes; a secret ref names a short credential, not a document", maxValueLen), nil)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", fmt.Errorf("create secrets directory: %w", err)
	}
	path := filepath.Join(s.dir, name)
	if !in.Replace {
		if _, err := os.Lstat(path); err == nil {
			return "", apierr.Conflict("SECRET_EXISTS",
				"secret ref "+name+" already exists; pass replace to rotate it", nil)
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("check secret ref %q: %w", name, err)
		}
	}
	// Write-then-rename so a crash mid-write can never leave a truncated
	// credential behind a ref that used to resolve.
	tmp, err := os.CreateTemp(s.dir, "."+name+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("stage secret ref %q: %w", name, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("restrict secret ref %q: %w", name, err)
	}
	if _, err := tmp.WriteString(value); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write secret ref %q: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close secret ref %q: %w", name, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("commit secret ref %q: %w", name, err)
	}
	return name, nil
}

// List returns the stored ref names, sorted. Never the values.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list secret refs: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// validateName enforces the resolver's contract: a ref is a bare name, and
// anything path-shaped is refused rather than sanitised. Rejecting a leading
// dot also keeps refs out of the store's own staging files.
func validateName(name string) error {
	if name == "" {
		return apierr.Invalid("SECRET_NAME_REQUIRED", "name is required", nil)
	}
	if len(name) > 128 {
		return apierr.Invalid("SECRET_NAME_TOO_LONG", "name must be at most 128 characters", nil)
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") ||
		strings.HasPrefix(name, ".") || strings.ContainsAny(name, " \t\r\n") {
		return apierr.Invalid("SECRET_NAME_INVALID",
			"name must be a bare file name: no path separators, whitespace, or leading dot", nil)
	}
	return nil
}
