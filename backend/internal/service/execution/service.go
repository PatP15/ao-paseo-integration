// Package execution serves the control-plane read and write model for remote
// execution: the host registry and the human inbox of questions and permission
// requests.
//
// It is the API's counterpart to service/dispatch, which owns launching work.
// Everything here is either a projection of durable facts or a decision that is
// committed to the outbox before any host is contacted; the package makes no
// remote calls of its own and imports no backend adapter.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// MaxAnswerLen bounds a human answer. It matches the session send-message limit:
// an answer is delivered as a message, so a longer one could not be sent anyway.
const MaxAnswerLen = 4096

// maxHostConcurrency caps sessions per host. The ceiling is an observation
// budget, not a resource one: each poll of the Paseo CLI costs roughly a second
// because the binary re-execs a helper, so a host tracking many sessions cannot
// finish a sweep inside its own tick.
const maxHostConcurrency = 64

// Store is the durable state this service reads and writes.
type Store interface {
	ListExecutionHosts(context.Context) ([]domain.ExecutionHost, error)
	GetExecutionHost(context.Context, domain.ExecutionHostID) (domain.ExecutionHost, []string, bool, error)
	UpsertExecutionHost(context.Context, domain.ExecutionHost, []string) error
	ListActiveSessionExecutionBindingsByHost(context.Context, domain.ExecutionHostID) ([]domain.SessionExecutionBinding, error)
	ListOpenExecutionQuestions(context.Context) ([]domain.ExecutionInboxQuestion, error)
	GetExecutionQuestion(context.Context, string) (domain.ExecutionInboxQuestion, bool, error)
	ResolveExecutionQuestion(context.Context, domain.ExecutionQuestionResolution) (domain.ExecutionCommand, error)
	UpsertProjectHostBinding(context.Context, domain.ProjectHostBinding) error
	ListProjectHostBindings(context.Context, domain.ProjectID) ([]domain.ProjectHostBinding, error)
	ListAllProjectHostBindings(context.Context) ([]domain.ProjectHostBinding, error)
	GetExecutionCommand(context.Context, string) (domain.ExecutionCommand, bool, error)
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	ListSessionExecutionEvents(context.Context, domain.SessionID, string, int) ([]domain.ExecutionEventRecord, error)
	ReplaceExecutionHostSkills(context.Context, domain.ExecutionHostID, []domain.ExecutionHostSkill, time.Time) error
	ListExecutionHostSkills(context.Context, domain.ExecutionHostID) ([]domain.ExecutionHostSkill, error)
	UpsertExecutionHostPrefs(context.Context, domain.ExecutionHostPrefs) error
	GetExecutionHostPrefs(context.Context, domain.ExecutionHostID) (domain.ExecutionHostPrefs, bool, error)
	UpsertExecutionHostInstructions(context.Context, domain.ExecutionHostPrefs) error
	GetExecutionHostInstructions(context.Context, domain.ExecutionHostID) (domain.ExecutionHostPrefs, bool, error)
	GetProject(context.Context, string) (domain.ProjectRecord, bool, error)
}

// Service answers host-registry and inbox requests for the HTTP API.
type Service struct {
	store Store
	now   func() time.Time
	newID func() string
	// selfTargetGuard, when set, refuses a host whose daemon identity matches
	// the operator's own. Optional and injected by the daemon because it needs
	// to probe over the network; nil in tests and when no local daemon answers.
	selfTargetGuard func(ctx context.Context, host domain.ExecutionHost) error
	// hostProber, when set, probes one host on demand and records the outcome
	// through the same rules the observer's tick applies. Injected by the
	// daemon for the same reason as selfTargetGuard: it talks to the network,
	// and this package deliberately makes no remote calls of its own.
	hostProber func(ctx context.Context, host domain.ExecutionHost) error
	// providerDiscovery, when set, asks one host's daemon what it can launch.
	// Injected for the same reason as the two above.
	providerDiscovery func(ctx context.Context, host domain.ExecutionHost) ([]domain.ExecutionHostProvider, error)
	// questionResolved, when set, closes the advisory notification announcing
	// a question once a human has answered or decided it. Failures stay inside
	// the hook: a notification is never load-bearing for the decision itself.
	questionResolved func(ctx context.Context, sessionID domain.SessionID, questionID string)
	// scheduleReader and scheduleDeleter, when set, read and delete recurring
	// schedules on one host's daemon. Injected like providerDiscovery: they
	// talk to the network and this package does not.
	scheduleReader  func(ctx context.Context, host domain.ExecutionHost) ([]domain.ExecutionHostSchedule, error)
	scheduleDeleter func(ctx context.Context, host domain.ExecutionHost, scheduleID string) error
	// maintenance, when set, is the live host maintenance channel (U9).
	maintenance MaintenanceChannel
	// instructions, when set, is the U9a half of the channel: instruction
	// files, repo drift, and skill transfer.
	instructions InstructionsChannel
	// defaultActor, when set, names the identity recorded for answers and
	// permission decisions whose caller supplied none. Explicit names win.
	defaultActor func() string

	providerCacheMu sync.Mutex
	providerCache   map[domain.ExecutionHostID]providerCacheEntry
}

type providerCacheEntry struct {
	providers []domain.ExecutionHostProvider
	fetchedAt time.Time
	host      providerCacheHost
}

// providerCacheHost is the registry identity whose discovery result was
// cached. Host IDs are stable across edits, so keying by ID alone can serve a
// result learned from a different endpoint (or before the host was disabled).
// Probe timestamps are deliberately excluded: a routine health tick must not
// evict otherwise valid discovery data.
type providerCacheHost struct {
	backendType       domain.ExecutionBackendType
	endpoint          string
	endpointSecretRef string
	enabled           bool
	serverID          string
	paseoVersion      string
}

