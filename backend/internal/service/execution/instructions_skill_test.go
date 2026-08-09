package execution

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalSkillFilesFromRoundTripsRegularFiles(t *testing.T) {
	skillsDir := t.TempDir()
	skillDir := filepath.Join(skillsDir, "advisor")
	if err := os.MkdirAll(filepath.Join(skillDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte("---\nname: advisor\n---\n")
	notes := []byte{0, 1, 2, 3, 255}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "assets", "notes.bin"), notes, 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := localSkillFilesFrom(skillsDir, "advisor")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "SKILL.md" || !bytes.Equal(files[0].Content, manifest) ||
		files[1].Path != "assets/notes.bin" || !bytes.Equal(files[1].Content, notes) {
		t.Fatalf("files = %+v", files)
	}
}

func TestLocalSkillFilesFromRefusesSymlinkedRoot(t *testing.T) {
	skillsDir := t.TempDir()
	outside := t.TempDir()
	secret := []byte("must-not-leave-root")
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(skillsDir, "advisor")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files, err := localSkillFilesFrom(skillsDir, "advisor")
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	for _, file := range files {
		if bytes.Contains(file.Content, secret) {
			t.Fatal("symlinked skill root content escaped")
		}
	}
}

func TestLocalSkillFilesFromRefusesSymlinkedFile(t *testing.T) {
	skillsDir := t.TempDir()
	skillDir := filepath.Join(skillsDir, "advisor")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	secret := []byte("must-not-leave-root")
	if err := os.WriteFile(outside, secret, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files, err := localSkillFilesFrom(skillsDir, "advisor")
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	for _, file := range files {
		if bytes.Contains(file.Content, secret) {
			t.Fatal("symlinked local skill file content escaped")
		}
	}
}
