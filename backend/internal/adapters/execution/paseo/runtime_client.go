package paseo

import (
	"context"
	"fmt"
	"strings"
)

// Logs returns Paseo's rendered transcript. Paseo 0.2.5 exposes no JSON or
// cursor on this command, so callers must treat it as a full snapshot.
func (c *Client) Logs(ctx context.Context, agentID string) (string, error) {
	args, err := logsArgs(c.host, agentID)
	if err != nil {
		return "", &Error{Kind: ErrorInvalidRequest, Message: err.Error(), Err: err}
	}
	result, err := c.runner.Run(ctx, args)
	if err != nil {
		return "", commandError(err, result, c.host)
	}
	return redact(string(result.stdout), c.host), nil
}

// Send delivers one message to an explicitly identified agent.
func (c *Client) Send(ctx context.Context, agentID, message string) error {
	args, err := sendArgs(c.host, agentID, message)
	return c.runNoOutput(ctx, args, err)
}

func logsArgs(host, agentID string) ([]string, error) {
	if err := validatePositionalID("agent id", agentID); err != nil {
		return nil, err
	}
	args, err := hostArgs([]string{"logs"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, agentID), nil
}

func sendArgs(host, agentID, message string) ([]string, error) {
	if err := validatePositionalID("agent id", agentID); err != nil {
		return nil, err
	}
	if message == "" || strings.ContainsAny(message, "\x00\r\n") {
		return nil, fmt.Errorf("message is empty or contains a line break")
	}
	args, err := hostArgs([]string{"send"}, host)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(message, "-") {
		args = append(args, "--")
	}
	return append(args, agentID, message), nil
}