// providerCacheTTL bounds how stale a served discovery result can be. Each
// discovery costs one CLI invocation per provider at ~0.9s each, so a settings
// panel that re-renders must not re-pay that; 30s is short enough that an
// operator toggling a provider on the host sees it on the next look.
const providerCacheTTL = 30 * time.Second

// New constructs the service.
func New(store Store) *Service {
	return &Service{store: store, now: time.Now, newID: uuid.NewString}
}

func newService(store Store, now func() time.Time, newID func() string) *Service {
	return &Service{store: store, now: now, newID: newID}
}

// SetSelfTargetGuard installs the registration-time check that refuses a host
// pointed at the operator's own daemon (gap G5). It is set after construction
// so New stays a pure store wrapper and tests need not supply a prober.
//
// The runtime guardHost already refuses a host whose server_id has DRIFTED, but
// that only fires once a host is registered and probed. This catches the
// mistake at the one moment AO can see both identities at once: registration.
func (s *Service) SetSelfTargetGuard(guard func(ctx context.Context, host domain.ExecutionHost) error) {
	s.selfTargetGuard = guard
}

// SetHostProber installs the on-demand probe used by POST
// /execution/hosts/{hostId}/probe. The prober performs the network probe AND
// records its outcome (probe rows, identity orphans); the service only reloads
// the host afterwards so the response reflects what was just recorded.
func (s *Service) SetHostProber(prober func(ctx context.Context, host domain.ExecutionHost) error) {
	s.hostProber = prober
}

// SetProviderDiscovery installs the network-facing provider discovery behind
// GET /execution/hosts/{hostId}/providers and dispatch settings validation.
func (s *Service) SetProviderDiscovery(discovery func(ctx context.Context, host domain.ExecutionHost) ([]domain.ExecutionHostProvider, error)) {
	s.providerDiscovery = discovery
}

// HostProviders returns what one registered host can launch, served from a
// brief cache. The discovery function's own errors pass through typed: an
// unreachable host is a fact the caller can present, not a server fault.
func (s *Service) HostProviders(ctx context.Context, id domain.ExecutionHostID) ([]domain.ExecutionHostProvider, error) {
	return s.hostProviders(ctx, id, true)
}

func (s *Service) hostProviders(
	ctx context.Context, id domain.ExecutionHostID, allowCache bool,
) ([]domain.ExecutionHostProvider, error) {
	id = domain.ExecutionHostID(strings.TrimSpace(string(id)))
	if id == "" {
		return nil, apierr.Invalid("HOST_ID_REQUIRED", "hostId is required", nil)
	}
	host, _, found, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get execution host %s: %w", id, err)
	}
	if !found {
		return nil, apierr.NotFound("HOST_NOT_FOUND", "host "+string(id)+" is not registered")
	}
	if s.providerDiscovery == nil {
		return nil, apierr.Internal("PROVIDER_DISCOVERY_UNAVAILABLE",
			"this daemon was started without execution provider discovery wiring")
	}

	cacheHost := providerCacheIdentity(host)
	if allowCache {
		s.providerCacheMu.Lock()
		entry, cached := s.providerCache[id]
		s.providerCacheMu.Unlock()
		if cached && entry.host == cacheHost && s.now().Sub(entry.fetchedAt) < providerCacheTTL {
			return entry.providers, nil
		}
	}

	providers, err := s.providerDiscovery(ctx, host)
	if err != nil {
		return nil, err
	}
	s.providerCacheMu.Lock()
	if s.providerCache == nil {
		s.providerCache = map[domain.ExecutionHostID]providerCacheEntry{}
	}
	s.providerCache[id] = providerCacheEntry{providers: providers, fetchedAt: s.now(), host: cacheHost}
	s.providerCacheMu.Unlock()
	return providers, nil
}

func providerCacheIdentity(host domain.ExecutionHost) providerCacheHost {
	return providerCacheHost{
		backendType: host.BackendType, endpoint: host.Endpoint,
		endpointSecretRef: host.EndpointSecretRef, enabled: host.Enabled,
		serverID: host.ServerID, paseoVersion: host.PaseoVersion,
	}
}

// ValidateDispatchSettings checks a thinking-option id against what discovery
// reports for the selected host, provider, and model. It refuses an id
// discovery did not return, naming the valid set so the caller can correct the
// request rather than guess.
func (s *Service) ValidateDispatchSettings(ctx context.Context, hostID domain.ExecutionHostID, provider, model, thinkingOptionID string) error {
	// Dispatch is a write boundary, so its validation must be live. The brief
	// UI cache is intentionally insufficient here: provider availability,
	// models, and mode vocabularies can change while a dialog is open.
	providers, err := s.hostProviders(ctx, hostID, false)
	if err != nil {
		return err
	}
	var match *domain.ExecutionHostProvider
	for i := range providers {
		if providers[i].Provider == provider {
			match = &providers[i]
			break
		}
	}
	if match == nil {
		return apierr.Invalid("PROVIDER_UNKNOWN",
			fmt.Sprintf("host %s does not report provider %q", hostID, provider),
			map[string]any{"providers": providerNames(providers)})
	}
	// No model requested means the provider's default model, whose thinking
	// vocabulary AO cannot see through discovery; requiring the model keeps
	// "validated" meaning validated instead of assumed.
	if strings.TrimSpace(model) == "" {
		return apierr.Invalid("MODEL_REQUIRED_FOR_SETTINGS",
			"model is required when settings.thinkingOptionId is set: thinking options are per-model", nil)
	}
	for _, candidate := range match.Models {
		if candidate.ID != model {
			continue
		}
		for _, valid := range candidate.ThinkingOptionIDs {
			if valid == thinkingOptionID {
				return nil
			}
		}
		return apierr.Invalid("THINKING_OPTION_UNKNOWN",
			fmt.Sprintf("model %s on host %s does not report thinking option %q", model, hostID, thinkingOptionID),
			map[string]any{"validThinkingOptionIds": append([]string{}, candidate.ThinkingOptionIDs...)})
	}
	return apierr.Invalid("MODEL_UNKNOWN",
		fmt.Sprintf("provider %s on host %s does not report model %q", provider, hostID, model),
		map[string]any{"validModelIds": modelIDs(match.Models)})
}

