package paseo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

// The maintenance channel (U9) drives the AO-owned worker binary on a host
// through a Paseo terminal: a local-isolation workspace is created on demand,
// a terminal in it runs one `ao-paseo-reporter maintain ...` invocation under
// a fresh nonce, the framed stdout is read back through terminal capture, and
// the workspace is archived. The frame layer (crc32, 76-col, base64) is the
// parser; no LLM touches the byte path.

func sha256HexOf(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// maintenanceClient is the optional CLI surface the channel needs, discovered
// structurally like reportTerminalClient.
type maintenanceClient interface {
	CreateLocalWorkspace(context.Context, string, string) (Workspace, error)
	ArchiveWorkspace(context.Context, string) error
	CreateTerminal(context.Context, TerminalCreateRequest) (Terminal, error)
	SendTerminalKeys(context.Context, string, ...string) error
	CaptureTerminal(context.Context, string, int, int) (TerminalCapture, error)
	KillTerminal(context.Context, string) error
}

const (
	// maintenancePollInterval paces capture reads. Each is one CLI invocation
	// (~0.9s, spike FINDINGS S10), so polling faster buys nothing.
	maintenancePollInterval = 1 * time.Second
	// maintenanceCaptureLimit bounds one capture window. Maintenance output is
	// tens of frames; a window this large means one read sees everything.
	maintenanceCaptureLimit = 5000
)

// newMaintenanceNonce mints the per-invocation channel nonce. Overridable in
// tests; the shape matches launch nonces so the shared frame codec applies.
var newMaintenanceNonce = func() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint maintenance nonce: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// HostInventory runs the inventory verb and returns the host's installed
// skills, name-sorted.
func (b *Backend) HostInventory(ctx context.Context, hostID domain.ExecutionHostID) ([]domain.ExecutionHostSkill, error) {
	result, err := b.runMaintenance(ctx, hostID, "inventory", nil, nil)
	if err != nil {
		return nil, err
	}
	if result.Done.Count != len(result.Skills) {
		return nil, fmt.Errorf("maintenance inventory reported %d skills but carried %d",
			result.Done.Count, len(result.Skills))
	}
	skills := make([]domain.ExecutionHostSkill, 0, len(result.Skills))
	for _, skill := range result.Skills {
		skills = append(skills, domain.ExecutionHostSkill{
			HostID: hostID, Name: skill.Name, Description: skill.Description,
		})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// HostPrefs runs the prefs-read verb and returns the preferences file exactly
// as it is on the host, with its hash.
func (b *Backend) HostPrefs(ctx context.Context, hostID domain.ExecutionHostID) (domain.ExecutionHostPrefs, error) {
	result, err := b.runMaintenance(ctx, hostID, "prefs-read", nil, nil)
	if err != nil {
		return domain.ExecutionHostPrefs{}, err
	}
	return assemblePrefs(hostID, result)
}

// WriteHostPrefs replaces the preferences file on the host, guarded by
// expectSHA256 (hex digest of the content currently on disk; a missing file
// hashes the empty string). The returned prefs are the worker's re-read of
// what it committed, not an echo of the input.
func (b *Backend) WriteHostPrefs(
	ctx context.Context, hostID domain.ExecutionHostID, content []byte, expectSHA256 string,
) (domain.ExecutionHostPrefs, error) {
	if len(content) == 0 {
		return domain.ExecutionHostPrefs{}, fmt.Errorf("preferences content is empty")
	}
	if len(content) > paseoevent.MaxPrefsFileBytes {
		return domain.ExecutionHostPrefs{}, fmt.Errorf("preferences content is %d bytes, over the %d byte cap",
			len(content), paseoevent.MaxPrefsFileBytes)
	}
	args := []string{
		"--content-b64", base64.StdEncoding.EncodeToString(content),
		"--sha256", sha256HexOf(content),
		"--expect-sha256", strings.ToLower(strings.TrimSpace(expectSHA256)),
	}
	result, err := b.runMaintenance(ctx, hostID, "prefs-write", args, nil)
	if err != nil {
		return domain.ExecutionHostPrefs{}, err
	}
	return assemblePrefs(hostID, result)
}

// HostInstructions reads the machine-scope CLAUDE.md through the channel.
func (b *Backend) HostInstructions(ctx context.Context, hostID domain.ExecutionHostID) (domain.ExecutionHostPrefs, error) {
	result, err := b.runMaintenance(ctx, hostID, "file-read", []string{"--target", "machine-claude"}, nil)
	if err != nil {
		return domain.ExecutionHostPrefs{}, err
	}
	return assemblePrefs(hostID, result)
}

// WriteHostInstructions replaces the machine-scope CLAUDE.md under the same
// drift precondition and confirm-read as the preferences write.
func (b *Backend) WriteHostInstructions(
	ctx context.Context, hostID domain.ExecutionHostID, content []byte, expectSHA256 string,
) (domain.ExecutionHostPrefs, error) {
	if len(content) == 0 {
		return domain.ExecutionHostPrefs{}, fmt.Errorf("instructions content is empty")
	}
	if len(content) > paseoevent.MaxPrefsFileBytes {
		return domain.ExecutionHostPrefs{}, fmt.Errorf("instructions content is %d bytes, over the %d byte cap",
			len(content), paseoevent.MaxPrefsFileBytes)
	}
	args := []string{
		"--target", "machine-claude",
		"--expect-sha256", strings.ToLower(strings.TrimSpace(expectSHA256)),
	}
	build := func(nonce string) ([]string, error) {
		lines, seq, err := paseoevent.EncodeFileChunkEvents(
			nonce, paseoevent.MaintenancePushFile, "content", content, 1)
		if err != nil {
			return nil, err
		}
		end, err := paseoevent.EncodeMaintenanceEvent(nonce, seq, paseoevent.MaintenancePushEnd,
			paseoevent.MaintenancePushEndPayload{Files: 1})
		if err != nil {
			return nil, err
		}
		return append(lines, end...), nil
	}
	result, err := b.runMaintenance(ctx, hostID, "file-write", args, build)
	if err != nil {
		return domain.ExecutionHostPrefs{}, err
	}
	return assemblePrefs(hostID, result)
}

// HostRepoStatus hashes one checkout's instruction files at its base branch.
func (b *Backend) HostRepoStatus(
	ctx context.Context, hostID domain.ExecutionHostID, repoPath, baseBranch string,
) (domain.ExecutionRepoStatus, error) {
	if err := validateHostPathArg(repoPath); err != nil {
		return domain.ExecutionRepoStatus{}, err
	}
	if err := validateHostNameArg(baseBranch); err != nil {
		return domain.ExecutionRepoStatus{}, err
	}
	result, err := b.runMaintenance(ctx, hostID, "repo-status",
		[]string{"--repo", repoPath, "--base", baseBranch}, nil)
	if err != nil {
		return domain.ExecutionRepoStatus{}, err
	}
	if result.Done.Count != len(result.RepoFiles) {
		return domain.ExecutionRepoStatus{}, fmt.Errorf("repo status reported %d files but carried %d",
			result.Done.Count, len(result.RepoFiles))
	}
	status := domain.ExecutionRepoStatus{Head: result.Done.Head}
	for _, file := range result.RepoFiles {
		status.Files = append(status.Files, domain.ExecutionRepoFile{Path: file.Path, SHA256: file.SHA256})
	}
	return status, nil
}

// HostRepoFF fast-forwards one checkout; anything else is a typed refusal
// carrying git's own words.
func (b *Backend) HostRepoFF(ctx context.Context, hostID domain.ExecutionHostID, repoPath string) (string, error) {
	if err := validateHostPathArg(repoPath); err != nil {
		return "", err
	}
	result, err := b.runMaintenance(ctx, hostID, "repo-ff", []string{"--repo", repoPath}, nil)
	if err != nil {
		return "", err
	}
	return result.Done.Head, nil
}

// HostSkillRead fetches one installed skill's files from the host.
func (b *Backend) HostSkillRead(
	ctx context.Context, hostID domain.ExecutionHostID, name string,
) ([]domain.ExecutionSkillFile, error) {
	if err := validateHostNameArg(name); err != nil {
		return nil, err
	}
	result, err := b.runMaintenance(ctx, hostID, "skill-read", []string{"--name", name}, nil)
	if err != nil {
		return nil, err
	}
	assembled, err := paseoevent.AssembleFileChunks(result.FileChunks)
	if err != nil {
		return nil, err
	}
	if result.Done.Count != len(assembled) {
		return nil, fmt.Errorf("skill read reported %d files but carried %d", result.Done.Count, len(assembled))
	}
	files := make([]domain.ExecutionSkillFile, 0, len(assembled))
	for _, file := range assembled {
		files = append(files, domain.ExecutionSkillFile{Path: file.Path, Content: file.Content})
	}
	return files, nil
}

// HostSkillPush installs one skill directory on the host by typing its files
// into the maintenance terminal as inbound frames; the worker verifies every
// hash before the staged directory atomically replaces the old one.
func (b *Backend) HostSkillPush(
	ctx context.Context, hostID domain.ExecutionHostID, name string, files []domain.ExecutionSkillFile,
) error {
	if err := validateHostNameArg(name); err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("skill %s has no files to push", name)
	}
	return b.runMaintenancePush(ctx, hostID, name, files)
}

func (b *Backend) runMaintenancePush(
	ctx context.Context, hostID domain.ExecutionHostID, name string, files []domain.ExecutionSkillFile,
) error {
	// The input frames are nonce-scoped like everything else; encoding happens
	// after runMaintenance mints the nonce, so the input builder is a closure.
	build := func(nonce string) ([]string, error) {
		var lines []string
		seq := 1
		for _, file := range files {
			encoded, next, err := paseoevent.EncodeFileChunkEvents(
				nonce, paseoevent.MaintenancePushFile, file.Path, file.Content, seq)
			if err != nil {
				return nil, err
			}
			lines = append(lines, encoded...)
			seq = next
		}
		end, err := paseoevent.EncodeMaintenanceEvent(nonce, seq, paseoevent.MaintenancePushEnd,
			paseoevent.MaintenancePushEndPayload{Files: len(files)})
		if err != nil {
			return nil, err
		}
		return append(lines, end...), nil
	}
	_, err := b.runMaintenance(ctx, hostID, "skill-push", []string{"--name", name}, build)
	return err
}

func validateHostPathArg(path string) error {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \t\r\n") {
		return fmt.Errorf("host path %q must be absolute with no whitespace", path)
	}
	return nil
}

func validateHostNameArg(name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\r\n/\\") {
		return fmt.Errorf("%q is not a valid bare name", name)
	}
	return nil
}

