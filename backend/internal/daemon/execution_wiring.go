package daemon

// Composition root for remote execution.
//
// Every piece below existed and was tested before this file did — the ports,
// the Paseo adapter, the observer, the dispatch outbox — but nothing
// constructed them, so a registered host stayed permanently "offline" and a
// dispatched command was never drained. Each PR was verified against its own
// acceptance criteria and each one honestly passed; nothing asserted that the
// daemon assembles them. That gap is only visible end to end, which is where it
// was eventually found.
//
// Backends are resolved PER HOST and cached, because each host is a different
// remote daemon reached through a different endpoint and credential. Resolution
// is lazy for the same reason startSCMObserver resolves GitHub tokens lazily:
// an unreachable host must not block daemon readiness.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	paseoexec "github.com/aoagents/agent-orchestrator/backend/internal/adapters/execution/paseo"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	paseoobserve "github.com/aoagents/agent-orchestrator/backend/internal/observe/paseo"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// secretResolver turns an ExecutionHost.EndpointSecretRef into the credential
// it names.
//
// The API deliberately refuses a credential embedded in an endpoint and demands
// a reference instead, so that a password never lands in a task row, a log
// line, or an error string. That refusal was implemented; the resolving half
// was not, which left --secret-ref naming something nothing could look up.
//
// Files under a 0700 directory are the v1 store: no new dependency, and the
// blast radius is the same uid that already owns ~/.ao. This is NOT a security
// boundary against a local agent — nothing on a shared uid is, which is the
// point SECURITY.md §1 makes — it only keeps credentials out of AO's own
// durable rows and telemetry.
type secretResolver struct {
	dir string
}

func newSecretResolver(dataDir string) secretResolver {
	return secretResolver{dir: filepath.Join(dataDir, "secrets")}
}

// Resolve returns the credential named by ref, or "" when ref is empty.
//
// An empty ref is legitimate: a host on a loopback daemon with no password set
// needs no credential. A ref that names a missing file is an error, because
// silently proceeding without a credential would turn an authentication
// mistake into a confusing connection failure much later.
func (r secretResolver) Resolve(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	// Refuse separators rather than sanitising them: a ref is a name, and
	// anything path-shaped is a caller bug worth surfacing, not repairing.
	if strings.ContainsAny(ref, `/\`) || strings.Contains(ref, "..") {
		return "", fmt.Errorf("secret ref %q must be a bare name, not a path", ref)
	}
	raw, err := os.ReadFile(filepath.Join(r.dir, ref))
	if err != nil {
		return "", fmt.Errorf("resolve secret ref %q: %w", ref, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// executionBackends resolves and caches one Paseo client per host.
type executionBackends struct {
	store    *sqlite.Store
	secrets  secretResolver
	logger   *slog.Logger
	mu       sync.Mutex
	byHostID map[domain.ExecutionHostID]*paseoexec.Client
}

func newExecutionBackends(store *sqlite.Store, dataDir string, logger *slog.Logger) *executionBackends {
	return &executionBackends{
		store:    store,
		secrets:  newSecretResolver(dataDir),
		logger:   logger,
		byHostID: make(map[domain.ExecutionHostID]*paseoexec.Client),
	}
}

// client returns the cached Paseo client for a host, constructing it on first
// use. A construction failure is logged and reported to the caller as "not
// available" rather than cached, so a host that was merely down at startup
// recovers on a later tick instead of staying broken until a restart.
func (b *executionBackends) client(ctx context.Context, hostID domain.ExecutionHostID) (*paseoexec.Client, bool) {
	b.mu.Lock()
	cached, ok := b.byHostID[hostID]
	b.mu.Unlock()
	if ok {
		return cached, true
	}

	host, _, found, err := b.store.GetExecutionHost(ctx, hostID)
	if err != nil || !found {
		return nil, false
	}
	if !host.Enabled {
		return nil, false
	}
	if host.BackendType != domain.ExecutionBackendPaseo {
		return nil, false
	}

	password, err := b.secrets.Resolve(host.EndpointSecretRef)
	if err != nil {
		b.logger.Warn("execution: cannot resolve host credential; host stays unreachable",
			"host", hostID, "ref", host.EndpointSecretRef, "err", err)
		return nil, false
	}

	endpoint := host.Endpoint
	if password != "" {
		// Carry the credential in the endpoint URI rather than the process
		// environment: PASEO_PASSWORD in AO's own env would be inherited by
		// every child, and paseo-env.ts strips only five keys.
		endpoint = fmt.Sprintf("tcp://%s?password=%s", host.Endpoint, password)
	}

	client, err := paseoexec.NewClient(ctx, endpoint, paseoexec.CLIRunner{Timeout: 30 * time.Second})
	if err != nil {
		b.logger.Warn("execution: host client unavailable",
			"host", hostID, "endpoint", host.Endpoint, "err", paseoexec.Redact(err.Error()))
		return nil, false
	}

	b.mu.Lock()
	b.byHostID[hostID] = client
	b.mu.Unlock()
	return client, true
}

// ResolveExecutionObserver implements paseoobserve.ObserverResolver.
//
// Returns a Backend, not the bare Client: Backend is what implements
// ports.ExecutionObserver, because it also holds the store it needs to confirm
// the host is registered before reporting anything about it. The Client alone
// speaks agent ids and adapter types and knows nothing about hosts.
func (b *executionBackends) ResolveExecutionObserver(hostID domain.ExecutionHostID) (ports.ExecutionObserver, bool) {
	client, ok := b.client(context.Background(), hostID)
	if !ok {
		return nil, false
	}
	return paseoexec.NewBackend(client, b.store), true
}

// startExecutionObserver wires the Paseo observer into daemon startup.
//
// Mirrors startSCMObserver: a host that cannot be reached does not fail
// startup, it simply reports a failed probe, and AO records that as an
// observation about the HOST rather than as a fact about its sessions.
func startExecutionObserver(
	ctx context.Context,
	store *sqlite.Store,
	lcm *lifecycle.Manager,
	dataDir string,
	logger *slog.Logger,
) <-chan struct{} {
	backends := newExecutionBackends(store, dataDir, logger)
	observer := paseoobserve.New(store, lcm, backends, logger)
	logger.Info("execution: paseo observer starting", "secrets", filepath.Join(dataDir, "secrets"))
	return observer.Start(ctx)
}