func providerNames(providers []domain.ExecutionHostProvider) []string {
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, provider.Provider)
	}
	return names
}

func modelIDs(models []domain.ExecutionProviderModel) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

// MaintenanceChannel is the live worker-side channel: skills inventory and the
// preferences file, read and written through the host's own AO-owned binary.
type MaintenanceChannel interface {
	Inventory(ctx context.Context, host domain.ExecutionHost) ([]domain.ExecutionHostSkill, error)
	ReadPrefs(ctx context.Context, host domain.ExecutionHost) (domain.ExecutionHostPrefs, error)
	// WritePrefs replaces the file guarded by expectSHA256 (hex digest of the
	// content currently on disk; a missing file hashes the empty string) and
	// returns the worker's confirm-read of what it committed.
	WritePrefs(ctx context.Context, host domain.ExecutionHost, content []byte, expectSHA256 string) (domain.ExecutionHostPrefs, error)
}

// SetMaintenanceChannel installs the network-facing maintenance channel.
func (s *Service) SetMaintenanceChannel(channel MaintenanceChannel) {
	s.maintenance = channel
}

// HostInventory is the persisted maintenance view of one host: the cached
// skills inventory and the confirmed preferences copy, each stamped with when
// AO captured it.
type HostInventory struct {
	Skills        []domain.ExecutionHostSkill
	SkillsAsOf    time.Time
	Prefs         *domain.ExecutionHostPrefs
	PrefsAsOf     time.Time
	FromLiveProbe bool
}

// Inventory returns one host's maintenance view. With refresh the channel runs
// live and the result is persisted before it is returned; without it the
// cached rows answer, with their asOf timestamps carrying the staleness.
func (s *Service) Inventory(ctx context.Context, id domain.ExecutionHostID, refresh bool) (HostInventory, error) {
	host, err := s.maintenanceHost(ctx, id)
	if err != nil {
		return HostInventory{}, err
	}
	if refresh {
		skills, err := s.maintenance.Inventory(ctx, host)
		if err != nil {
			return HostInventory{}, err
		}
		now := s.now().UTC()
		if err := s.store.ReplaceExecutionHostSkills(ctx, host.ID, skills, now); err != nil {
			return HostInventory{}, fmt.Errorf("persist host %s skills: %w", host.ID, err)
		}
		prefs, err := s.maintenance.ReadPrefs(ctx, host)
		if err != nil {
			return HostInventory{}, err
		}
		prefs.ConfirmedAt = now
		if err := s.store.UpsertExecutionHostPrefs(ctx, prefs); err != nil {
			return HostInventory{}, fmt.Errorf("persist host %s prefs: %w", host.ID, err)
		}
	}
	skills, err := s.store.ListExecutionHostSkills(ctx, host.ID)
	if err != nil {
		return HostInventory{}, err
	}
	inventory := HostInventory{Skills: skills, FromLiveProbe: refresh}
	for _, skill := range skills {
		if skill.CapturedAt.After(inventory.SkillsAsOf) {
			inventory.SkillsAsOf = skill.CapturedAt
		}
	}
	if prefs, found, err := s.store.GetExecutionHostPrefs(ctx, host.ID); err != nil {
		return HostInventory{}, err
	} else if found {
		inventory.Prefs, inventory.PrefsAsOf = &prefs, prefs.ConfirmedAt
	}
	return inventory, nil
}

// PutPreferences replaces one host's orchestration preferences.
//
// The flow is write → worker confirm-read → persist, and the write is guarded
// by baseSHA256 — the digest of the content the caller believes is on the host
// (the one the inventory read returned). The worker refuses a mismatch as
// drift, so a file edited on the host since AO last looked is surfaced, never
// clobbered; and an unreachable host refuses before anything is sent, because
// a config write may never be ambiguous.
func (s *Service) PutPreferences(ctx context.Context, id domain.ExecutionHostID, content, baseSHA256 string) (domain.ExecutionHostPrefs, error) {
	if strings.TrimSpace(content) == "" {
		return domain.ExecutionHostPrefs{}, apierr.Invalid("PREFS_CONTENT_REQUIRED", "content is required", nil)
	}
	if !json.Valid([]byte(content)) {
		return domain.ExecutionHostPrefs{}, apierr.Invalid("PREFS_CONTENT_INVALID",
			"content must be valid JSON: the file is read by every Paseo skill on the host", nil)
	}
	baseSHA256 = strings.ToLower(strings.TrimSpace(baseSHA256))
	if baseSHA256 == "" {
		return domain.ExecutionHostPrefs{}, apierr.Invalid("PREFS_BASE_HASH_REQUIRED",
			"baseSha256 is required: it is the hash of the content currently on the host, from the inventory read", nil)
	}
	host, err := s.maintenanceHost(ctx, id)
	if err != nil {
		return domain.ExecutionHostPrefs{}, err
	}
	prefs, err := s.maintenance.WritePrefs(ctx, host, []byte(content), baseSHA256)
	if err != nil {
		return domain.ExecutionHostPrefs{}, err
	}
	prefs.ConfirmedAt = s.now().UTC()
	if err := s.store.UpsertExecutionHostPrefs(ctx, prefs); err != nil {
		return domain.ExecutionHostPrefs{}, fmt.Errorf("persist host %s prefs: %w", host.ID, err)
	}
	return prefs, nil
}

