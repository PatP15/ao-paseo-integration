package paseoreporter

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

// The maintain subcommands are the worker half of AO's host maintenance
// channel (U9): AO drives them through a Paseo terminal and reads their framed
// stdout back through terminal capture. Everything they touch is an explicit
// AO-owned location — the skills directory and the orchestration preferences
// file — never a caller-supplied path.

// DefaultSkillsDir is where Claude-family harnesses install skills.
func DefaultSkillsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

// DefaultPrefsPath is the Paseo orchestration preferences file.
func DefaultPrefsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".paseo", "orchestration-preferences.json"), nil
}

// MaintainInventory emits one skill event per installed skill, then done.
// A skill is a directory under skillsDir containing SKILL.md; its name is the
// frontmatter name (falling back to the directory name) and its description is
// the frontmatter description.
func MaintainInventory(skillsDir, nonce string, out io.Writer) error {
	entries, err := os.ReadDir(skillsDir)
	if os.IsNotExist(err) {
		entries = nil
	} else if err != nil {
		return emitMaintenanceError(out, nonce, fmt.Sprintf("read skills directory: %v", err))
	}
	seq := 0
	count := 0
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		manifest := filepath.Join(skillsDir, name, "SKILL.md")
		raw, err := os.ReadFile(manifest)
		if err != nil {
			continue // a directory without a manifest is not a skill
		}
		skillName, description := parseSkillFrontmatter(string(raw))
		if skillName == "" {
			skillName = name
		}
		seq++
		count++
		if err := paseoevent.WriteMaintenanceEvent(out, nonce, seq, paseoevent.MaintenanceSkill,
			paseoevent.MaintenanceSkillPayload{Name: skillName, Description: description}); err != nil {
			return err
		}
	}
	seq++
	return paseoevent.WriteMaintenanceEvent(out, nonce, seq, paseoevent.MaintenanceDone,
		paseoevent.MaintenanceDonePayload{Count: count, Home: homeDir()})
}

// homeDir is best-effort on purpose: an unresolvable home degrades AO back to
// its "/" fallback, it never fails the run that carried real facts.
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// MaintainPrefsRead emits the preferences file as content chunks plus a done
// event carrying the file's sha256. A missing file is done with Exists=false —
// a fact, not an error.
func MaintainPrefsRead(prefsPath, nonce string, out io.Writer) error {
	content, exists, err := readPrefs(prefsPath)
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	return emitPrefsContent(out, nonce, content, exists)
}

// MaintainPrefsWrite atomically replaces the preferences file, guarded by the
// caller's precondition, then emits the re-read content as the confirmation.
//
// expectSHA256 is the hex digest of the content the caller believes is on
// disk (a missing file hashes as the empty string). A mismatch is drift — the
// file changed under AO — and the write is refused whole; a config write may
// never be ambiguous or clobber an edit AO has not seen.
func MaintainPrefsWrite(prefsPath, nonce, contentB64, contentSHA256, expectSHA256 string, out io.Writer) error {
	content, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return emitMaintenanceError(out, nonce, "content is not valid base64: "+err.Error())
	}
	if len(content) > paseoevent.MaxPrefsFileBytes {
		return emitMaintenanceError(out, nonce,
			fmt.Sprintf("content is %d bytes, over the %d byte cap", len(content), paseoevent.MaxPrefsFileBytes))
	}
	if digest := sha256Hex(content); digest != strings.ToLower(strings.TrimSpace(contentSHA256)) {
		return emitMaintenanceError(out, nonce,
			"content arrived corrupted: sha256 "+digest+" does not match the declared "+contentSHA256)
	}
	current, _, err := readPrefs(prefsPath)
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	if digest := sha256Hex(current); digest != strings.ToLower(strings.TrimSpace(expectSHA256)) {
		return emitMaintenanceError(out, nonce,
			"drift: the file on disk hashes to "+digest+", not the expected "+expectSHA256+"; re-read before writing")
	}
	if err := os.MkdirAll(filepath.Dir(prefsPath), 0o700); err != nil {
		return emitMaintenanceError(out, nonce, "create preferences directory: "+err.Error())
	}
	tmp, err := os.CreateTemp(filepath.Dir(prefsPath), ".ao-prefs-*")
	if err != nil {
		return emitMaintenanceError(out, nonce, "stage preferences write: "+err.Error())
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return emitMaintenanceError(out, nonce, "stage preferences write: "+err.Error())
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return emitMaintenanceError(out, nonce, "stage preferences write: "+err.Error())
	}
	if err := os.Rename(tmpPath, prefsPath); err != nil {
		_ = os.Remove(tmpPath)
		return emitMaintenanceError(out, nonce, "commit preferences write: "+err.Error())
	}
	// The confirmation is a genuine re-read of the committed file, not an echo
	// of the input: what AO persists is what is actually on disk.
	confirmed, exists, err := readPrefs(prefsPath)
	if err != nil {
		return emitMaintenanceError(out, nonce, "re-read after write: "+err.Error())
	}
	return emitPrefsContent(out, nonce, confirmed, exists)
}

