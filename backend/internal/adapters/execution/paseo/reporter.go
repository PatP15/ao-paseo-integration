package paseo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
)

type reportTerminalClient interface {
	CreateTerminal(context.Context, TerminalCreateRequest) (Terminal, error)
	SendTerminalKeys(context.Context, string, ...string) error
}

// PrepareReportTransport creates and starts the dedicated rung-0 reporter
// terminal for a launch. The method deliberately sits outside ports: it is an
// optional AO report capability discovered structurally by the dispatch worker,
// not part of the execution substrate every backend must provide.
func (b *Backend) PrepareReportTransport(
	ctx context.Context,
	sessionID domain.SessionID,
	workspaceID domain.ExecutionWorkspaceID,
	launchID, nonce string,
) error {
	if strings.TrimSpace(string(sessionID)) == "" || strings.TrimSpace(string(workspaceID)) == "" || strings.TrimSpace(launchID) == "" {
		return fmt.Errorf("report transport requires session, workspace, and launch ids")
	}
	if err := paseoevent.ValidateNonce(nonce); err != nil {
		return err
	}
	client, ok := b.client.(reportTerminalClient)
	if !ok {
		return fmt.Errorf("execution client does not support report terminals")
	}
	binding, found, err := b.store.GetSessionExecutionBinding(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("load execution binding for report terminal: %w", err)
	}
	if !found || binding.BackendType != domain.ExecutionBackendPaseo || binding.ExternalWorkspaceID != workspaceID ||
		binding.HostWorkspacePath == "" {
		return fmt.Errorf("report terminal requires the persisted Paseo workspace binding")
	}

	terminalID := binding.TerminalID
	if terminalID == "" {
		name := reporterTerminalName(launchID)
		terminal, err := client.CreateTerminal(ctx, TerminalCreateRequest{
			WorkspaceID: string(workspaceID), Cwd: binding.HostWorkspacePath, Name: name,
		})
		if err != nil {
			return fmt.Errorf("create report terminal: %w", err)
		}
		if terminal.ID == "" || terminal.Name != name || terminal.Cwd != binding.HostWorkspacePath {
			return fmt.Errorf("report terminal response did not match the workspace")
		}
		terminalID = terminal.ID
		binding.TerminalID = terminalID
		if err := b.store.UpsertSessionExecutionBinding(ctx, binding); err != nil {
			return fmt.Errorf("persist report terminal id: %w", err)
		}
	}

	// C-c makes replay safe across a daemon crash after persisting terminal_id
	// but before or during send-keys. A running reporter is stopped and restarted
	// from the append-only spool; eventId dedupe makes the replay free.
	command := strings.Join([]string{
		shellQuote(paseoevent.ReporterBinary), "serve",
		"--session", shellQuote(string(sessionID)),
		"--launch", shellQuote(launchID),
		"--nonce", shellQuote(nonce),
	}, " ")
	if err := client.SendTerminalKeys(ctx, terminalID, "C-c", command, "Enter"); err != nil {
		return fmt.Errorf("start report terminal: %w", err)
	}
	return nil
}

func reporterTerminalName(launchID string) string {
	digest := sha256.Sum256([]byte(launchID))
	return "ao-reporter-" + hex.EncodeToString(digest[:6])
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
