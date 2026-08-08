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
}

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
		QuestionID: question.ID, Answer: answer, AnsweredBy: actor(in.AnsweredBy),
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
		QuestionID: question.ID, Answer: string(in.Decision), AnsweredBy: actor(in.DecidedBy),
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

func actor(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "human"
	}
	return name
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

// ListBindings returns a project's host bindings.
func (s *Service) ListBindings(ctx context.Context, projectID domain.ProjectID) ([]domain.ProjectHostBinding, error) {
	return s.store.ListProjectHostBindings(ctx, domain.ProjectID(strings.TrimSpace(string(projectID))))
}
