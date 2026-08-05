package domain

import "time"

// ExecutionHostTransport identifies how AO reaches an execution host.
type ExecutionHostTransport string

// Execution host transport values.
const (
	ExecutionTransportLocal      ExecutionHostTransport = "local"
	ExecutionTransportTailscale  ExecutionHostTransport = "tailscale"
	ExecutionTransportLAN        ExecutionHostTransport = "lan"
	ExecutionTransportPaseoRelay ExecutionHostTransport = "paseo_relay"
)

// ExecutionTrustZone describes which trust boundary a host belongs to.
type ExecutionTrustZone string

// Execution host trust-zone values.
const (
	ExecutionTrustZoneHobby ExecutionTrustZone = "hobby"
	ExecutionTrustZoneWork  ExecutionTrustZone = "work"
	ExecutionTrustZoneMixed ExecutionTrustZone = "mixed"
)

// ExecutionHost is the durable registry row for a remote execution target.
type ExecutionHost struct {
	ID                    ExecutionHostID
	Name                  string
	BackendType           ExecutionBackendType
	Transport             ExecutionHostTransport
	Endpoint              string
	EndpointSecretRef     string
	TrustZone             ExecutionTrustZone
	Enabled               bool
	MaxConcurrentSessions int
	ServerID              string
	PaseoVersion          string
	RequiresNoMCP         bool
	RequiresNoRelay       bool
	LastSuccessfulProbeAt time.Time
	LastFailedProbeAt     time.Time
	LastProbeError        string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ExecutionHostCapability is one routable capability exposed by a host.
type ExecutionHostCapability struct {
	HostID     ExecutionHostID
	Capability string
}

// ExecutionHostStatus is the live status probe AO records for one host.
type ExecutionHostStatus struct {
	HostID         ExecutionHostID
	Reachable      bool
	DesktopManaged bool
	ServerID       string
	Version        string
	MCPEnabled     bool
	RelayEnabled   bool
	ObservedAt     time.Time
}