func (s *Service) maintenanceHost(ctx context.Context, id domain.ExecutionHostID) (domain.ExecutionHost, error) {
	id = domain.ExecutionHostID(strings.TrimSpace(string(id)))
	if id == "" {
		return domain.ExecutionHost{}, apierr.Invalid("HOST_ID_REQUIRED", "hostId is required", nil)
	}
	host, _, found, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("get execution host %s: %w", id, err)
	}
	if !found {
		return domain.ExecutionHost{}, apierr.NotFound("HOST_NOT_FOUND", "host "+string(id)+" is not registered")
	}
	if s.maintenance == nil {
		return domain.ExecutionHost{}, apierr.Internal("MAINTENANCE_CHANNEL_UNAVAILABLE",
			"this daemon was started without host maintenance wiring")
	}
	return host, nil
}

// SetScheduleChannel installs the network-facing schedule read and delete used
// by the per-host schedules endpoints.
func (s *Service) SetScheduleChannel(
	read func(ctx context.Context, host domain.ExecutionHost) ([]domain.ExecutionHostSchedule, error),
	remove func(ctx context.Context, host domain.ExecutionHost, scheduleID string) error,
) {
	s.scheduleReader, s.scheduleDeleter = read, remove
}

// HostSchedule is one schedule row with AO's policy judgement attached.
type HostSchedule struct {
	domain.ExecutionHostSchedule
	// PolicyViolation is true for every row by decision D6: AO owns scheduling
	// and offers no schedule create, so anything present on an AO-driven host
	// was created outside AO and is surfaced as a violation to act on, not an
	// inventory to browse. The flag exists per row so the judgement can narrow
	// later without changing the shape.
	PolicyViolation bool
}

// HostSchedules reads one host's recurring schedules live.
//
// The documented blind spot: heartbeats have no listing in the pinned CLI, so
// an empty result is a statement about schedules only, never about heartbeats.
func (s *Service) HostSchedules(ctx context.Context, id domain.ExecutionHostID) ([]HostSchedule, error) {
	host, err := s.scheduleHost(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.scheduleReader(ctx, host)
	if err != nil {
		return nil, err
	}
	schedules := make([]HostSchedule, 0, len(rows))
	for _, row := range rows {
		schedules = append(schedules, HostSchedule{ExecutionHostSchedule: row, PolicyViolation: true})
	}
	return schedules, nil
}

// DeleteHostSchedule removes one schedule from one host.
func (s *Service) DeleteHostSchedule(ctx context.Context, id domain.ExecutionHostID, scheduleID string) error {
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduleID == "" {
		return apierr.Invalid("SCHEDULE_ID_REQUIRED", "scheduleId is required", nil)
	}
	host, err := s.scheduleHost(ctx, id)
	if err != nil {
		return err
	}
	return s.scheduleDeleter(ctx, host, scheduleID)
}

func (s *Service) scheduleHost(ctx context.Context, id domain.ExecutionHostID) (domain.ExecutionHost, error) {
	id = domain.ExecutionHostID(strings.TrimSpace(string(id)))
	if id == "" {
		return domain.ExecutionHost{}, apierr.Invalid("HOST_ID_REQUIRED", "hostId is required", nil)
	}
	host, _, found, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("get execution host %s: %w", id, err)
	}
	if !found {
		return domain.ExecutionHost{}, apierr.NotFound("HOST_NOT_FOUND", "host "+string(id)+" is not registered")
	}
	if s.scheduleReader == nil || s.scheduleDeleter == nil {
		return domain.ExecutionHost{}, apierr.Internal("SCHEDULE_CHANNEL_UNAVAILABLE",
			"this daemon was started without execution schedule wiring")
	}
	return host, nil
}

// ProbeHost probes one registered host now and returns the refreshed registry
// view.
//
// An unreachable host is a 200 with reachable=false and the probe error in the
// view — unreachability is a recorded fact, not a request failure. The only
// error outcomes are an unknown host, a daemon started without probe wiring,
// and a positive self-target identity match (gap G5), which refuses the same
// way registration does.
func (s *Service) ProbeHost(ctx context.Context, id domain.ExecutionHostID) (Host, error) {
	id = domain.ExecutionHostID(strings.TrimSpace(string(id)))
	if id == "" {
		return Host{}, apierr.Invalid("HOST_ID_REQUIRED", "hostId is required", nil)
	}
	host, _, found, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return Host{}, fmt.Errorf("get execution host %s: %w", id, err)
	}
	if !found {
		return Host{}, apierr.NotFound("HOST_NOT_FOUND", "host "+string(id)+" is not registered")
	}
	if s.hostProber == nil {
		return Host{}, apierr.Internal("PROBE_UNAVAILABLE",
			"this daemon was started without execution probe wiring")
	}
	proberErr := s.hostProber(ctx, host)

	// Reload before deciding what to return: the prober records its outcome
	// (including a self-target refusal) as probe facts, and the caller should
	// see the row that was just written, not the one from before the probe.
	fresh, _, stillFound, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return Host{}, fmt.Errorf("reload execution host %s: %w", id, err)
	}
	if !stillFound {
		return Host{}, apierr.NotFound("HOST_NOT_FOUND", "host "+string(id)+" is not registered")
	}
	if proberErr != nil {
		return Host{}, proberErr
	}
	return s.hostView(ctx, fresh)
}

// Host is one registry entry with its routable capabilities and current load.
//
// Reachable and ActiveSessions are derived at read time from probe facts and
// live bindings; neither is stored, so a stale display value cannot outlive the
// fact it came from.
type Host struct {
	domain.ExecutionHost
	Capabilities   []string
	ActiveSessions int
	Reachable      bool
}

// HostInput registers or replaces one host.
type HostInput struct {
	ID                    domain.ExecutionHostID
	Name                  string
	Transport             domain.ExecutionHostTransport
	Endpoint              string
	EndpointSecretRef     string
	TrustZone             domain.ExecutionTrustZone
	Enabled               bool
	MaxConcurrentSessions int
	RequiresNoMCP         bool
	RequiresNoRelay       bool
	Capabilities          []string
}

