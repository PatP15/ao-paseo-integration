package execution

import (
	"context"
	"crypto/sha256"
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

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// U9a service surface: the machine-scope CLAUDE.md, per-binding instruction
// drift, checkout sync, and skill push-sync. Canonical truth is always the
// project's own git history on the AO machine; hosts only ever converge
// toward it (ff pull, skill push) — no path here writes canon.

// InstructionsChannel is the U9a half of the maintenance channel.
type InstructionsChannel interface {
	ReadInstructions(ctx context.Context, host domain.ExecutionHost) (domain.ExecutionHostPrefs, error)
	WriteInstructions(ctx context.Context, host domain.ExecutionHost, content []byte, expectSHA256 string) (domain.ExecutionHostPrefs, error)
	RepoStatus(ctx context.Context, host domain.ExecutionHost, repoPath, baseBranch string) (domain.ExecutionRepoStatus, error)
	RepoFF(ctx context.Context, host domain.ExecutionHost, repoPath string) (string, error)
	SkillRead(ctx context.Context, host domain.ExecutionHost, name string) ([]domain.ExecutionSkillFile, error)
	SkillPush(ctx context.Context, host domain.ExecutionHost, name string, files []domain.ExecutionSkillFile) error
}

// SetInstructionsChannel installs the U9a channel operations.
func (s *Service) SetInstructionsChannel(channel InstructionsChannel) {
	s.instructions = channel
}

// Instructions returns the host's machine-scope CLAUDE.md: the confirmed copy,
// refreshed live first when asked.
func (s *Service) Instructions(ctx context.Context, id domain.ExecutionHostID, refresh bool) (domain.ExecutionHostPrefs, bool, error) {
	host, err := s.instructionsHost(ctx, id)
	if err != nil {
		return domain.ExecutionHostPrefs{}, false, err
	}
	if refresh {
		live, err := s.instructions.ReadInstructions(ctx, host)
		if err != nil {
			return domain.ExecutionHostPrefs{}, false, HostChannelError(host, apierr.KindInternal, "HOST_INSTRUCTIONS_UNAVAILABLE",
				"%s did not answer the instructions read. Test the connection on that computer, then reopen this tab.", err)
		}
		live.ConfirmedAt = s.now().UTC()
		if err := s.store.UpsertExecutionHostInstructions(ctx, live); err != nil {
			return domain.ExecutionHostPrefs{}, false, fmt.Errorf("persist host %s instructions: %w", host.ID, err)
		}
	}
	return s.store.GetExecutionHostInstructions(ctx, host.ID)
}

// PutInstructions replaces the machine-scope CLAUDE.md: write → worker
// confirm-read → persist, guarded by baseSHA256 exactly like preferences.
func (s *Service) PutInstructions(ctx context.Context, id domain.ExecutionHostID, content, baseSHA256 string) (domain.ExecutionHostPrefs, error) {
	if strings.TrimSpace(content) == "" {
		return domain.ExecutionHostPrefs{}, apierr.Invalid("INSTRUCTIONS_CONTENT_REQUIRED", "content is required", nil)
	}
	baseSHA256 = strings.ToLower(strings.TrimSpace(baseSHA256))
	if baseSHA256 == "" {
		return domain.ExecutionHostPrefs{}, apierr.Invalid("INSTRUCTIONS_BASE_HASH_REQUIRED",
			"baseSha256 is required: it is the hash of the content currently on the host, from the instructions read", nil)
	}
	host, err := s.instructionsHost(ctx, id)
	if err != nil {
		return domain.ExecutionHostPrefs{}, err
	}
	confirmed, err := s.instructions.WriteInstructions(ctx, host, []byte(content), baseSHA256)
	if err != nil {
		return domain.ExecutionHostPrefs{}, HostChannelError(host, apierr.KindInternal, "HOST_INSTRUCTIONS_WRITE_FAILED",
			"%s did not accept the instructions. Nothing was written there; reopen this tab to see what the file says now.", err)
	}
	confirmed.ConfirmedAt = s.now().UTC()
	if err := s.store.UpsertExecutionHostInstructions(ctx, confirmed); err != nil {
		return domain.ExecutionHostPrefs{}, fmt.Errorf("persist host %s instructions: %w", host.ID, err)
	}
	return confirmed, nil
}

// BindingDrift is one host binding's instruction-file state against canon.
type BindingDrift struct {
	HostID       domain.ExecutionHostID
	HostRepoPath string
	BaseBranch   string
	Head         string
	// InSync is true when every canonical instruction file hashes identically
	// at the host checkout's base branch, and nothing extra exists there.
	InSync bool
	// DriftedPaths lists instruction files whose committed content differs
	// (or exists on only one side).
	DriftedPaths []string
	// Error carries a per-binding read failure so one unreachable host does
	// not blank the whole project view.
	Error string
}

// CanonicalFile is one instruction file at the project's default branch.
type CanonicalFile struct {
	Path    string
	SHA256  string
	Content string
}

// ProjectInstructions is the canonical instruction set plus per-binding drift.
type ProjectInstructions struct {
	Branch   string
	Files    []CanonicalFile
	Bindings []BindingDrift
}

// ProjectInstructionsView renders the project's committed instruction files
// and compares every host binding's checkout against them, live.
func (s *Service) ProjectInstructionsView(ctx context.Context, projectID domain.ProjectID) (ProjectInstructions, error) {
	projectID = domain.ProjectID(strings.TrimSpace(string(projectID)))
	if projectID == "" {
		return ProjectInstructions{}, apierr.Invalid("PROJECT_ID_REQUIRED", "projectId is required", nil)
	}
	project, found, err := s.store.GetProject(ctx, string(projectID))
	if err != nil {
		return ProjectInstructions{}, fmt.Errorf("get project %s: %w", projectID, err)
	}
	if !found {
		return ProjectInstructions{}, apierr.NotFound("PROJECT_NOT_FOUND", "project "+string(projectID)+" was not found")
	}
	branch := strings.TrimSpace(project.Config.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	files, err := canonicalInstructionFiles(project.Path, branch)
	if err != nil {
		return ProjectInstructions{}, apierr.Conflict("CANON_UNREADABLE",
			"cannot read the project's committed instruction files: "+err.Error(), nil)
	}
	view := ProjectInstructions{Branch: branch, Files: files}

	bindings, err := s.store.ListProjectHostBindings(ctx, projectID)
	if err != nil {
		return ProjectInstructions{}, fmt.Errorf("list bindings for project %s: %w", projectID, err)
	}
	canonical := map[string]string{}
	for _, file := range files {
		canonical[file.Path] = file.SHA256
	}
	for _, binding := range bindings {
		drift := BindingDrift{
			HostID: binding.HostID, HostRepoPath: binding.HostRepoPath, BaseBranch: binding.BaseBranch,
		}
		if s.instructions == nil {
			drift.Error = "this daemon was started without host maintenance wiring"
		} else if host, hostErr := s.instructionsHost(ctx, binding.HostID); hostErr != nil {
			drift.Error = hostErr.Error()
		} else if status, statusErr := s.instructions.RepoStatus(ctx, host, binding.HostRepoPath, binding.BaseBranch); statusErr != nil {
			drift.Error = statusErr.Error()
		} else {
			drift.Head = status.Head
			drift.DriftedPaths, drift.InSync = diffInstructionHashes(canonical, status.Files)
		}
		view.Bindings = append(view.Bindings, drift)
	}
	return view, nil
}

// SyncBinding fast-forwards one binding's checkout and returns its refreshed
// drift state. A non-ff checkout is refused with git's own words (a typed 409
// from the channel); AO never resolves divergence remotely.
func (s *Service) SyncBinding(ctx context.Context, projectID domain.ProjectID, hostID domain.ExecutionHostID) (BindingDrift, error) {
	projectID = domain.ProjectID(strings.TrimSpace(string(projectID)))
	binding, err := s.projectBinding(ctx, projectID, hostID)
	if err != nil {
		return BindingDrift{}, err
	}
	host, err := s.instructionsHost(ctx, binding.HostID)
	if err != nil {
		return BindingDrift{}, err
	}
	if _, err := s.instructions.RepoFF(ctx, host, binding.HostRepoPath); err != nil {
		return BindingDrift{}, err
	}
	project, found, err := s.store.GetProject(ctx, string(projectID))
	if err != nil || !found {
		return BindingDrift{}, fmt.Errorf("get project %s after sync: %w", projectID, err)
	}
	branch := strings.TrimSpace(project.Config.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	canonicalFiles, err := canonicalInstructionFiles(project.Path, branch)
	if err != nil {
		return BindingDrift{}, apierr.Conflict("CANON_UNREADABLE",
			"cannot read the project's committed instruction files: "+err.Error(), nil)
	}
	canonical := map[string]string{}
	for _, file := range canonicalFiles {
		canonical[file.Path] = file.SHA256
	}
	status, err := s.instructions.RepoStatus(ctx, host, binding.HostRepoPath, binding.BaseBranch)
	if err != nil {
		return BindingDrift{}, err
	}
	drift := BindingDrift{
		HostID: binding.HostID, HostRepoPath: binding.HostRepoPath, BaseBranch: binding.BaseBranch,
		Head: status.Head,
	}
	drift.DriftedPaths, drift.InSync = diffInstructionHashes(canonical, status.Files)
	return drift, nil
}

// SkillSyncSourceLocal names the AO machine's own ~/.claude/skills as a skill
// sync source.
const SkillSyncSourceLocal = "local"

// SyncSkill pushes one skill onto a host — from the AO machine's own skills
// directory or from another registered host — then re-runs the inventory so
// the result is the worker's confirming re-read, never an assumption.
func (s *Service) SyncSkill(ctx context.Context, hostID domain.ExecutionHostID, name, source string) (HostInventory, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\ `) || strings.Contains(name, "..") || strings.HasPrefix(name, ".") {
		return HostInventory{}, apierr.Invalid("SKILL_NAME_INVALID", "skill name must be a bare directory name", nil)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return HostInventory{}, apierr.Invalid("SKILL_SOURCE_REQUIRED",
			"source is required: 'local' for this machine's ~/.claude/skills, or a registered host id", nil)
	}
	target, err := s.instructionsHost(ctx, hostID)
	if err != nil {
		return HostInventory{}, err
	}

	var files []domain.ExecutionSkillFile
	if source == SkillSyncSourceLocal {
		files, err = localSkillFiles(name)
		if err != nil {
			return HostInventory{}, apierr.NotFound("SKILL_NOT_FOUND", err.Error())
		}
	} else {
		sourceHost, sourceErr := s.instructionsHost(ctx, domain.ExecutionHostID(source))
		if sourceErr != nil {
			return HostInventory{}, sourceErr
		}
		files, err = s.instructions.SkillRead(ctx, sourceHost, name)
		if err != nil {
			return HostInventory{}, err
		}
	}
	if err := s.instructions.SkillPush(ctx, target, name, files); err != nil {
		return HostInventory{}, err
	}
	// The confirming re-read: the skill is installed when the host's own
	// inventory says so, not when the push returned.
	inventory, err := s.Inventory(ctx, target.ID, true)
	if err != nil {
		return HostInventory{}, err
	}
	for _, skill := range inventory.Skills {
		if skill.Name == name {
			return inventory, nil
		}
	}
	return HostInventory{}, apierr.Conflict("SKILL_SYNC_UNCONFIRMED",
		"the push completed but the host's re-inventory does not list "+name, nil)
}

func (s *Service) instructionsHost(ctx context.Context, id domain.ExecutionHostID) (domain.ExecutionHost, error) {
	id = domain.ExecutionHostID(strings.TrimSpace(string(id)))
	if id == "" {
		return domain.ExecutionHost{}, apierr.Invalid("HOST_ID_REQUIRED", "hostId is required", nil)
	}
	host, _, found, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("get execution host %s: %w", id, err)
	}
	if !found {
		return domain.ExecutionHost{}, apierr.NotFound("HOST_NOT_FOUND", "host "+string(id)+" is not registered")
	}
	if s.instructions == nil {
		return domain.ExecutionHost{}, apierr.Internal("MAINTENANCE_CHANNEL_UNAVAILABLE",
			"this daemon was started without host maintenance wiring")
	}
	return host, nil
}

func (s *Service) projectBinding(ctx context.Context, projectID domain.ProjectID, hostID domain.ExecutionHostID) (domain.ProjectHostBinding, error) {
	if projectID == "" {
		return domain.ProjectHostBinding{}, apierr.Invalid("PROJECT_ID_REQUIRED", "projectId is required", nil)
	}
	bindings, err := s.store.ListProjectHostBindings(ctx, projectID)
	if err != nil {
		return domain.ProjectHostBinding{}, fmt.Errorf("list bindings for project %s: %w", projectID, err)
	}
	for _, binding := range bindings {
		if binding.HostID == hostID {
			return binding, nil
		}
	}
	return domain.ProjectHostBinding{}, apierr.NotFound("BINDING_NOT_FOUND",
		fmt.Sprintf("project %s is not bound to host %s", projectID, hostID))
}

func diffInstructionHashes(canonical map[string]string, remote []domain.ExecutionRepoFile) ([]string, bool) {
	remoteByPath := map[string]string{}
	for _, file := range remote {
		remoteByPath[file.Path] = file.SHA256
	}
	driftedSet := map[string]struct{}{}
	for path, sum := range canonical {
		if remoteByPath[path] != sum {
			driftedSet[path] = struct{}{}
		}
	}
	for path := range remoteByPath {
		if _, ok := canonical[path]; !ok {
			driftedSet[path] = struct{}{}
		}
	}
	drifted := make([]string, 0, len(driftedSet))
	for path := range driftedSet {
		drifted = append(drifted, path)
	}
	sort.Strings(drifted)
	return drifted, len(drifted) == 0
}

// instructionFilePattern mirrors the worker's: the same rule must select
// files on both sides or drift becomes an artifact of the comparison.
var instructionFilePattern = regexp.MustCompile(`^(CLAUDE\.md|AGENTS\.md|\.claude/skills/.+)$`)

// canonicalInstructionFiles reads the committed instruction files at the
// project's default branch — committed on purpose: "canonical" is what a new
// worktree would receive, not scratch edits in the working tree.
func canonicalInstructionFiles(repoPath, branch string) ([]CanonicalFile, error) {
	listed, err := exec.Command("git", "-C", repoPath, "ls-tree", "-r", "--name-only", branch).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree %s failed in %s", branch, repoPath)
	}
	var paths []string
	for _, line := range strings.Split(string(listed), "\n") {
		if line = strings.TrimSpace(line); line != "" && instructionFilePattern.MatchString(line) {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	files := make([]CanonicalFile, 0, len(paths))
	for _, path := range paths {
		content, err := exec.Command("git", "-C", repoPath, "show", branch+":"+path).Output()
		if err != nil {
			return nil, fmt.Errorf("git show %s:%s failed", branch, path)
		}
		digest := sha256.Sum256(content)
		files = append(files, CanonicalFile{
			Path: path, SHA256: hex.EncodeToString(digest[:]), Content: string(content),
		})
	}
	return files, nil
}

// localSkillFiles reads one skill from the AO machine's own skills directory.
func localSkillFiles(name string) ([]domain.ExecutionSkillFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	return localSkillFilesFrom(filepath.Join(home, ".claude", "skills"), name)
}

func localSkillFilesFrom(skillsDir, name string) ([]domain.ExecutionSkillFile, error) {
	skillPath := filepath.Join(skillsDir, name)
	info, err := os.Lstat(skillPath)
	if err != nil {
		return nil, fmt.Errorf("open local skill %s: %w", name, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("open local skill %s: skill root is not a directory", name)
	}
	// Pin the selected directory by inode. OpenRoot follows the path supplied
	// as its root, so containment alone is insufficient when the skill name is
	// itself a symlink or is swapped between validation and open.
	root, err := os.OpenRoot(skillPath)
	if err != nil {
		return nil, fmt.Errorf("open local skill %s: %w", name, err)
	}
	defer func() { _ = root.Close() }()
	openedInfo, err := root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("open local skill %s: %w", name, err)
	}
	if !openedInfo.IsDir() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("open local skill %s: skill root changed while opening", name)
	}
	var files []domain.ExecutionSkillFile
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("local skill %s contains symbolic link %q", name, filepath.ToSlash(path))
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("local skill %s contains non-regular file %q", name, filepath.ToSlash(path))
		}
		content, readErr := readLocalSkillFile(root, path)
		if readErr != nil {
			return readErr
		}
		files = append(files, domain.ExecutionSkillFile{Path: filepath.ToSlash(path), Content: content})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read local skill %s: %w", name, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("local skill %s has no files", name)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func readLocalSkillFile(root *os.Root, path string) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("local skill file %q is not regular", filepath.ToSlash(path))
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("local skill file %q changed while opening", filepath.ToSlash(path))
	}
	return io.ReadAll(file)
}
