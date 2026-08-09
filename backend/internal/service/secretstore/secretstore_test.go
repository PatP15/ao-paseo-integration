package secretstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func apierrCode(t *testing.T, err error) string {
	t.Helper()
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *apierr.Error, got %T: %v", err, err)
	}
	return apiErr.Code
}

func TestSaveWritesResolvableFile(t *testing.T) {
	dataDir := t.TempDir()
	store := New(dataDir)

	ref, err := store.Save(SaveInput{Name: "worker-pw", Value: "  hunter2\n"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if ref != "worker-pw" {
		t.Fatalf("ref = %q, want worker-pw", ref)
	}

	path := filepath.Join(dataDir, "secrets", "worker-pw")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// The resolver trims on read; storing trimmed keeps the file exact.
	if string(raw) != "hunter2" {
		t.Fatalf("stored %q, want %q", raw, "hunter2")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %v, want 0600", perm)
	}
	dirInfo, err := os.Stat(filepath.Join(dataDir, "secrets"))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", perm)
	}
}

func TestSaveRefusesOverwriteWithoutReplace(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Save(SaveInput{Name: "pw", Value: "one"}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	_, err := store.Save(SaveInput{Name: "pw", Value: "two"})
	if code := apierrCode(t, err); code != "SECRET_EXISTS" {
		t.Fatalf("code = %q, want SECRET_EXISTS", code)
	}
	if _, err := store.Save(SaveInput{Name: "pw", Value: "two", Replace: true}); err != nil {
		t.Fatalf("Save with Replace: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, "pw"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(raw) != "two" {
		t.Fatalf("stored %q after replace, want %q", raw, "two")
	}
}

func TestSaveConcurrentCreateHasOneWinner(t *testing.T) {
	store := New(t.TempDir())
	const writers = 32
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			<-start
			_, err := store.Save(SaveInput{Name: "shared", Value: value})
			results <- err
		}(fmt.Sprintf("value-%d", i))
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case apierrCodeValue(err) == "SECRET_EXISTS":
			conflicted++
		default:
			t.Fatalf("concurrent Save returned unexpected error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != writers-1 {
		t.Fatalf("concurrent creates: succeeded=%d conflicted=%d, want 1/%d", succeeded, conflicted, writers-1)
	}
	if raw, err := os.ReadFile(filepath.Join(store.dir, "shared")); err != nil || !strings.HasPrefix(string(raw), "value-") {
		t.Fatalf("winning value = %q, err=%v", raw, err)
	}
}

func apierrCodeValue(err error) string {
	var apiErr *apierr.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

func TestSaveRejectsPathShapedAndEmptyNames(t *testing.T) {
	store := New(t.TempDir())
	for _, tc := range []struct{ name, wantCode string }{
		{"", "SECRET_NAME_REQUIRED"},
		{"   ", "SECRET_NAME_REQUIRED"},
		{"a/b", "SECRET_NAME_INVALID"},
		{`a\b`, "SECRET_NAME_INVALID"},
		{"..", "SECRET_NAME_INVALID"},
		{"a..b", "SECRET_NAME_INVALID"},
		{".hidden", "SECRET_NAME_INVALID"},
		{"has space", "SECRET_NAME_INVALID"},
		{strings.Repeat("n", 129), "SECRET_NAME_TOO_LONG"},
	} {
		_, err := store.Save(SaveInput{Name: tc.name, Value: "v"})
		if code := apierrCode(t, err); code != tc.wantCode {
			t.Fatalf("Save(%q) code = %q, want %q", tc.name, code, tc.wantCode)
		}
	}
	// Nothing may exist on disk after only refused writes.
	if _, err := os.Stat(store.dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(store.dir)
		if len(entries) > 0 {
			t.Fatalf("refused writes left files behind: %v", entries)
		}
	}
}

func TestSaveRejectsEmptyAndOversizedValues(t *testing.T) {
	store := New(t.TempDir())
	_, err := store.Save(SaveInput{Name: "pw", Value: "  \n"})
	if code := apierrCode(t, err); code != "SECRET_VALUE_REQUIRED" {
		t.Fatalf("code = %q, want SECRET_VALUE_REQUIRED", code)
	}
	_, err = store.Save(SaveInput{Name: "pw", Value: strings.Repeat("x", maxValueLen+1)})
	if code := apierrCode(t, err); code != "SECRET_VALUE_TOO_LONG" {
		t.Fatalf("code = %q, want SECRET_VALUE_TOO_LONG", code)
	}
}

func TestListReturnsNamesOnlySorted(t *testing.T) {
	store := New(t.TempDir())

	names, err := store.List()
	if err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("List on missing dir = %v, want empty", names)
	}

	for _, name := range []string{"zeta", "alpha", "mid"} {
		if _, err := store.Save(SaveInput{Name: name, Value: "v"}); err != nil {
			t.Fatalf("Save(%q): %v", name, err)
		}
	}
	// A stray dotfile (e.g. an interrupted stage) must not surface as a ref.
	if err := os.WriteFile(filepath.Join(store.dir, ".alpha.tmp-1"), []byte("x"), 0o600); err != nil {
		t.Fatalf("plant dotfile: %v", err)
	}

	names, err = store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"alpha", "mid", "zeta"}
	if len(names) != len(want) {
		t.Fatalf("List = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("List = %v, want %v", names, want)
		}
	}
}
