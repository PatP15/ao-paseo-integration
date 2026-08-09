package paseoreporter

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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
