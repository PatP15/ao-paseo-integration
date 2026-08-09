package paseoreporter

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

// U9a: instruction files, repo drift, and skill transfer through the same
// channel as U9. Every path the file verbs touch comes from an explicit
// allowlist keyed by target name — a caller-supplied path is never accepted —
// and a pushed skill name obeys the same bare-name rule the secret resolver
// uses.

// fileTargets is the complete allowlist of files the file verbs may touch.
var fileTargets = map[string]func() (string, error){
	// The machine-scope Claude instructions: applies to every agent on this
	// host, which is exactly why it is the only writable file target.
	"machine-claude": func() (string, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(home, ".claude", "CLAUDE.md"), nil
	},
}

func resolveFileTarget(target string) (string, error) {
	resolve, ok := fileTargets[target]
	if !ok {
		return "", fmt.Errorf("file target %q is not on the allowlist", target)
	}
	return resolve()
}

// MaintainFileRead emits one allowlisted file's content, chunked, plus done.
func MaintainFileRead(target, nonce string, out io.Writer) error {
	path, err := resolveFileTarget(target)
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	content, exists, err := readPrefs(path)
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	return emitPrefsContent(out, nonce, content, exists)
}

// MaintainFileWrite atomically replaces one allowlisted file under the same
// drift precondition as the preferences write, confirming by re-read. The new
// content arrives as inbound frames on stdin — an instructions file can be
// far larger than fits one typed terminal line — carrying exactly one file
// whose hash is verified before anything touches disk.
func MaintainFileWrite(target, nonce, expectSHA256 string, in io.Reader, out io.Writer) error {
	path, err := resolveFileTarget(target)
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	files, err := readPushStream(nonce, in)
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	if len(files) != 1 {
		return emitMaintenanceError(out, nonce, fmt.Sprintf("file write expects exactly one pushed file, got %d", len(files)))
	}
	content := files[0].Content
	digest := sha256.Sum256(content)
	return MaintainPrefsWrite(path, nonce,
		base64.StdEncoding.EncodeToString(content), hex.EncodeToString(digest[:]), expectSHA256, out)
}

// readPushStream accumulates stdin lines until the push end marker decodes.
func readPushStream(nonce string, in io.Reader) ([]paseoevent.MaintenanceFile, error) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		files, complete, err := paseoevent.ParseMaintenancePush(nonce, lines)
		if err != nil {
			return nil, err
		}
		if complete {
			return files, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read push stream: %w", err)
	}
	return nil, fmt.Errorf("push stream ended before its end marker")
}

// instructionFilePattern matches the repo files whose drift AO tracks.
var instructionFilePattern = regexp.MustCompile(`^(CLAUDE\.md|AGENTS\.md|\.claude/skills/.+)$`)

// MaintainRepoStatus emits the sha256 of each instruction file's content at
// the checkout's base branch, plus done carrying the checkout's HEAD. Hashes
// are of committed content on purpose: drift is a statement about what the
// checkout would deliver to a worktree, not about scratch edits.
func MaintainRepoStatus(repoPath, baseBranch, nonce string, out io.Writer) error {
	if err := validateRepoArgs(repoPath, baseBranch); err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	head, err := gitOutput(repoPath, "rev-parse", "HEAD")
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	listed, err := gitOutput(repoPath, "ls-tree", "-r", "--name-only", baseBranch)
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	var paths []string
	for _, line := range strings.Split(listed, "\n") {
		if line = strings.TrimSpace(line); line != "" && instructionFilePattern.MatchString(line) {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	seq := 0
	for _, path := range paths {
		content, err := gitShow(repoPath, baseBranch, path)
		if err != nil {
			return emitMaintenanceError(out, nonce, err.Error())
		}
		digest := sha256.Sum256(content)
		seq++
		if err := paseoevent.WriteMaintenanceEvent(out, nonce, seq, paseoevent.MaintenanceRepoFile,
			paseoevent.MaintenanceRepoFilePayload{Path: path, SHA256: hex.EncodeToString(digest[:])}); err != nil {
			return err
		}
	}
	seq++
	return paseoevent.WriteMaintenanceEvent(out, nonce, seq, paseoevent.MaintenanceDone,
		paseoevent.MaintenanceDonePayload{Count: len(paths), Head: strings.TrimSpace(head), Home: homeDir()})
}

// MaintainRepoFF fast-forwards the checkout. Anything but a clean
// fast-forward is refused with git's own words: AO never resolves a host
// checkout's divergence remotely.
func MaintainRepoFF(repoPath, nonce string, out io.Writer) error {
	if err := validateRepoArgs(repoPath, "HEAD"); err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	output, err := gitOutput(repoPath, "pull", "--ff-only")
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	_ = output
	head, err := gitOutput(repoPath, "rev-parse", "HEAD")
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	return paseoevent.WriteMaintenanceEvent(out, nonce, 1, paseoevent.MaintenanceDone,
		paseoevent.MaintenanceDonePayload{Head: strings.TrimSpace(head), Home: homeDir()})
}

// MaintainSkillRead emits every file of one installed skill as chunk events.
func MaintainSkillRead(skillsDir, name, nonce string, out io.Writer) error {
	if err := validateBareName(name); err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	rootPath := filepath.Join(skillsDir, name)
	root, err := openDirectoryRoot(rootPath)
	if err != nil {
		return emitMaintenanceError(out, nonce, fmt.Sprintf("read skill %s: %v", name, err))
	}
	defer func() { _ = root.Close() }()
	var paths []string
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill %s contains symbolic link %q", name, filepath.ToSlash(path))
		}
		if !entry.IsDir() && entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return emitMaintenanceError(out, nonce, fmt.Sprintf("read skill %s: %v", name, err))
	}
	sort.Strings(paths)
	seq := 1
	count := 0
	for _, path := range paths {
		content, err := readRegularRootFile(root, path, 0)
		if err != nil {
			return emitMaintenanceError(out, nonce, fmt.Sprintf("read skill file: %v", err))
		}
		lines, next, err := paseoevent.EncodeFileChunkEvents(nonce, paseoevent.MaintenanceSkillFile,
			filepath.ToSlash(path), content, seq)
		if err != nil {
			return err
		}
		seq = next
		count++
		for _, line := range lines {
			if _, err := fmt.Fprintln(out, line); err != nil {
				return fmt.Errorf("write skill frame: %w", err)
			}
		}
	}
	return paseoevent.WriteMaintenanceEvent(out, nonce, seq, paseoevent.MaintenanceDone,
		paseoevent.MaintenanceDonePayload{Count: count, Home: homeDir()})
}