// AnswerInput answers an agent-authored question with text.
type AnswerInput struct {
	QuestionID string
	Answer     string
	AnsweredBy string
}

// DecisionInput decides a host-side permission request.
//
// RequestID is optional and is a confirmation, not an input: when present it
// must equal the full id AO observed. AO always delivers its own stored id, so a
// caller that displays a shortened one cannot cause a short id to be sent.
type DecisionInput struct {
	QuestionID string
	Decision   domain.ExecutionPermissionDecision
	RequestID  string
	Note       string
	DecidedBy  string
}

// ListHosts returns the registry with capabilities and current load.
//
// Capabilities are fetched per host rather than in one join: the registry is a
// handful of machines an operator entered by hand, so the extra reads are
// cheaper than a bespoke query to maintain.
func (s *Service) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.store.ListExecutionHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list execution hosts: %w", err)
	}
	hosts := make([]Host, 0, len(rows))
	for _, row := range rows {
		host, err := s.hostView(ctx, row)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// RegisterHost validates and upserts one host.
//
// Validation happens here rather than at exec time on purpose. A host string
// without a colon is accepted by the Paseo CLI and silently resolves to the
// *local* daemon, so an endpoint typo would run work on the operator's own
// machine; and a credential pasted into the endpoint would be persisted in
// plaintext and echoed back by every list call.
func (s *Service) RegisterHost(ctx context.Context, in HostInput) (Host, error) {
	id := domain.ExecutionHostID(strings.TrimSpace(string(in.ID)))
	if id == "" {
		return Host{}, apierr.Invalid("HOST_ID_REQUIRED", "hostId is required", nil)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Host{}, apierr.Invalid("HOST_NAME_REQUIRED", "name is required", nil)
	}
	if err := validateTransport(in.Transport); err != nil {
		return Host{}, err
	}
	if err := validateTrustZone(in.TrustZone); err != nil {
		return Host{}, err
	}
	endpoint := strings.TrimSpace(in.Endpoint)
	if err := validateEndpoint(endpoint); err != nil {
		return Host{}, err
	}
	if in.MaxConcurrentSessions < 1 || in.MaxConcurrentSessions > maxHostConcurrency {
		return Host{}, apierr.Invalid("HOST_CONCURRENCY_INVALID",
			fmt.Sprintf("maxConcurrentSessions must be between 1 and %d", maxHostConcurrency), nil)
	}
	// The remote daemon injects a flat catalog of agent-control tools into every
	// agent it runs, including ones that create and kill other agents, and the
	// only switch is daemon-wide. A host registered without that switch asserted
	// would hand every dispatched agent the ability to dispatch more, so this is
	// a refusal rather than a default.
	if !in.RequiresNoMCP {
		return Host{}, apierr.Invalid("HOST_REQUIRES_NO_MCP",
			"requiresNoMcp must be true: AO only drives hosts whose daemon runs with MCP tool injection disabled", nil)
	}
	capabilities, err := normalizeCapabilities(in.Capabilities)
	if err != nil {
		return Host{}, err
	}

	existing, _, found, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return Host{}, fmt.Errorf("get execution host %s: %w", id, err)
	}
	now := s.now().UTC()
	host := domain.ExecutionHost{
		ID: id, Name: name, BackendType: domain.ExecutionBackendPaseo, Transport: in.Transport,
		Endpoint: endpoint, EndpointSecretRef: strings.TrimSpace(in.EndpointSecretRef),
		TrustZone: in.TrustZone, Enabled: in.Enabled, MaxConcurrentSessions: in.MaxConcurrentSessions,
		RequiresNoMCP: in.RequiresNoMCP, RequiresNoRelay: in.RequiresNoRelay,
		CreatedAt: now, UpdatedAt: now,
	}
	if found {
		// Probe facts and the observed server id belong to the observer, not to
		// whoever edits the registry. Carrying them across an edit is what keeps a
		// server-identity change detectable: overwriting server_id here would erase
		// the evidence that every agent id AO holds for this host is now stale.
		host.CreatedAt = existing.CreatedAt
		host.ServerID = existing.ServerID
		host.PaseoVersion = existing.PaseoVersion
		host.LastSuccessfulProbeAt = existing.LastSuccessfulProbeAt
		host.LastFailedProbeAt = existing.LastFailedProbeAt
		host.LastProbeError = existing.LastProbeError
	}
	if s.selfTargetGuard != nil {
		if err := s.selfTargetGuard(ctx, host); err != nil {
			return Host{}, err
		}
	}
	if err := s.store.UpsertExecutionHost(ctx, host, capabilities); err != nil {
		return Host{}, fmt.Errorf("upsert execution host %s: %w", id, err)
	}
	return s.hostView(ctx, host)
}

// ListQuestions returns the open human inbox: agent-authored questions and
// host-side permission requests, in one queue.
func (s *Service) ListQuestions(ctx context.Context) ([]domain.ExecutionInboxQuestion, error) {
	questions, err := s.store.ListOpenExecutionQuestions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open execution questions: %w", err)
	}
	return questions, nil
}

