package paseoreporter

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

const testNonce = "0123456789ab"

func parseRun(t *testing.T, out *bytes.Buffer) paseoevent.MaintenanceResult {
	t.Helper()
	result, err := paseoevent.ParseMaintenanceRun(testNonce, strings.Split(out.String(), "\n"))
	if err != nil {
		t.Fatalf("parse maintenance run: %v", err)
	}
	return result
}

func TestMaintainInventoryRoundTripsSkillFrontmatter(t *testing.T) {
	skillsDir := t.TempDir()
	for name, manifest := range map[string]string{
		"deploy":   "---\nname: deploy-service\ndescription: Deploy the service safely\n---\n\nBody.",
		"nameless": "no frontmatter here",
		"quoted":   "---\nname: \"quoted-name\"\ndescription: 'single quoted'\n---\n",
	} {
		if err := os.MkdirAll(filepath.Join(skillsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillsDir, name, "SKILL.md"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A directory without a manifest is not a skill.
	if err := os.MkdirAll(filepath.Join(skillsDir, "not-a-skill"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := MaintainInventory(skillsDir, testNonce, &out); err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &out)
	if result.Err != nil || result.Done == nil || result.Done.Count != 3 || len(result.Skills) != 3 {
		t.Fatalf("result = %+v", result)
	}
	byName := map[string]string{}
	for _, skill := range result.Skills {
		byName[skill.Name] = skill.Description
	}
	if byName["deploy-service"] != "Deploy the service safely" {
		t.Fatalf("frontmatter skill = %#v", byName)
	}
	if byName["quoted-name"] != "single quoted" {
		t.Fatalf("quoted frontmatter = %#v", byName)
	}
	if _, ok := byName["nameless"]; !ok {
		t.Fatalf("directory-name fallback missing: %#v", byName)
	}
}

func TestMaintainInventoryOnAMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	var out bytes.Buffer
	if err := MaintainInventory(filepath.Join(t.TempDir(), "absent"), testNonce, &out); err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &out)
	if result.Err != nil || result.Done == nil || result.Done.Count != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func sha256HexBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func TestMaintainPrefsWriteConfirmsByRereadAndRefusesDrift(t *testing.T) {
	prefsPath := filepath.Join(t.TempDir(), "orchestration-preferences.json")
	original := []byte(`{"providers":{"impl":"codex/gpt-5.4"}}`)
	if err := os.WriteFile(prefsPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := []byte(`{"providers":{"impl":"claude/opus"}}`)

	var out bytes.Buffer
	err := MaintainPrefsWrite(prefsPath, testNonce,
		base64.StdEncoding.EncodeToString(replacement), sha256HexBytes(replacement), sha256HexBytes(original), &out)
	if err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &out)
	if result.Err != nil || result.Done == nil || !result.Done.Exists {
		t.Fatalf("write result = %+v", result)
	}
	if result.Done.SHA256 != sha256HexBytes(replacement) {
		t.Fatalf("confirmed hash = %s", result.Done.SHA256)
	}
	onDisk, err := os.ReadFile(prefsPath)
	if err != nil || !bytes.Equal(onDisk, replacement) {
		t.Fatalf("file after write = %q err=%v", onDisk, err)
	}

	// Drift: the precondition no longer matches, so the write is refused and
	// the file is untouched.
	out.Reset()
	stale := sha256HexBytes(original)
	if err := MaintainPrefsWrite(prefsPath, testNonce,
		base64.StdEncoding.EncodeToString(original), sha256HexBytes(original), stale, &out); err != nil {
		t.Fatal(err)
	}
	result = parseRun(t, &out)
	if result.Err == nil || !strings.Contains(result.Err.Message, "drift") {
		t.Fatalf("drift result = %+v", result)
	}
	onDisk, _ = os.ReadFile(prefsPath)
	if !bytes.Equal(onDisk, replacement) {
		t.Fatalf("drifted write mutated the file: %q", onDisk)
	}
}

func TestMaintainPrefsWriteSerializesConcurrentPreconditions(t *testing.T) {
	prefsPath := filepath.Join(t.TempDir(), "orchestration-preferences.json")
	original := []byte(`{"version":0}`)
	if err := os.WriteFile(prefsPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	const writers = 32
	start := make(chan struct{})
	results := make(chan string, writers)
	var group sync.WaitGroup
	for index := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			replacement := []byte(fmt.Sprintf(`{"version":%d}`, index+1))
			var out bytes.Buffer
			<-start
			if err := MaintainPrefsWrite(prefsPath, testNonce,
				base64.StdEncoding.EncodeToString(replacement), sha256HexBytes(replacement),
				sha256HexBytes(original), &out); err != nil {
				results <- "function error: " + err.Error()
				return
			}
			results <- out.String()
		}()
	}
	close(start)
	group.Wait()
	close(results)

	succeeded := 0
	refused := 0
	for output := range results {
		if strings.HasPrefix(output, "function error: ") {
			t.Fatal(output)
		}
		var out bytes.Buffer
		out.WriteString(output)
		result := parseRun(t, &out)
		switch {
		case result.Err == nil && result.Done != nil:
			succeeded++
		case result.Err != nil && strings.Contains(result.Err.Message, "drift"):
			refused++
		default:
			t.Fatalf("unexpected concurrent result: %+v", result)
		}
	}
	if succeeded != 1 || refused != writers-1 {
		t.Fatalf("concurrent results: succeeded=%d refused=%d, want 1/%d", succeeded, refused, writers-1)
	}
}

func TestMaintainPrefsWriteRefusesCorruptedContent(t *testing.T) {
	prefsPath := filepath.Join(t.TempDir(), "prefs.json")
	content := []byte(`{"providers":{}}`)
	var out bytes.Buffer
	if err := MaintainPrefsWrite(prefsPath, testNonce,
		base64.StdEncoding.EncodeToString(content), "deadbeef", sha256HexBytes(nil), &out); err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &out)
	if result.Err == nil || !strings.Contains(result.Err.Message, "corrupted") {
		t.Fatalf("corruption result = %+v", result)
	}
	if _, err := os.Stat(prefsPath); !os.IsNotExist(err) {
		t.Fatal("refused write still created the file")
	}
}

func TestMaintainPrefsReadReportsAMissingFileAsAFact(t *testing.T) {
	var out bytes.Buffer
	if err := MaintainPrefsRead(filepath.Join(t.TempDir(), "absent.json"), testNonce, &out); err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &out)
	if result.Err != nil || result.Done == nil || result.Done.Exists || result.Done.Parts != 0 {
		t.Fatalf("missing-file result = %+v", result)
	}
	if result.Done.SHA256 != sha256HexBytes(nil) {
		t.Fatalf("missing file hash = %s, want the empty-string hash", result.Done.SHA256)
	}
}

func TestMaintainPrefsWriteCreatesMissingParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "orchestration-preferences.json")
	content := []byte(`{"providers":{}}`)
	var out bytes.Buffer
	if err := MaintainPrefsWrite(path, testNonce,
		base64.StdEncoding.EncodeToString(content), sha256HexBytes(content), sha256HexBytes(nil), &out); err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &out)
	if result.Err != nil || result.Done == nil || !result.Done.Exists {
		t.Fatalf("write result = %+v", result)
	}
}

func TestMaintainPrefsReadRefusesSymlinkWithoutDisclosingTarget(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	secret := []byte("must-not-cross-the-maintenance-allowlist")
	if err := os.WriteFile(outside, secret, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "orchestration-preferences.json")
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := MaintainPrefsRead(path, testNonce, &out); err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &out)
	if result.Err == nil || !strings.Contains(result.Err.Message, "not a regular file") {
		t.Fatalf("symlink result = %+v", result)
	}
	if bytes.Contains(out.Bytes(), secret) {
		t.Fatalf("maintenance output disclosed symlink target: %q", out.String())
	}
}
