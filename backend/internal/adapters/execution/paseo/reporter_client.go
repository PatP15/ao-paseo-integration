package paseo

import (
	"context"
	"fmt"
	"strings"
)

// TerminalCreateRequest is the explicit workspace placement for AO's report
// terminal. Cwd is verified against the workspace response before the terminal
// id is persisted.
type TerminalCreateRequest struct {
	WorkspaceID string
	Cwd         string
	Name        string
}

// Terminal is the small, strict portion of Paseo's terminal-create response AO
// needs to bind later capture reads.
type Terminal struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Cwd  string `json:"cwd"`
}

// CreateTerminal starts a shell terminal in an explicit workspace and cwd.
func (c *Client) CreateTerminal(ctx context.Context, req TerminalCreateRequest) (Terminal, error) {
	args, err := terminalCreateArgs(c.host, req)
	return runJSON[Terminal](ctx, c, args, err)
}

// SendTerminalKeys sends literal key tokens to one explicitly named terminal.
func (c *Client) SendTerminalKeys(ctx context.Context, terminalID string, keys ...string) error {
	args, err := terminalSendKeysArgs(c.host, terminalID, keys)
	return c.runNoOutput(ctx, args, err)
}

// KillTerminal kills one explicitly identified terminal.
func (c *Client) KillTerminal(ctx context.Context, terminalID string) error {
	args, err := terminalKillArgs(c.host, terminalID)
	return c.runNoOutput(ctx, args, err)
}

func terminalCreateArgs(host string, req TerminalCreateRequest) ([]string, error) {
	if strings.TrimSpace(req.WorkspaceID) == "" || strings.TrimSpace(req.Cwd) == "" || strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("terminal create requires workspace, cwd, and name")
	}
	args, err := hostArgs([]string{"terminal", "create"}, host)
	if err != nil {
		return nil, err
	}
	// Paseo 0.2.5's terminal create has no --workspace flag: a terminal joins
	// a workspace by cwd. WorkspaceID stays required above because the caller
	// must have created that workspace — its cwd is what req.Cwd carries.
	return append(args, "--cwd", req.Cwd, "--name", req.Name, "--json"), nil
}

func terminalKillArgs(host, terminalID string) ([]string, error) {
	if terminalID == "" || strings.ContainsAny(terminalID, " \t\r\n") || strings.HasPrefix(terminalID, "-") {
		return nil, fmt.Errorf("invalid terminal id %q", terminalID)
	}
	args, err := hostArgs([]string{"terminal", "kill"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, terminalID), nil
}

func terminalSendKeysArgs(host, terminalID string, keys []string) ([]string, error) {
	if strings.TrimSpace(terminalID) == "" || len(keys) == 0 {
		return nil, fmt.Errorf("terminal send-keys requires terminal and keys")
	}
	for _, key := range keys {
		if key == "" || strings.ContainsAny(key, "\x00\r\n") {
			return nil, fmt.Errorf("terminal key is empty or contains a line break")
		}
	}
	args, err := hostArgs([]string{"terminal", "send-keys"}, host)
	if err != nil {
		return nil, err
	}
	return append(append(args, terminalID), keys...), nil
}