// Answer records a human answer to an agent-authored question and enqueues the
// message that delivers it.
//
// A permission request is refused here. It cannot be discharged with text: the
// agent is paused on a host-side prompt that only an explicit decision releases,
// so pasting an answer would leave the request pending forever while AO believed
// it had replied.
func (s *Service) Answer(ctx context.Context, in AnswerInput) (domain.ExecutionCommand, error) {
	question, err := s.openQuestion(ctx, in.QuestionID)
	if err != nil {
		return domain.ExecutionCommand{}, err
	}
	if question.Source != domain.ExecutionQuestionAgentEvent {
		return domain.ExecutionCommand{}, apierr.Conflict("QUESTION_REQUIRES_DECISION",
			"this is a host permission request: decide it with allow or deny instead of answering with text", nil)
	}
	answer := strings.TrimSpace(in.Answer)
	if answer == "" {
		return domain.ExecutionCommand{}, apierr.Invalid("ANSWER_REQUIRED", "answer is required", nil)
	}
	if len(answer) > MaxAnswerLen {
		return domain.ExecutionCommand{}, apierr.Invalid("ANSWER_TOO_LONG",
			fmt.Sprintf("answer must be at most %d characters", MaxAnswerLen), nil)
	}
	payload, err := json.Marshal(domain.ExecutionAnswerPayload{QuestionID: question.ID, Message: answer})
	if err != nil {
		return domain.ExecutionCommand{}, fmt.Errorf("marshal answer payload: %w", err)
	}
	return s.resolve(ctx, domain.ExecutionQuestionResolution{
		QuestionID: question.ID, Answer: answer, AnsweredBy: s.actor(in.AnsweredBy),
		CommandID: s.newID(), CommandType: domain.ExecutionCommandSendMessage,
		PayloadJSON: string(payload), AuditType: "execution.question_answered",
		DecidedAt: s.now().UTC(),
	})
}

// Decide records an allow/deny on a host-side permission request and enqueues
// the decision.
//
// The delivered request id is always the full one AO observed, taken from
// storage. The host rejects a truncated id, and a decision carrying no id at all
// approves every pending request on that agent, so neither a shortened id nor an
// absent one may reach the adapter.
func (s *Service) Decide(ctx context.Context, in DecisionInput) (domain.ExecutionCommand, error) {
	question, err := s.openQuestion(ctx, in.QuestionID)
	if err != nil {
		return domain.ExecutionCommand{}, err
	}
	if question.Source != domain.ExecutionQuestionPaseoPermission {
		return domain.ExecutionCommand{}, apierr.Conflict("QUESTION_REQUIRES_ANSWER",
			"this is an agent question: answer it with text instead of a permission decision", nil)
	}
	commandType, ok := in.Decision.CommandType()
	if !ok {
		return domain.ExecutionCommand{}, apierr.Invalid("DECISION_INVALID",
			"decision must be allow or deny", nil)
	}
	if question.ExternalID == "" {
		return domain.ExecutionCommand{}, apierr.Conflict("PERMISSION_ID_MISSING",
			"the stored permission request has no host request id, and a decision without one would approve every pending request", nil)
	}
	// A caller may echo the id back as a confirmation, but only the exact one.
	// The host's own listing truncates ids to eight characters, so a UI built on
	// that listing fails here instead of sending an id the host would reject.
	if confirm := strings.TrimSpace(in.RequestID); confirm != "" && confirm != question.ExternalID {
		return domain.ExecutionCommand{}, apierr.Invalid("PERMISSION_ID_MISMATCH",
			"requestId does not match the full host request id AO observed", nil)
	}
	note := strings.TrimSpace(in.Note)
	if len(note) > MaxAnswerLen {
		return domain.ExecutionCommand{}, apierr.Invalid("NOTE_TOO_LONG",
			fmt.Sprintf("note must be at most %d characters", MaxAnswerLen), nil)
	}
	payload, err := json.Marshal(domain.ExecutionPermissionPayload{
		QuestionID: question.ID, RequestID: question.ExternalID, Decision: in.Decision, Note: note,
	})
	if err != nil {
		return domain.ExecutionCommand{}, fmt.Errorf("marshal permission payload: %w", err)
	}
	return s.resolve(ctx, domain.ExecutionQuestionResolution{
		QuestionID: question.ID, Answer: string(in.Decision), AnsweredBy: s.actor(in.DecidedBy),
		CommandID: s.newID(), CommandType: commandType, PayloadJSON: string(payload),
		AuditType: "execution.permission_decided", DecidedAt: s.now().UTC(),
	})
}

func (s *Service) openQuestion(ctx context.Context, id string) (domain.ExecutionInboxQuestion, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.ExecutionInboxQuestion{}, apierr.Invalid("QUESTION_ID_REQUIRED", "questionId is required", nil)
	}
	question, found, err := s.store.GetExecutionQuestion(ctx, id)
	if err != nil {
		return domain.ExecutionInboxQuestion{}, fmt.Errorf("get execution question %s: %w", id, err)
	}
	if !found {
		return domain.ExecutionInboxQuestion{}, apierr.NotFound("QUESTION_NOT_FOUND", "question "+id+" was not found")
	}
	return question, nil
}

// SetQuestionResolvedHook installs the notification-closing hook, injected by
// the daemon alongside the notification writer.
func (s *Service) SetQuestionResolvedHook(hook func(ctx context.Context, sessionID domain.SessionID, questionID string)) {
	s.questionResolved = hook
}

func (s *Service) resolve(ctx context.Context, resolution domain.ExecutionQuestionResolution) (domain.ExecutionCommand, error) {
	command, err := s.store.ResolveExecutionQuestion(ctx, resolution)
	switch {
	case errors.Is(err, domain.ErrExecutionQuestionNotOpen):
		return domain.ExecutionCommand{}, apierr.Conflict("QUESTION_NOT_OPEN",
			"question "+resolution.QuestionID+" has already been answered", nil)
	case errors.Is(err, domain.ErrSessionNotRemote):
		return domain.ExecutionCommand{}, apierr.Conflict("SESSION_NOT_REMOTE",
			"question "+resolution.QuestionID+" belongs to a session with no execution host", nil)
	case err != nil:
		return domain.ExecutionCommand{}, fmt.Errorf("resolve execution question %s: %w", resolution.QuestionID, err)
	}
	if s.questionResolved != nil {
		s.questionResolved(ctx, command.SessionID, resolution.QuestionID)
	}
	return command, nil
}

