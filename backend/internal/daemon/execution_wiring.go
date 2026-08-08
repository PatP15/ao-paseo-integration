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
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	paseoobserve "github.com/aoagents/agent-orchestrator/backend/internal/observe/paseo"
	"github.com/aoagents/agent-orchestrator/backend/internal/paseoevent"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	dispatchsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/dispatch"
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

// ResolveExecutionEventSource implements paseoevent.SourceResolver.
//
// The same host-scoped backend used for inspection owns both report reads:
// terminal capture is the primary, cursored transport and the full rendered
// transcript is the advisory fallback. Resolving through this cache keeps both
// reads pinned to the host identity and credential already used for inspection.
func (b *executionBackends) ResolveExecutionEventSource(hostID domain.ExecutionHostID) (paseoevent.Source, bool) {
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
) (<-chan struct{}, <-chan struct{}) {
	// One cache shared by both: the observer and the drain talk to the same
	// hosts, and a second cache would double the client count and the version
	// handshakes for no benefit.
	backends := newExecutionBackends(store, dataDir, logger)
	reports := paseoevent.NewIngestor(store, lcm, backends, logger)
	observer := paseoobserve.NewWithReports(store, lcm, backends, reports, logger)
	logger.Info("execution: paseo observer and dispatch drain starting",
		"secrets", filepath.Join(dataDir, "secrets"))
	return observer.Start(ctx), startDispatchWorker(ctx, store, backends, logger)
}

// ResolveExecutionBackend implements dispatch.BackendResolver.
func (b *executionBackends) ResolveExecutionBackend(hostID domain.ExecutionHostID) (ports.ExecutionBackend, bool) {
	client, ok := b.client(context.Background(), hostID)
	if !ok {
		return nil, false
	}
	return paseoexec.NewBackend(client, b.store), true
}

// dispatchDrainInterval paces the outbox. The queue is not latency-critical —
// a dispatch has already been committed durably by the time it lands here — and
// each delivery costs remote CLI invocations at roughly 0.9s each (spike
// FINDINGS S10), so draining faster mostly competes with the observer for the
// same host.
const dispatchDrainInterval = 3 * time.Second

// startDispatchWorker drains the execution-command outbox.
//
// Without this the whole dispatch path is a write-only queue: `ao remote
// dispatch` returns 201, the command lands as `pending`, and nothing ever
// delivers it. That is exactly what happened the first time this was run end to
// end — a session bound to a host with no workspace and no agent, and an outbox
// row at attempt 0 forever.
//
// DeliverOne is called in a loop until it reports nothing left, so a backlog
// clears in one tick rather than one row per tick.
func startDispatchWorker(
	ctx context.Context,
	store *sqlite.Store,
	backends *executionBackends,
	logger *slog.Logger,
) <-chan struct{} {
	done := make(chan struct{})
	// The report nonce and launch contract must be durable before the remote
	// agent starts. Without the brief writer the ingestor is wired but has no
	// authority for deciding which frames belong to this launch, so it safely
	// rejects every report.
	worker := dispatchsvc.NewWorkerWithBriefs(store, backends, paseoevent.NewBriefs(store))
	go func() {
		defer close(done)
		ticker := time.NewTicker(dispatchDrainInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// A panic here would otherwise kill the daemon: a goroutine
				// panic takes the process with it, so one malformed reply from
				// one host would stop AO entirely — including every local
				// session that has nothing to do with remote execution. A bad
				// host must degrade to "this host is not working", never to
				// "AO is not running".
				drainOnce(ctx, worker, logger)
			}
		}
	}()
	return done
}

// drainOnce delivers until the outbox is empty, converting a panic into a log
// line. DeliverOne calls into adapter code that parses remote JSON; a nil field
// there must not be fatal to the daemon.
func drainOnce(ctx context.Context, worker *dispatchsvc.Worker, logger *slog.Logger) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("dispatch: delivery panicked; drain continues next tick", "panic", r)
		}
	}()
	for {
		delivered, err := worker.DeliverOne(ctx)
		if err != nil {
			// Delivery failures are the worker's own business: it records the
			// attempt, backs off, and retries. Returning would stop the drain
			// for every other session too.
			logger.Debug("dispatch: delivery attempt failed", "err", err)
			return
		}
		if !delivered {
			return
		}
	}
}