// MaintainSkillPush receives a skill directory as inbound frames on stdin,
// verifies every file against its declared hash, stages the directory, and
// atomically swaps it into ~/.claude/skills/<name>. A failed verify or an
// interrupted stream leaves any existing skill untouched: nothing under the
// final name changes until the rename.
func MaintainSkillPush(skillsDir, name, nonce string, in io.Reader, out io.Writer) error {
	if err := validateBareName(name); err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	files, err := readPushStream(nonce, in)
	if err != nil {
		return emitMaintenanceError(out, nonce, err.Error())
	}
	if len(files) == 0 {
		return emitMaintenanceError(out, nonce, "push carried no files")
	}

	if err := os.MkdirAll(skillsDir, 0o750); err != nil {
		return emitMaintenanceError(out, nonce, "stage skill: "+err.Error())
	}
	stage := filepath.Join(skillsDir, ".ao-stage-"+nonce)
	// Create the staging root exclusively. Using MkdirAll only for file
	// parents would follow a stale or attacker-created symlink at stage and
	// let pushed bytes escape skillsDir before the final rename.
	if err := os.Mkdir(stage, 0o750); err != nil {
		return emitMaintenanceError(out, nonce, "stage skill: "+err.Error())
	}
	defer func() { _ = os.RemoveAll(stage) }()
	for _, file := range files {
		relative := filepath.Clean(filepath.FromSlash(file.Path))
		if relative == "." || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
			return emitMaintenanceError(out, nonce, fmt.Sprintf("pushed path %q escapes the skill directory", file.Path))
		}
		destination := filepath.Join(stage, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
			return emitMaintenanceError(out, nonce, "stage skill: "+err.Error())
		}
		if err := os.WriteFile(destination, file.Content, 0o600); err != nil {
			return emitMaintenanceError(out, nonce, "stage skill: "+err.Error())
		}
	}

	final := filepath.Join(skillsDir, name)
	previous := filepath.Join(skillsDir, ".ao-old-"+nonce)
	hadPrevious := false
	if _, err := os.Stat(final); err == nil {
		hadPrevious = true
		if err := os.Rename(final, previous); err != nil {
			return emitMaintenanceError(out, nonce, "swap skill: "+err.Error())
		}
	}
	if err := os.Rename(stage, final); err != nil {
		if hadPrevious {
			_ = os.Rename(previous, final)
		}
		return emitMaintenanceError(out, nonce, "swap skill: "+err.Error())
	}
	if hadPrevious {
		_ = os.RemoveAll(previous)
	}
	return paseoevent.WriteMaintenanceEvent(out, nonce, 1, paseoevent.MaintenanceDone,
		paseoevent.MaintenanceDonePayload{Count: len(files), Home: homeDir()})
}

func validateBareName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") ||
		strings.HasPrefix(name, ".") || strings.ContainsAny(name, " \t\r\n") {
		return fmt.Errorf("skill name %q must be a bare directory name", name)
	}
	return nil
}

func validateRepoArgs(repoPath, ref string) error {
	if !filepath.IsAbs(repoPath) {
		return fmt.Errorf("repo path %q must be absolute", repoPath)
	}
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\r\n") {
		return fmt.Errorf("ref %q is not a valid branch name", ref)
	}
	if info, err := os.Stat(repoPath); err != nil || !info.IsDir() {
		return fmt.Errorf("repo path %q is not a directory on this host", repoPath)
	}
	return nil
}

// gitOutput runs one git command against the repo, folding stderr into the
// error verbatim — a refused fast-forward must surface git's own words.
func gitOutput(repoPath string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func gitShow(repoPath, ref, path string) ([]byte, error) {
	command := exec.Command("git", "-C", repoPath, "show", ref+":"+path)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s:%s failed", ref, path)
	}
	return output, nil
}