func (s *Service) hostView(ctx context.Context, host domain.ExecutionHost) (Host, error) {
	_, capabilities, _, err := s.store.GetExecutionHost(ctx, host.ID)
	if err != nil {
		return Host{}, fmt.Errorf("get execution host %s capabilities: %w", host.ID, err)
	}
	bindings, err := s.store.ListActiveSessionExecutionBindingsByHost(ctx, host.ID)
	if err != nil {
		return Host{}, fmt.Errorf("list active bindings for host %s: %w", host.ID, err)
	}
	host.Endpoint = redactEndpoint(host.Endpoint)
	if capabilities == nil {
		capabilities = []string{}
	}
	return Host{
		ExecutionHost: host, Capabilities: capabilities, ActiveSessions: len(bindings),
		// A host is reachable only on the strength of its most recent probe. An
		// unreachable host is a fact about the host and nothing more: it never
		// implies anything about the sessions bound to it.
		Reachable: !host.LastSuccessfulProbeAt.IsZero() && host.LastSuccessfulProbeAt.After(host.LastFailedProbeAt),
	}, nil
}

func validateTransport(transport domain.ExecutionHostTransport) error {
	switch transport {
	case domain.ExecutionTransportLocal, domain.ExecutionTransportTailscale,
		domain.ExecutionTransportLAN, domain.ExecutionTransportPaseoRelay:
		return nil
	}
	return apierr.Invalid("HOST_TRANSPORT_INVALID",
		"transport must be one of local, tailscale, lan, paseo_relay", nil)
}

func validateTrustZone(zone domain.ExecutionTrustZone) error {
	switch zone {
	case domain.ExecutionTrustZoneHobby, domain.ExecutionTrustZoneWork, domain.ExecutionTrustZoneMixed:
		return nil
	}
	return apierr.Invalid("HOST_TRUST_ZONE_INVALID",
		"trustZone must be one of hobby, work, mixed", nil)
}

// endpointCredentialKeys are query keys whose value is a bearer credential. A
// relay offer URL carries one, and the CLI also accepts a password in the query
// string; either would be persisted in plaintext and echoed back by every list.
var endpointCredentialKeys = []string{"password=", "token="}

func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return apierr.Invalid("HOST_ENDPOINT_REQUIRED", "endpoint is required", nil)
	}
	// The CLI resolves any host string without a colon to null and falls through
	// to the local daemon, so a typo here would run remote work on the operator's
	// own machine under a remote session's identity.
	if !strings.Contains(endpoint, ":") {
		return apierr.Invalid("HOST_ENDPOINT_INVALID",
			"endpoint must contain a colon, e.g. worker.example.ts.net:6780", nil)
	}
	if strings.ContainsAny(endpoint, " \t\r\n") {
		return apierr.Invalid("HOST_ENDPOINT_INVALID", "endpoint must not contain whitespace", nil)
	}
	// An endpoint is passed to the CLI as one argv element. A leading dash would
	// be read as a flag rather than a value.
	if strings.HasPrefix(endpoint, "-") {
		return apierr.Invalid("HOST_ENDPOINT_INVALID", "endpoint must not start with '-'", nil)
	}
	lowered := strings.ToLower(endpoint)
	for _, key := range endpointCredentialKeys {
		if strings.Contains(lowered, key) {
			return apierr.Invalid("HOST_ENDPOINT_HAS_CREDENTIAL",
				"endpoint must not embed a credential; store it as endpointSecretRef instead", nil)
		}
	}
	return nil
}

// normalizeCapabilities trims, lower-cases, and de-duplicates the routable
// capability set. Routing matches capabilities exactly, so "Unity" and "unity"
// registered on two hosts would silently be two different capabilities.
func normalizeCapabilities(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		capability := strings.ToLower(strings.TrimSpace(value))
		if capability == "" {
			return nil, apierr.Invalid("HOST_CAPABILITY_INVALID", "capabilities must not contain empty entries", nil)
		}
		if _, exists := seen[capability]; exists {
			continue
		}
		seen[capability] = struct{}{}
		out = append(out, capability)
	}
	return out, nil
}

// redactEndpoint masks a credential that reached storage by some other path.
// Registration refuses one outright; this keeps a legacy or hand-edited row from
// leaking through the read model.
func redactEndpoint(endpoint string) string {
	lowered := strings.ToLower(endpoint)
	for _, key := range endpointCredentialKeys {
		index := strings.Index(lowered, key)
		if index < 0 {
			continue
		}
		return endpoint[:index+len(key)] + "REDACTED"
	}
	return endpoint
}

// SetDefaultActor installs the identity recorded when a caller names none —
// the daemon wires the OS username of whoever runs it.
func (s *Service) SetDefaultActor(identity func() string) {
	s.defaultActor = identity
}

func (s *Service) actor(name string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	if s.defaultActor != nil {
		if resolved := strings.TrimSpace(s.defaultActor()); resolved != "" {
			return resolved
		}
	}
	return "human"
}

// BindingInput is a project's machine-specific checkout on one host.
type BindingInput struct {
	ProjectID    domain.ProjectID
	HostID       domain.ExecutionHostID
	HostRepoPath string
	BaseBranch   string
	Priority     int
	SetupProfile string
	Disabled     bool
}

