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
	// MaintenanceHome is the worker's home directory as the maintenance
	// channel learned it from a run's done event. Channel-owned, empty until
	// the first successful run; maintenance workspaces are created here, with
	// "/" as the only fallback AO can name unaided.
	MaintenanceHome string
	CreatedAt       time.Time
	UpdatedAt       time.Time
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

// ExecutionZoneID identifies a named execution zone.
type ExecutionZoneID string

// ExecutionZone is an operator-named grouping that carries AUTONOMY policy.
//
// It deliberately carries no isolation setting. Isolation is a property of a
// host (ExecutionHost.Isolated), because whether one agent can read another's
// transcript depends on the operating system the two share, not on any label AO
// applies. A zone can say "do not auto-dispatch"; only a uid or a machine
// boundary can say "cannot read".
//
// Replaces ExecutionTrustZone, whose hobby|work|mixed enum fused those two
// axes and so made every "work" project look like it needed its own uid.
type ExecutionZone struct {
	ID                ExecutionZoneID
	Name              string
	Description       string
	AutoDispatch      bool
	MaxRepairAttempts int
	MayPushBranch     bool
	MayCreateDraftPR  bool
	MaxRuntimeMinutes int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Isolation reports whether a host is asserted to share no uid with any other
// zone's hosts.
//
// OPERATOR-ASSERTED AND UNVERIFIABLE. No Paseo CLI surface reports the uid a
// remote daemon runs as, so AO records the claim and displays it as a claim.
// The value of writing it down is that a wrong assumption becomes visible
// instead of implicit.
type Isolation struct {
	Isolated bool
	// Note records HOW isolation is achieved — "separate OS user", "dedicated
	// machine", "shares uid with hobby zone" — so a reviewer can judge the
	// claim rather than trust the boolean.
	Note string
}