// MaintenanceRefusedError is a worker-side refusal (drift, corruption, cap)
// carried verbatim; the operation did not happen.
type MaintenanceRefusedError struct{ Message string }

func (e *MaintenanceRefusedError) Error() string { return "maintenance refused: " + e.Message }

func (b *Backend) runMaintenance(
	ctx context.Context, hostID domain.ExecutionHostID, verb string, extraArgs []string,
	buildInput func(nonce string) ([]string, error),
) (paseoevent.MaintenanceResult, error) {
	host, err := b.registeredHost(ctx, hostID)
	if err != nil {
		return paseoevent.MaintenanceResult{}, err
	}
	client, ok := b.client.(maintenanceClient)
	if !ok {
		return paseoevent.MaintenanceResult{}, fmt.Errorf("execution client does not support the maintenance channel")
	}
	nonce, err := newMaintenanceNonce()
	if err != nil {
		return paseoevent.MaintenanceResult{}, err
	}
	if err := paseoevent.ValidateNonce(nonce); err != nil {
		return paseoevent.MaintenanceResult{}, err
	}

	// The workspace lives in the home directory the channel learned from a
	// previous run's done event; before anything has been learned, "/" is the
	// only path AO can name on a machine it knows nothing about. A learned
	// home that no longer works falls back the same way and is re-learned
	// from this run's own done event.
	title := fmt.Sprintf("ao-maintenance:%s:%s", hostID, nonce)
	path := host.MaintenanceHome
	if path == "" {
		path = "/"
	}
	workspace, err := client.CreateLocalWorkspace(ctx, path, title)
	if err != nil && path != "/" {
		path = "/"
		workspace, err = client.CreateLocalWorkspace(ctx, path, title)
	}
	if err != nil {
		return paseoevent.MaintenanceResult{}, fmt.Errorf("create maintenance workspace on host %s: %w", hostID, err)
	}
	if workspace.WorkspaceID == "" || workspace.Cwd == "" {
		return paseoevent.MaintenanceResult{}, fmt.Errorf("maintenance workspace response is missing id or cwd")
	}
	// Archive on the way out regardless of outcome: the workspace exists only
	// to host this one invocation. A failed archive is not worth failing the
	// operation that already completed, but it must not be silent either —
	// the error wraps through to the caller's logs via the returned error only
	// when the run itself also failed.
	defer func() { _ = client.ArchiveWorkspace(context.WithoutCancel(ctx), workspace.WorkspaceID) }()

	terminal, err := client.CreateTerminal(ctx, TerminalCreateRequest{
		WorkspaceID: workspace.WorkspaceID, Cwd: workspace.Cwd, Name: "ao-maintenance-" + nonce,
	})
	if err != nil {
		return paseoevent.MaintenanceResult{}, fmt.Errorf("create maintenance terminal on host %s: %w", hostID, err)
	}
	if terminal.ID == "" {
		return paseoevent.MaintenanceResult{}, fmt.Errorf("maintenance terminal response is missing an id")
	}
	// Terminals join workspaces by cwd, so archiving alone may not end this
	// one; kill it explicitly. Best-effort like the archive.
	defer func() { _ = client.KillTerminal(context.WithoutCancel(ctx), terminal.ID) }()

	command := []string{
		shellQuote(paseoevent.ReporterBinary), "maintain", verb, "--nonce", shellQuote(nonce),
	}
	for _, arg := range extraArgs {
		command = append(command, shellQuote(arg))
	}
	if err := client.SendTerminalKeys(ctx, terminal.ID, "C-c", strings.Join(command, " "), "Enter"); err != nil {
		return paseoevent.MaintenanceResult{}, fmt.Errorf("start maintenance command on host %s: %w", hostID, err)
	}

	if buildInput != nil {
		lines, err := buildInput(nonce)
		if err != nil {
			return paseoevent.MaintenanceResult{}, err
		}
		// Frames are 76 columns each, far under the PTY's canonical line
		// limit; batches keep each send-keys argv modest, and the ~1s cost of
		// each CLI invocation naturally paces input well below what the
		// reading process drains.
		const batch = 30
		for start := 0; start < len(lines); start += batch {
			end := min(start+batch, len(lines))
			keys := make([]string, 0, (end-start)*2)
			for _, line := range lines[start:end] {
				keys = append(keys, line, "Enter")
			}
			if err := client.SendTerminalKeys(ctx, terminal.ID, keys...); err != nil {
				return paseoevent.MaintenanceResult{}, fmt.Errorf("send maintenance input on host %s: %w", hostID, err)
			}
		}
	}

	for {
		capture, err := client.CaptureTerminal(ctx, terminal.ID, 0, maintenanceCaptureLimit)
		if err != nil {
			return paseoevent.MaintenanceResult{}, fmt.Errorf("read maintenance output on host %s: %w", hostID, err)
		}
		result, err := paseoevent.ParseMaintenanceRun(nonce, capture.Lines)
		if err != nil {
			return paseoevent.MaintenanceResult{}, fmt.Errorf("decode maintenance output on host %s: %w", hostID, err)
		}
		if result.Err != nil {
			return paseoevent.MaintenanceResult{}, &MaintenanceRefusedError{Message: result.Err.Message}
		}
		if result.Done != nil {
			// Learn (or correct) the worker's home for the next run's
			// workspace. Best-effort: the facts this run carried do not
			// depend on remembering where to put the next one.
			if home := strings.TrimSpace(result.Done.Home); home != "" && home != host.MaintenanceHome {
				_ = b.store.SetExecutionHostMaintenanceHome(context.WithoutCancel(ctx), hostID, home)
			}
			return result, nil
		}
		select {
		case <-ctx.Done():
			return paseoevent.MaintenanceResult{}, fmt.Errorf("maintenance %s on host %s: %w", verb, hostID, ctx.Err())
		case <-time.After(maintenancePollInterval):
		}
	}
}

func assemblePrefs(hostID domain.ExecutionHostID, result paseoevent.MaintenanceResult) (domain.ExecutionHostPrefs, error) {
	done := result.Done
	if done.Parts != len(result.PrefsParts) {
		return domain.ExecutionHostPrefs{}, fmt.Errorf("maintenance prefs reported %d parts but carried %d",
			done.Parts, len(result.PrefsParts))
	}
	var content []byte
	for part := 1; part <= done.Parts; part++ {
		encoded, ok := result.PrefsParts[part]
		if !ok {
			return domain.ExecutionHostPrefs{}, fmt.Errorf("maintenance prefs part %d is missing", part)
		}
		chunk, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return domain.ExecutionHostPrefs{}, fmt.Errorf("maintenance prefs part %d is not valid base64: %w", part, err)
		}
		content = append(content, chunk...)
	}
	if digest := sha256HexOf(content); digest != done.SHA256 {
		return domain.ExecutionHostPrefs{}, fmt.Errorf("maintenance prefs content hashes to %s, not the declared %s",
			digest, done.SHA256)
	}
	return domain.ExecutionHostPrefs{
		HostID: hostID, Content: string(content), SHA256: done.SHA256, Exists: done.Exists,
	}, nil
}
