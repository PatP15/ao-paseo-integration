package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	paseoexec "github.com/aoagents/agent-orchestrator/backend/internal/adapters/execution/paseo"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestExecutionClientEndpointEncodesPassword(t *testing.T) {
	want := `a&b #+%`
	endpoint := executionClientEndpoint("worker.example:6807", want)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	if got := parsed.Query().Get("password"); got != want {
		t.Fatalf("decoded password = %q, want %q (endpoint %q)", got, want, endpoint)
	}
	if parsed.Host != "worker.example:6807" {
		t.Fatalf("host = %q, want worker.example:6807", parsed.Host)
	}
}

func TestExecutionBackendsCacheHonorsCredentialRotationAndDisable(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host := domain.ExecutionHost{
		ID: "worker", Name: "Worker", BackendType: domain.ExecutionBackendPaseo,
		Transport: domain.ExecutionTransportLAN, Endpoint: "127.0.0.1:1", EndpointSecretRef: "worker-pw",
		TrustZone: domain.ExecutionTrustZoneHobby, Enabled: true, MaxConcurrentSessions: 1,
		RequiresNoMCP: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.UpsertExecutionHost(ctx, host, nil); err != nil {
		t.Fatalf("upsert host: %v", err)
	}
	secretDir := filepath.Join(dataDir, "secrets")
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "worker-pw"), []byte("old-password"), 0o600); err != nil {
		t.Fatal(err)
	}

	backends := newExecutionBackends(store, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cached := new(paseoexec.Client)
	backends.byHostID[host.ID] = cachedExecutionClient{
		client: cached, fingerprint: executionClientFingerprint(host.Endpoint, "old-password"),
	}
	if got, ok := backends.client(ctx, host.ID); !ok || got != cached {
		t.Fatalf("unchanged config did not reuse cached client: got %p, ok=%v", got, ok)
	}

	if err := os.WriteFile(filepath.Join(secretDir, "worker-pw"), []byte("rotated-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Client construction validates the local Paseo CLI, not the remote
	// endpoint. Rotation must therefore return a different, successfully built
	// client rather than the cached pointer carrying the old credential.
	if got, ok := backends.client(ctx, host.ID); !ok || got == nil || got == cached {
		t.Fatalf("rotated credential reused stale client: got %p, cached %p, ok=%v", got, cached, ok)
	}

	host.Enabled = false
	host.UpdatedAt = time.Now()
	if err := store.UpsertExecutionHost(ctx, host, nil); err != nil {
		t.Fatalf("disable host: %v", err)
	}
	backends.byHostID[host.ID] = cachedExecutionClient{
		client: cached, fingerprint: executionClientFingerprint(host.Endpoint, "rotated-password"),
	}
	if got, ok := backends.client(ctx, host.ID); ok || got != nil {
		t.Fatalf("disabled host returned cached client: got %p, ok=%v", got, ok)
	}
}
