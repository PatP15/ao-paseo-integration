package paseo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Workspace is Paseo 0.2.5's workspace JSON shape.
type Workspace struct {
	WorkspaceID string `json:"workspaceId"`
	Project     string `json:"project"`
	Name        string `json:"name"`
	Isolation   string `json:"isolation"`
	Cwd         string `json:"cwd"`
}

// Agent is Paseo 0.2.5's agent-list JSON shape.
type Agent struct {
	ID       string `json:"id"`
	ShortID  string `json:"shortId"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Thinking string `json:"thinking"`
	Status   string `json:"status"`
	Cwd      string `json:"cwd"`
	Created  string `json:"created"`
}

// RunResult is Paseo 0.2.5's detached-run JSON shape.
type RunResult struct {
	AgentID  string `json:"agentId"`
	Status   string `json:"status"`
	Provider string `json:"provider"`
	Cwd      string `json:"cwd"`
	Title    string `json:"title"`
}

// AgentDetail is Paseo 0.2.5's inspect JSON shape.
type AgentDetail struct {
	ID                 string              `json:"Id"`
	Name               string              `json:"Name"`
	Provider           string              `json:"Provider"`
	Model              string              `json:"Model"`
	Thinking           string              `json:"Thinking"`
	Status             string              `json:"Status"`
	Archived           bool                `json:"Archived"`
	ArchivedAt         *time.Time          `json:"ArchivedAt"`
	Mode               string              `json:"Mode"`
	Cwd                string              `json:"Cwd"`
	CreatedAt          time.Time           `json:"CreatedAt"`
	UpdatedAt          time.Time           `json:"UpdatedAt"`
	LastUsage          Usage               `json:"LastUsage"`
	Capabilities       Capabilities        `json:"Capabilities"`
	AvailableModes     []Mode              `json:"AvailableModes"`
	PendingPermissions []PendingPermission `json:"PendingPermissions"`
	Worktree           string              `json:"Worktree"`
	ParentAgentID      *string             `json:"ParentAgentId"`
}

// Usage is the token and cost section of an inspected agent.
type Usage struct {
	InputTokens  int64   `json:"InputTokens"`
	OutputTokens int64   `json:"OutputTokens"`
	CachedTokens int64   `json:"CachedTokens"`
	CostUSD      float64 `json:"CostUsd"`
}

// Capabilities is the provider capability section of an inspected agent.
type Capabilities struct {
	Streaming    bool `json:"Streaming"`
	Persistence  bool `json:"Persistence"`
	DynamicModes bool `json:"DynamicModes"`
	MCPServers   bool `json:"McpServers"`
}

// Mode is one provider mode advertised by an inspected agent.
type Mode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// PendingPermission is one full, unresolved permission request.
type PendingPermission struct {
	ID       string `json:"Id"`
	ToolName string `json:"ToolName"`
	Reason   string `json:"Reason"`
}

// Provider is Paseo 0.2.5's provider-list JSON shape.
type Provider struct {
	Provider    string `json:"provider"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Enabled     string `json:"enabled"`
	DefaultMode string `json:"defaultMode"`
	Modes       string `json:"modes"`
}

// TerminalCapture is a cursored slice of hard-wrapped terminal screen lines.
type TerminalCapture struct {
	TerminalID string   `json:"terminalId"`
	Lines      []string `json:"lines"`
	TotalLines int      `json:"totalLines"`
}

func decodeStrict[T any](data []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode Paseo JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return value, fmt.Errorf("decode Paseo JSON: multiple values")
		}
		return value, fmt.Errorf("decode Paseo JSON: %w", err)
	}
	return value, nil
}
