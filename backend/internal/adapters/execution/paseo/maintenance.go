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
	CreateLocalWorkspace(context.Context, string) (Workspace, error)
	ArchiveWorkspace(context.Context, string) error
	CreateTerminal(context.Context, TerminalCreateRequest) (Terminal, error)
	SendTerminalKeys(context.Context, string, ...string) error
	CaptureTerminal(context.Context, string, int, int) (TerminalCapture, error)
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
	result, err := b.runMaintenance(ctx, hostID, "inventory", nil)
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
	result, err := b.runMaintenance(ctx, hostID, "prefs-read", nil)
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
	result, err := b.runMaintenance(ctx, hostID, "prefs-write", args)
	if err != nil {
		return domain.ExecutionHostPrefs{}, err
	}
	return assemblePrefs(hostID, result)
}

// MaintenanceRefusedError is a worker-side refusal (drift, corruption, cap)
// carried verbatim; the operation did not happen.
type MaintenanceRefusedError struct{ Message string }

func (e *MaintenanceRefusedError) Error() string { return "maintenance refused: " + e.Message }

func (b *Backend) runMaintenance(
	ctx context.Context, hostID domain.ExecutionHostID, verb string, extraArgs []string,
) (paseoevent.MaintenanceResult, error) {
	if _, err := b.registeredHost(ctx, hostID); err != nil {
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

	title := fmt.Sprintf("ao-maintenance:%s:%s", hostID, nonce)
	workspace, err := client.CreateLocalWorkspace(ctx, title)
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

	command := []string{
		shellQuote(paseoevent.ReporterBinary), "maintain", verb, "--nonce", shellQuote(nonce),
	}
	for _, arg := range extraArgs {
		command = append(command, shellQuote(arg))
	}
	if err := client.SendTerminalKeys(ctx, terminal.ID, "C-c", strings.Join(command, " "), "Enter"); err != nil {
		return paseoevent.MaintenanceResult{}, fmt.Errorf("start maintenance command on host %s: %w", hostID, err)
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
