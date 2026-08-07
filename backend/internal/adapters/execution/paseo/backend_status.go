package paseo

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DaemonStatus is Paseo 0.2.5's /api/status shape.
type DaemonStatus struct {
	Status         string `json:"status"`
	ServerID       string `json:"serverId"`
	Hostname       string `json:"hostname"`
	Version        string `json:"version"`
	Listen         string `json:"listen"`
	DesktopManaged *bool  `json:"desktopManaged"`
}

// statusHTTPTimeout bounds the identity probe. It is deliberately shorter than
// the CLI timeout: this is one loopback-or-LAN GET, and a host that cannot
// answer it quickly is a host the observer should record as unreachable rather
// than wait on.
const statusHTTPTimeout = 10 * time.Second

// Status reads the targeted daemon's identity over HTTP, not the CLI.
//
// `paseo status` accepts --home and NOT --host: it reports on the daemon whose
// PASEO_HOME it is given, which is always the local one. Asking it about a
// remote host fails with "unknown option '--host' (Did you mean --home?)".
// spike/FINDINGS.md records this, and the first implementation here contradicted
// it — every remote host probed permanently offline as a result, which only
// surfaced when the observer was finally wired into the daemon.
//
// GET /api/status is the supported remote surface. It requires the bearer token
// when the daemon has a password; only /api/health is exempt.
func (c *Client) Status(ctx context.Context) (DaemonStatus, error) {
	endpoint, password, err := statusURL(c.host)
	if err != nil {
		return DaemonStatus{}, &Error{Kind: ErrorInvalidRequest, Message: err.Error(), Err: err}
	}

	reqCtx, cancel := context.WithTimeout(ctx, statusHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return DaemonStatus{}, &Error{Kind: ErrorInvalidRequest, Message: Redact(err.Error(), password), Err: err}
	}
	if password != "" {
		req.Header.Set("Authorization", "Bearer "+password)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Unreachable, never a zero value: the observer distinguishes "host is
		// down" from "host is up with nothing on it", and collapsing them would
		// let a network blip read as an empty host.
		return DaemonStatus{}, &Error{
			Kind:    ErrorNetwork,
			Message: Redact(fmt.Sprintf("status probe failed: %v", err), password),
			Err:     err,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return DaemonStatus{}, &Error{
			Kind:    ErrorAuth,
			Message: "status probe rejected: daemon requires a password and none matched",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return DaemonStatus{}, &Error{
			Kind:    ErrorNetwork,
			Message: fmt.Sprintf("status probe returned HTTP %d", resp.StatusCode),
		}
	}

	var status DaemonStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return DaemonStatus{}, &Error{
			Kind:    ErrorInvalidRequest,
			Message: Redact(fmt.Sprintf("decode status: %v", err), password),
			Err:     err,
		}
	}
	return status, nil
}

// statusURL turns a --host string into the /api/status URL plus the password to
// present, without ever putting the password in the URL it returns.
//
// Accepts the same forms --host does: host:port, and
// tcp://host:port?ssl=true&password=… . A colonless value is refused rather
// than defaulted, because Paseo silently falls back to the LOCAL daemon for
// one — which would make a remote host's probe report the operator's own
// machine as healthy.
func statusURL(host string) (endpoint, password string, err error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "", fmt.Errorf("host must not be empty")
	}

	scheme := "http"
	authority := host

	if strings.Contains(host, "://") {
		parsed, parseErr := url.Parse(host)
		if parseErr != nil {
			return "", "", fmt.Errorf("parse host: %w", parseErr)
		}
		authority = parsed.Host
		password = parsed.Query().Get("password")
		if strings.EqualFold(parsed.Query().Get("ssl"), "true") {
			scheme = "https"
		}
	}

	if _, _, splitErr := net.SplitHostPort(authority); splitErr != nil {
		return "", "", fmt.Errorf(
			"host %q needs host:port; Paseo resolves a colonless host to the LOCAL daemon", authority)
	}
	return scheme + "://" + authority + "/api/status", password, nil
}

// ListWorkspaces returns active workspaces so a completed create whose response
// was lost can be recovered by its AO-owned title without creating a second
// worktree.
func (c *Client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	args, err := workspaceListArgs(c.host)
	return runJSON[[]Workspace](ctx, c, args, err)
}

func workspaceListArgs(host string) ([]string, error) {
	args, err := hostArgs([]string{"workspace", "ls"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, "--json"), nil
}

// validateDaemonStatus refuses a status reply missing the fields AO routes on.
//
// desktopManaged is deliberately NOT required, and that is a downgrade forced
// by Paseo rather than a relaxation of the rule. Only two surfaces report
// daemon identity, and neither does both halves of what AO needs:
//
//	GET /api/status        works remotely, omits desktopManaged
//	paseo status --json    reports desktopManaged, takes --home and cannot
//	                       target a remote host at all
//
// So for a REMOTE host, desktop ownership is unknowable in 0.2.5. Requiring it
// made every remote host fail validation permanently, which is how this was
// found.
//
// The hazard it guarded — AO driving the operator's own desktop daemon — is
// better caught by comparing the probed ServerID against the local daemon's:
// that identifies the specific daemon rather than a class of them, and it works
// over the surface that actually reaches a remote host. See selfTargetingHost.
func validateDaemonStatus(status DaemonStatus) error {
	if status.ServerID == "" {
		return fmt.Errorf("paseo daemon status omitted server id")
	}
	if status.Version == "" {
		return fmt.Errorf("paseo daemon status omitted version")
	}
	return nil
}

// IsDesktopManaged reports desktop ownership when it is known.
//
// The bool is false when Paseo did not say, which is every remote probe. A
// caller must not read "not known" as "not desktop managed"; that conflation is
// exactly what the second return value exists to prevent.
func (s DaemonStatus) IsDesktopManaged() (managed, known bool) {
	if s.DesktopManaged == nil {
		return false, false
	}
	return *s.DesktopManaged, true
}