func emitPrefsContent(out io.Writer, nonce string, content []byte, exists bool) error {
	seq := 0
	chunks := paseoevent.SplitPrefsChunks(content)
	for index, chunk := range chunks {
		seq++
		if err := paseoevent.WriteMaintenanceEvent(out, nonce, seq, paseoevent.MaintenancePrefs,
			paseoevent.MaintenancePrefsPayload{
				Part: index + 1, ContentB64: base64.StdEncoding.EncodeToString(chunk),
			}); err != nil {
			return err
		}
	}
	seq++
	return paseoevent.WriteMaintenanceEvent(out, nonce, seq, paseoevent.MaintenanceDone,
		paseoevent.MaintenanceDonePayload{Parts: len(chunks), SHA256: sha256Hex(content), Exists: exists, Home: homeDir()})
}

func emitMaintenanceError(out io.Writer, nonce, message string) error {
	return paseoevent.WriteMaintenanceEvent(out, nonce, 1, paseoevent.MaintenanceError,
		paseoevent.MaintenanceErrorPayload{Message: message})
}

func readPrefs(path string) ([]byte, bool, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open preferences directory: %w", err)
	}
	defer func() { _ = parent.Close() }()

	name := filepath.Base(path)
	info, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("stat preferences file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("preferences file is not a regular file")
	}
	if info.Size() > paseoevent.MaxPrefsFileBytes {
		return nil, false, fmt.Errorf("preferences file is %d bytes, over the %d byte cap",
			info.Size(), paseoevent.MaxPrefsFileBytes)
	}
	file, err := parent.Open(name)
	if err != nil {
		return nil, false, fmt.Errorf("read preferences file: %w", err)
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat opened preferences file: %w", err)
	}
	// The lstat/open comparison closes the swap window without following a
	// caller-planted symlink: Root.Open cannot escape the parent, and a changed
	// inode is refused before any bytes are read.
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, false, fmt.Errorf("preferences file changed while opening or is not a regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, paseoevent.MaxPrefsFileBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read preferences file: %w", err)
	}
	if len(content) > paseoevent.MaxPrefsFileBytes {
		return nil, false, fmt.Errorf("preferences file is over the %d byte cap", paseoevent.MaxPrefsFileBytes)
	}
	return content, true, nil
}

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// parseSkillFrontmatter extracts name and description from a SKILL.md YAML
// frontmatter block. Deliberately minimal: single-line scalar values only,
// because that is what skill manifests carry; anything else falls back to
// empty and the directory name wins.
func parseSkillFrontmatter(manifest string) (name, description string) {
	lines := strings.Split(manifest, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			break
		}
		if value, ok := strings.CutPrefix(trimmed, "name:"); ok && name == "" {
			name = strings.Trim(strings.TrimSpace(value), `"'`)
		}
		if value, ok := strings.CutPrefix(trimmed, "description:"); ok && description == "" {
			description = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return name, description
}
