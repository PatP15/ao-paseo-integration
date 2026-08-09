package paseoreporter

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestMaintainRepoStatusHashesInstructionFilesAtTheBase(t *testing.T) {
	origin := t.TempDir()
	gitRun(t, origin, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(origin, ".claude", "skills", "deploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"CLAUDE.md":                      "project instructions\n",
		"AGENTS.md":                      "agents\n",
		".claude/skills/deploy/SKILL.md": "---\nname: deploy\n---\n",
		"README.md":                      "not an instruction file\n",
	} {
		if err := os.WriteFile(filepath.Join(origin, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "-q", "-m", "base")

	checkout := filepath.Join(t.TempDir(), "clone")
	gitRun(t, filepath.Dir(checkout), "clone", "-q", origin, checkout)

	var out bytes.Buffer
	if err := MaintainRepoStatus(checkout, "main", testNonce, &out); err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &out)
	if result.Err != nil || result.Done == nil || result.Done.Count != 3 || result.Done.Head == "" {
		t.Fatalf("result = %+v", result)
	}
	paths := map[string]bool{}
	for _, file := range result.RepoFiles {
		if file.SHA256 == "" {
			t.Fatalf("file %s has no hash", file.Path)
		}
		paths[file.Path] = true
	}
	if !paths["CLAUDE.md"] || !paths["AGENTS.md"] || !paths[".claude/skills/deploy/SKILL.md"] || paths["README.md"] {
		t.Fatalf("hashed paths = %v", paths)
	}
}

func TestMaintainRepoFFRefusesDivergenceWithGitsOwnWords(t *testing.T) {
	origin := t.TempDir()
	gitRun(t, origin, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "CLAUDE.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "-q", "-m", "v1")
	checkout := filepath.Join(t.TempDir(), "clone")
	gitRun(t, filepath.Dir(checkout), "clone", "-q", origin, checkout)

	// Fast-forwardable: origin advances, checkout pulls clean.
	if err := os.WriteFile(filepath.Join(origin, "CLAUDE.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "-q", "-m", "v2")
	originHead := gitRun(t, origin, "rev-parse", "HEAD")

	var out bytes.Buffer
	if err := MaintainRepoFF(checkout, testNonce, &out); err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &out)
	if result.Err != nil || result.Done == nil || result.Done.Head != originHead {
		t.Fatalf("ff result = %+v, want head %s", result, originHead)
	}

	// Divergence: local commit + origin commit → the pull is refused whole and
	// git's own message travels back.
	if err := os.WriteFile(filepath.Join(checkout, "CLAUDE.md"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, checkout, "add", ".")
	gitRun(t, checkout, "commit", "-q", "-m", "local")
	if err := os.WriteFile(filepath.Join(origin, "CLAUDE.md"), []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "-q", "-m", "v3")

	out.Reset()
	if err := MaintainRepoFF(checkout, testNonce, &out); err != nil {
		t.Fatal(err)
	}
	result = parseRun(t, &out)
	if result.Err == nil || !strings.Contains(result.Err.Message, "git pull --ff-only") {
		t.Fatalf("divergence result = %+v", result)
	}
	if head := gitRun(t, checkout, "log", "-1", "--format=%s"); head != "local" {
		t.Fatalf("refused ff moved the checkout: HEAD is %q", head)
	}
}

func TestSkillPushRoundTripsThroughSkillRead(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceDir, "advisor", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: advisor\ndescription: Second opinion\n---\nBody.\n"
	extra := strings.Repeat("data", 700) // multi-chunk file
	if err := os.WriteFile(filepath.Join(sourceDir, "advisor", "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "advisor", "assets", "notes.txt"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}

	// Read the skill through the outbound verb, as AO would from a source host.
	var read bytes.Buffer
	if err := MaintainSkillRead(sourceDir, "advisor", testNonce, &read); err != nil {
		t.Fatal(err)
	}
	result := parseRun(t, &read)
	if result.Err != nil || result.Done == nil || result.Done.Count != 2 {
		t.Fatalf("skill read = %+v", result)
	}
	files, err := paseoevent.AssembleFileChunks(result.FileChunks)
	if err != nil {
		t.Fatal(err)
	}

	// Push it into a target skills dir through the inbound stream.
	var stream []string
	seq := 1
	for _, file := range files {
		lines, next, err := paseoevent.EncodeFileChunkEvents(testNonce, paseoevent.MaintenancePushFile, file.Path, file.Content, seq)
		if err != nil {
			t.Fatal(err)
		}
		stream = append(stream, lines...)
		seq = next
	}
	endFrames, err := paseoevent.EncodeMaintenanceEvent(testNonce, seq, paseoevent.MaintenancePushEnd,
		paseoevent.MaintenancePushEndPayload{Files: len(files)})
	if err != nil {
		t.Fatal(err)
	}
	stream = append(stream, endFrames...)

	targetDir := t.TempDir()
	// Pre-existing version that must survive a FAILED push and be replaced by
	// a good one.
	if err := os.MkdirAll(filepath.Join(targetDir, "advisor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "advisor", "SKILL.md"), []byte("old version"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Interrupted stream: no end marker → refused, old version intact.
	var out bytes.Buffer
	if err := MaintainSkillPush(targetDir, "advisor", testNonce,
		strings.NewReader(strings.Join(stream[:len(stream)-1], "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if result := parseRun(t, &out); result.Err == nil || !strings.Contains(result.Err.Message, "end marker") {
		t.Fatalf("interrupted push = %+v", result)
	}
	if content, _ := os.ReadFile(filepath.Join(targetDir, "advisor", "SKILL.md")); string(content) != "old version" {
		t.Fatalf("interrupted push mutated the skill: %q", content)
	}

	// Complete stream: byte-identical arrival, old version replaced whole.
	out.Reset()
	if err := MaintainSkillPush(targetDir, "advisor", testNonce,
		strings.NewReader(strings.Join(stream, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if result := parseRun(t, &out); result.Err != nil || result.Done == nil || result.Done.Count != 2 {
		t.Fatalf("push = %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(targetDir, "advisor", "SKILL.md"))
	if err != nil || string(got) != manifest {
		t.Fatalf("pushed SKILL.md = %q err=%v", got, err)
	}
	nested, err := os.ReadFile(filepath.Join(targetDir, "advisor", "assets", "notes.txt"))
	if err != nil || string(nested) != extra {
		t.Fatalf("pushed nested file mismatch: %d bytes err=%v", len(nested), err)
	}
	if entries, _ := os.ReadDir(targetDir); len(entries) != 1 {
		t.Fatalf("stage or backup directories left behind: %v", entries)
	}
}

func TestFileVerbsHonorTheAllowlist(t *testing.T) {
	var out bytes.Buffer
	if err := MaintainFileRead("etc-passwd", testNonce, &out); err != nil {
		t.Fatal(err)
	}
	if result := parseRun(t, &out); result.Err == nil || !strings.Contains(result.Err.Message, "allowlist") {
		t.Fatalf("off-allowlist read = %+v", result)
	}
}