// newHostProber builds the on-demand probe behind POST
// /execution/hosts/{hostId}/probe.
//
// It deliberately does NOT go through the executionBackends client cache: the
// cache refuses disabled hosts, and "test the connection to a host I have not
// enabled yet" is precisely what a manual probe is for. A transient client is
// constructed per call — a probe is one HTTP GET, so there is nothing worth
// caching.
//
// The recorded outcome goes through paseoobserve.ProbeHost, the same rules the
// observer's tick applies, so a manual probe and a scheduled one can never
// disagree about reachability or server-identity drift. On a reachable probe
// the G5 self-target guard runs as well; a positive match is recorded as a
// failed probe and returned as the same refusal registration gives.
func newHostProber(
	store *sqlite.Store,
	dataDir string,
	logger *slog.Logger,
	selfTargetGuard func(context.Context, domain.ExecutionHost) error,
) func(context.Context, domain.ExecutionHost) error {
	secrets := newSecretResolver(dataDir)
	return func(ctx context.Context, host domain.ExecutionHost) error {
		now := time.Now().UTC()
		recordFailure := func(reason string) error {
			return store.RecordExecutionHostProbe(ctx, domain.ExecutionHostProbe{
				HostID: host.ID, Reachable: false, Error: reason, ObservedAt: now,
			})
		}

		password, err := secrets.Resolve(host.EndpointSecretRef)
		if err != nil {
			// A missing credential is a probe failure the operator can act on,
			// not an internal error: record it and let the view carry it.
			return recordFailure("cannot resolve host credential: " + err.Error())
		}
		endpoint := host.Endpoint
		if password != "" {
			endpoint = fmt.Sprintf("tcp://%s?password=%s", host.Endpoint, password)
		}
		client, err := paseoexec.NewClient(ctx, endpoint, paseoexec.CLIRunner{Timeout: 30 * time.Second})
		if err != nil {
			return recordFailure(paseoexec.Redact(err.Error(), password))
		}

		probe, err := paseoobserve.ProbeHost(ctx, store, paseoexec.NewBackend(client, store), host, now, logger)
		if err != nil {
			return err
		}
		if !probe.Reachable || selfTargetGuard == nil {
			return nil
		}
		if guardErr := selfTargetGuard(ctx, host); guardErr != nil {
			// The daemon answered, but it is the operator's own: reachable in
			// the network sense and unusable in every other. Record the refusal
			// so the registry view says why, then surface it to the caller.
			if recordErr := recordFailure(guardErr.Error()); recordErr != nil {
				return recordErr
			}
			return guardErr
		}
		return nil
	}
}

// localPaseoDaemon is the operator's own Paseo daemon. G5 compares a host being
// registered against this identity. It is the documented default listen
// address; a non-default local daemon is not covered, which is acceptable
// because the runtime server_id-drift guard still protects every session.
const localPaseoDaemon = "127.0.0.1:6767"

// newSelfTargetGuard builds the G5 registration check: refuse a host whose
// daemon serverId equals the operator's own local daemon.
//
// It is FAIL-OPEN by design. If either the candidate host or the local daemon
// cannot be probed — offline, password-protected, unsupported version — the
// guard allows the registration and logs why. Blocking on a probe failure would
// stop an operator registering a legitimately-offline worker, and the runtime
// guardHost still refuses a server_id that later resolves to something else.
// The guard only ever REFUSES on a positive identity match.
func newSelfTargetGuard(dataDir string, logger *slog.Logger) func(context.Context, domain.ExecutionHost) error {
	secrets := newSecretResolver(dataDir)

	// serverID probes one daemon and returns (id, true) only on a clean,
	// identified answer. Every failure — unresolved secret, unreachable daemon,
	// unsupported version, empty id — collapses to (─, false), so the guard's
	// single decision below is "both sides identified AND equal", never a
	// tangle of error branches that each have to remember to fail open.
	serverID := func(ctx context.Context, endpoint, secretRef, role string) (string, bool) {
		password, err := secrets.Resolve(secretRef)
		if err != nil {
			logger.Debug("self-target guard: cannot resolve secret; skipping", "role", role, "err", err)
			return "", false
		}
		target := endpoint
		if password != "" {
			target = fmt.Sprintf("tcp://%s?password=%s", endpoint, password)
		}
		client, err := paseoexec.NewClient(ctx, target, paseoexec.CLIRunner{Timeout: 10 * time.Second})
		if err != nil {
			logger.Debug("self-target guard: probe failed; skipping", "role", role,
				"err", paseoexec.Redact(err.Error(), password))
			return "", false
		}
		status, err := client.Status(ctx)
		if err != nil {
			logger.Debug("self-target guard: status probe failed; skipping", "role", role,
				"err", paseoexec.Redact(err.Error(), password))
			return "", false
		}
		if status.ServerID == "" {
			return "", false
		}
		return status.ServerID, true
	}

	return func(ctx context.Context, host domain.ExecutionHost) error {
		local, localOK := serverID(ctx, localPaseoDaemon, "", "local")
		candidate, candidateOK := serverID(ctx, host.Endpoint, host.EndpointSecretRef, "candidate")
		// Refuse only on a positive match; any unidentified side fails open,
		// because the runtime server_id-drift guard still covers what this misses.
		if localOK && candidateOK && local == candidate {
			return apierr.Conflict(
				"HOST_IS_SELF",
				"this endpoint resolves to the operator's own Paseo daemon (same serverId as the "+
					"local daemon); AO must not drive its own daemon as a remote worker",
				nil)
		}
		return nil
	}
}