// BindProject records where a project is checked out on a host.
//
// Without a binding a project has no candidate hosts at all: the router
// iterates bindings, so an unbound project produces zero candidates and
// dispatch fails with ErrNoEligibleHost. The table, the store method and the
// router all existed; nothing could write a row, so no dispatch could ever
// succeed. Found by running a dispatch end to end.
//
// The repo path is per-host on purpose and cannot be inferred from the
// project: the same repo is /home/u/x on one machine and C:\Projects\X on
// another, which is the whole reason this is a binding rather than a project
// field.
func (s *Service) BindProject(ctx context.Context, in BindingInput) (domain.ProjectHostBinding, error) {
	projectID := domain.ProjectID(strings.TrimSpace(string(in.ProjectID)))
	if projectID == "" {
		return domain.ProjectHostBinding{}, apierr.Invalid("PROJECT_ID_REQUIRED", "projectId is required", nil)
	}
	hostID := domain.ExecutionHostID(strings.TrimSpace(string(in.HostID)))
	if hostID == "" {
		return domain.ProjectHostBinding{}, apierr.Invalid("HOST_ID_REQUIRED", "hostId is required", nil)
	}
	repoPath := strings.TrimSpace(in.HostRepoPath)
	if repoPath == "" {
		return domain.ProjectHostBinding{}, apierr.Invalid(
			"HOST_REPO_PATH_REQUIRED",
			"hostRepoPath is required: it is the checkout path ON THE HOST, which AO cannot infer", nil)
	}
	if _, _, found, err := s.store.GetExecutionHost(ctx, hostID); err != nil {
		return domain.ProjectHostBinding{}, err
	} else if !found {
		return domain.ProjectHostBinding{}, apierr.NotFound(
			"HOST_NOT_FOUND", "register the host before binding a project to it")
	}

	baseBranch := strings.TrimSpace(in.BaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	priority := in.Priority
	if priority <= 0 {
		priority = 100
	}
	now := s.now().UTC()
	binding := domain.ProjectHostBinding{
		ProjectID: projectID, HostID: hostID, HostRepoPath: repoPath,
		BaseBranch: baseBranch, Priority: priority, Enabled: !in.Disabled,
		SetupProfile: strings.TrimSpace(in.SetupProfile),
		CreatedAt:    now, UpdatedAt: now,
	}
	if err := s.store.UpsertProjectHostBinding(ctx, binding); err != nil {
		return domain.ProjectHostBinding{}, err
	}
	return binding, nil
}

// GetCommand returns one outbox command by id.
//
// This is how a dispatch caller watches pending → delivering → acknowledged
// (or failed) after the 201: the dispatch response carries the command id and,
// until this read existed, nothing could ever look at it again. The payload is
// deliberately not exposed — it can carry a prompt, and command state is what
// a progress display needs.
func (s *Service) GetCommand(ctx context.Context, id string) (domain.ExecutionCommand, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.ExecutionCommand{}, apierr.Invalid("COMMAND_ID_REQUIRED", "commandId is required", nil)
	}
	command, found, err := s.store.GetExecutionCommand(ctx, id)
	if err != nil {
		return domain.ExecutionCommand{}, fmt.Errorf("get execution command %s: %w", id, err)
	}
	if !found {
		return domain.ExecutionCommand{}, apierr.NotFound("COMMAND_NOT_FOUND", "command "+id+" was not found")
	}
	return command, nil
}

// Event-listing bounds. The default suits a UI timeline's first load; the cap
// keeps one request from dragging a long session's whole history through JSON
// at once — the cursor exists precisely so a caller never needs to.
const (
	DefaultEventLimit = 200
	MaxEventLimit     = 1000
)

// EventsFilter selects one session's ingested execution events. AfterID is the
// last event id the caller already holds; empty starts from the beginning.
type EventsFilter struct {
	SessionID domain.SessionID
	AfterID   string
	Limit     int
}

// ListSessionEvents returns the durable rows report ingestion has recorded for
// one session, oldest first: agent-authored reports and observer transitions
// alike, distinguished by transport. Read-only projection — serving it can
// never re-apply an event.
func (s *Service) ListSessionEvents(ctx context.Context, filter EventsFilter) ([]domain.ExecutionEventRecord, error) {
	sessionID := domain.SessionID(strings.TrimSpace(string(filter.SessionID)))
	if sessionID == "" {
		return nil, apierr.Invalid("SESSION_ID_REQUIRED", "sessionId is required", nil)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultEventLimit
	}
	if limit > MaxEventLimit {
		limit = MaxEventLimit
	}
	if _, found, err := s.store.GetSession(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("get session %s: %w", sessionID, err)
	} else if !found {
		return nil, apierr.NotFound("SESSION_NOT_FOUND", "session "+string(sessionID)+" was not found")
	}
	events, err := s.store.ListSessionExecutionEvents(ctx, sessionID, strings.TrimSpace(filter.AfterID), limit)
	if errors.Is(err, domain.ErrExecutionEventCursorUnknown) {
		return nil, apierr.Invalid("EVENT_CURSOR_UNKNOWN",
			"after does not name an event of this session; restart from the beginning", nil)
	}
	if err != nil {
		return nil, fmt.Errorf("list execution events for session %s: %w", sessionID, err)
	}
	return events, nil
}

// BindingFilter narrows a bindings list. Both fields are optional; an empty
// filter returns every binding across all projects.
type BindingFilter struct {
	ProjectID domain.ProjectID
	HostID    domain.ExecutionHostID
}

// ListBindings returns project↔host bindings matching the filter.
//
// This is the read half the bind PUT never had: without it neither the
// Computers pane (bindings per host) nor project settings (hosts per project)
// can show what is bound, and "registered but unbound" — the trap the first
// end-to-end run fell into — stays invisible.
func (s *Service) ListBindings(ctx context.Context, filter BindingFilter) ([]domain.ProjectHostBinding, error) {
	projectID := domain.ProjectID(strings.TrimSpace(string(filter.ProjectID)))
	hostID := domain.ExecutionHostID(strings.TrimSpace(string(filter.HostID)))

	var bindings []domain.ProjectHostBinding
	var err error
	if projectID != "" {
		bindings, err = s.store.ListProjectHostBindings(ctx, projectID)
	} else {
		bindings, err = s.store.ListAllProjectHostBindings(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list project host bindings: %w", err)
	}
	if hostID == "" {
		return bindings, nil
	}
	filtered := make([]domain.ProjectHostBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.HostID == hostID {
			filtered = append(filtered, binding)
		}
	}
	return filtered, nil
}
