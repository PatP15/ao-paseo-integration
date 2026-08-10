package controllers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	dispatchsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/dispatch"
	executionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/execution"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/secretstore"
)

// ExecutionService is the host-registry and human-inbox surface.
type ExecutionService interface {
	ListHosts(ctx context.Context) ([]executionsvc.Host, error)
	RegisterHost(ctx context.Context, in executionsvc.HostInput) (executionsvc.Host, error)
	ProbeHost(ctx context.Context, id domain.ExecutionHostID) (executionsvc.Host, error)
	ListQuestions(ctx context.Context) ([]domain.ExecutionInboxQuestion, error)
	Answer(ctx context.Context, in executionsvc.AnswerInput) (domain.ExecutionCommand, error)
	Decide(ctx context.Context, in executionsvc.DecisionInput) (domain.ExecutionCommand, error)
	BindProject(ctx context.Context, in executionsvc.BindingInput) (domain.ProjectHostBinding, error)
	ListBindings(ctx context.Context, filter executionsvc.BindingFilter) ([]domain.ProjectHostBinding, error)
	GetCommand(ctx context.Context, id string) (domain.ExecutionCommand, error)
	HostProviders(ctx context.Context, id domain.ExecutionHostID) ([]domain.ExecutionHostProvider, error)
	ListSessionEvents(ctx context.Context, filter executionsvc.EventsFilter) ([]domain.ExecutionEventRecord, error)
	HostSchedules(ctx context.Context, id domain.ExecutionHostID) ([]executionsvc.HostSchedule, error)
	DeleteHostSchedule(ctx context.Context, id domain.ExecutionHostID, scheduleID string) error
	Inventory(ctx context.Context, id domain.ExecutionHostID, refresh bool) (executionsvc.HostInventory, error)
	PutPreferences(ctx context.Context, id domain.ExecutionHostID, content, baseSHA256 string) (domain.ExecutionHostPrefs, error)
	Instructions(ctx context.Context, id domain.ExecutionHostID, refresh bool) (domain.ExecutionHostPrefs, bool, error)
	PutInstructions(ctx context.Context, id domain.ExecutionHostID, content, baseSHA256 string) (domain.ExecutionHostPrefs, error)
	ProjectInstructionsView(ctx context.Context, projectID domain.ProjectID) (executionsvc.ProjectInstructions, error)
	SyncBinding(ctx context.Context, projectID domain.ProjectID, hostID domain.ExecutionHostID) (executionsvc.BindingDrift, error)
	SyncSkill(ctx context.Context, hostID domain.ExecutionHostID, name, source string) (executionsvc.HostInventory, error)
}

// ExecutionDispatcher enqueues one approved work-item attempt. It commits AO's
// own facts and returns; nothing remote has happened when it does.
type ExecutionDispatcher interface {
	Dispatch(ctx context.Context, req dispatchsvc.Request) (domain.ExecutionDispatch, error)
}

// ExecutionSecretStore stores host credentials behind refs. Save returns only
// the ref; the value never travels back out through any read surface.
type ExecutionSecretStore interface {
	Save(in secretstore.SaveInput) (string, error)
	List() ([]string, error)
}

// ExecutionController exposes the remote-execution control plane: which hosts
// exist, dispatching approved work to them, and the inbox of questions and
// permission requests a human owes an answer on.
type ExecutionController struct {
	Svc      ExecutionService
	Dispatch ExecutionDispatcher
	Secrets  ExecutionSecretStore
}

// Register mounts the execution routes.
//
// Questions and permissions are separate paths over one storage table because
// they are answerable in different ways, and mixing them would let a caller
// reply to a host permission request with prose. The route a client picks is
// therefore the assertion it is making about what kind of item this is, and the
// service rejects a mismatch.
func (c *ExecutionController) Register(r chi.Router) {
	r.Get("/execution/hosts", c.listHosts)
	r.Put("/execution/hosts/{hostId}", c.registerHost)
	r.Post("/execution/hosts/{hostId}/probe", c.probeHost)
	r.Get("/execution/hosts/{hostId}/providers", c.hostProviders)
	r.Post("/execution/dispatch", c.dispatch)
	r.Put("/execution/projects/{projectId}/hosts/{hostId}", c.bindProject)
	r.Get("/execution/bindings", c.listBindings)
	r.Get("/execution/questions", c.listQuestions)
	r.Post("/execution/questions/{questionId}/answer", c.answerQuestion)
	r.Post("/execution/permissions/{questionId}/decision", c.decidePermission)
	r.Post("/execution/secrets", c.createSecret)
	r.Get("/execution/secrets", c.listSecrets)
	r.Get("/execution/commands/{commandId}", c.getCommand)
	r.Get("/sessions/{sessionId}/execution-events", c.listSessionEvents)
	r.Get("/execution/hosts/{hostId}/schedules", c.listHostSchedules)
	r.Delete("/execution/hosts/{hostId}/schedules/{scheduleId}", c.deleteHostSchedule)
	r.Get("/execution/hosts/{hostId}/inventory", c.hostInventory)
	r.Put("/execution/hosts/{hostId}/preferences", c.putHostPreferences)
	r.Get("/execution/hosts/{hostId}/instructions", c.hostInstructions)
	r.Put("/execution/hosts/{hostId}/instructions", c.putHostInstructions)
	r.Get("/projects/{id}/instructions", c.projectInstructions)
	r.Post("/execution/bindings/{projectId}/{hostId}/sync", c.syncBinding)
	r.Post("/execution/hosts/{hostId}/skills/{name}/sync", c.syncSkill)
}

// ExecutionInstructionsQuery selects one host's machine-scope CLAUDE.md view.
type ExecutionInstructionsQuery struct {
	HostID  string `path:"hostId" description:"Registered execution host."`
	Refresh bool   `query:"refresh" description:"Read the file live through the maintenance channel and persist before answering."`
}

// PutExecutionInstructionsRequest replaces the host's machine-scope CLAUDE.md.
type PutExecutionInstructionsRequest struct {
	Content    string `json:"content" description:"Complete new file content."`
	BaseSHA256 string `json:"baseSha256" description:"Hex sha256 of the content currently on the host. A mismatch is refused as drift."`
}

// ExecutionInstructionsEnvelope wraps the confirmed machine-scope CLAUDE.md.
type ExecutionInstructionsEnvelope struct {
	Instructions *ExecutionHostPrefsResponse `json:"instructions" description:"Absent when the host has never been read."`
}

func (c *ExecutionController) hostInstructions(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/execution/hosts/{hostId}/instructions")
		return
	}
	refresh := r.URL.Query().Get("refresh") == "true"
	instructions, found, err := c.Svc.Instructions(r.Context(), domain.ExecutionHostID(chi.URLParam(r, "hostId")), refresh)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := ExecutionInstructionsEnvelope{}
	if found {
		out.Instructions = &ExecutionHostPrefsResponse{
			Content: instructions.Content, SHA256: instructions.SHA256,
			Exists: instructions.Exists, ConfirmedAt: instructions.ConfirmedAt,
		}
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

func (c *ExecutionController) putHostInstructions(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPut, "/api/v1/execution/hosts/{hostId}/instructions")
		return
	}
	var in PutExecutionInstructionsRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	instructions, err := c.Svc.PutInstructions(r.Context(),
		domain.ExecutionHostID(chi.URLParam(r, "hostId")), in.Content, in.BaseSHA256)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ExecutionInstructionsEnvelope{Instructions: &ExecutionHostPrefsResponse{
		Content: instructions.Content, SHA256: instructions.SHA256,
		Exists: instructions.Exists, ConfirmedAt: instructions.ConfirmedAt,
	}})
}

// CanonicalInstructionFileResponse is one committed instruction file at the
// project's default branch.
type CanonicalInstructionFileResponse struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// BindingDriftResponse is one host binding's instruction state against canon.
type BindingDriftResponse struct {
	HostID       string   `json:"hostId"`
	HostRepoPath string   `json:"hostRepoPath"`
	BaseBranch   string   `json:"baseBranch"`
	Head         string   `json:"head,omitempty"`
	InSync       bool     `json:"inSync"`
	DriftedPaths []string `json:"driftedPaths"`
	Error        string   `json:"error,omitempty" description:"Per-binding read failure; one unreachable host does not blank the view."`
}

// ProjectInstructionsResponse is the body of GET /api/v1/projects/{id}/instructions.
type ProjectInstructionsResponse struct {
	Branch   string                             `json:"branch"`
	Files    []CanonicalInstructionFileResponse `json:"files"`
	Bindings []BindingDriftResponse             `json:"bindings"`
}

// BindingSyncParams identifies one project↔host binding.
type BindingSyncParams struct {
	ProjectID string `path:"projectId" description:"Bound project."`
	HostID    string `path:"hostId" description:"Bound execution host."`
}

// SkillSyncParams identifies one skill on one target host.
type SkillSyncParams struct {
	HostID string `path:"hostId" description:"Target execution host."`
	Name   string `path:"name" description:"Bare skill directory name."`
}

// SyncSkillRequest names where the skill comes from.
type SyncSkillRequest struct {
	Source string `json:"source" description:"'local' for the AO machine's own ~/.claude/skills, or a registered host id."`
}

// BindingDriftEnvelope wraps one refreshed binding state after a sync.
type BindingDriftEnvelope struct {
	Binding BindingDriftResponse `json:"binding"`
}

func bindingDriftResponse(drift executionsvc.BindingDrift) BindingDriftResponse {
	paths := drift.DriftedPaths
	if paths == nil {
		paths = []string{}
	}
	return BindingDriftResponse{
		HostID: string(drift.HostID), HostRepoPath: drift.HostRepoPath, BaseBranch: drift.BaseBranch,
		Head: drift.Head, InSync: drift.InSync, DriftedPaths: paths, Error: drift.Error,
	}
}

func (c *ExecutionController) projectInstructions(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/projects/{id}/instructions")
		return
	}
	view, err := c.Svc.ProjectInstructionsView(r.Context(), domain.ProjectID(chi.URLParam(r, "id")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	files := make([]CanonicalInstructionFileResponse, 0, len(view.Files))
	for _, file := range view.Files {
		files = append(files, CanonicalInstructionFileResponse{Path: file.Path, SHA256: file.SHA256, Content: file.Content})
	}
	bindings := make([]BindingDriftResponse, 0, len(view.Bindings))
	for _, drift := range view.Bindings {
		bindings = append(bindings, bindingDriftResponse(drift))
	}
	envelope.WriteJSON(w, http.StatusOK, ProjectInstructionsResponse{Branch: view.Branch, Files: files, Bindings: bindings})
}

func (c *ExecutionController) syncBinding(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/execution/bindings/{projectId}/{hostId}/sync")
		return
	}
	drift, err := c.Svc.SyncBinding(r.Context(),
		domain.ProjectID(chi.URLParam(r, "projectId")), domain.ExecutionHostID(chi.URLParam(r, "hostId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, BindingDriftEnvelope{Binding: bindingDriftResponse(drift)})
}

func (c *ExecutionController) syncSkill(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/execution/hosts/{hostId}/skills/{name}/sync")
		return
	}
	var in SyncSkillRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	inventory, err := c.Svc.SyncSkill(r.Context(),
		domain.ExecutionHostID(chi.URLParam(r, "hostId")), chi.URLParam(r, "name"), in.Source)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	skills := make([]ExecutionHostSkillResponse, 0, len(inventory.Skills))
	for _, skill := range inventory.Skills {
		skills = append(skills, hostSkillResponse(skill))
	}
	envelope.WriteJSON(w, http.StatusOK, ExecutionHostInventoryResponse{
		Skills: skills, SkillsAsOf: inventory.SkillsAsOf, Refreshed: true,
	})
}

// ExecutionInventoryQuery selects one host's maintenance view.
type ExecutionInventoryQuery struct {
	HostID  string `path:"hostId" description:"Registered execution host."`
	Refresh bool   `query:"refresh" description:"Run the maintenance channel live and persist before answering; without it the cached rows answer with their asOf timestamps."`
}

// ExecutionHostSkillResponse is one installed skill on the host.
type ExecutionHostSkillResponse struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// PolicyGated marks skills that orchestrate through Paseo (spawning
	// agents, scheduling): AO owns scheduling and drives no-MCP daemons (D6),
	// so these are off by default at dispatch and badged in host detail.
	PolicyGated bool `json:"policyGated"`
}

func hostSkillResponse(skill domain.ExecutionHostSkill) ExecutionHostSkillResponse {
	return ExecutionHostSkillResponse{
		Name: skill.Name, Description: skill.Description,
		PolicyGated: domain.SkillPolicyGated(skill.Name, skill.Description),
	}
}

// ExecutionHostPrefsResponse is the host's orchestration preferences as last
// confirmed by the maintenance channel. Sha256 is the drift anchor: pass it
// back as baseSha256 when writing.
type ExecutionHostPrefsResponse struct {
	Content     string    `json:"content"`
	SHA256      string    `json:"sha256"`
	Exists      bool      `json:"exists" description:"False when the file is absent on the host; content is then empty and sha256 is the empty-string hash."`
	ConfirmedAt time.Time `json:"confirmedAt"`
}

// ExecutionHostInventoryResponse is the body of GET
// /api/v1/execution/hosts/{hostId}/inventory: cached remote facts, each
// stamped with when AO captured them.
type ExecutionHostInventoryResponse struct {
	Skills     []ExecutionHostSkillResponse `json:"skills"`
	SkillsAsOf time.Time                    `json:"skillsAsOf,omitzero"`
	Prefs      *ExecutionHostPrefsResponse  `json:"prefs,omitempty"`
	Refreshed  bool                         `json:"refreshed" description:"True when this response came from a live channel run rather than cache alone."`
}

// PutExecutionPreferencesRequest replaces the host's preferences file.
type PutExecutionPreferencesRequest struct {
	Content    string `json:"content" description:"Complete new file content; must be valid JSON."`
	BaseSHA256 string `json:"baseSha256" description:"Hex sha256 of the content currently on the host, from the inventory read. A mismatch on the host is refused as drift."`
}

// ExecutionPreferencesEnvelope wraps the confirmed preferences after a write.
type ExecutionPreferencesEnvelope struct {
	Prefs ExecutionHostPrefsResponse `json:"prefs"`
}

func (c *ExecutionController) hostInventory(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/execution/hosts/{hostId}/inventory")
		return
	}
	refresh := r.URL.Query().Get("refresh") == "true"
	inventory, err := c.Svc.Inventory(r.Context(), domain.ExecutionHostID(chi.URLParam(r, "hostId")), refresh)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	skills := make([]ExecutionHostSkillResponse, 0, len(inventory.Skills))
	for _, skill := range inventory.Skills {
		skills = append(skills, hostSkillResponse(skill))
	}
	out := ExecutionHostInventoryResponse{
		Skills: skills, SkillsAsOf: inventory.SkillsAsOf, Refreshed: inventory.FromLiveProbe,
	}
	if inventory.Prefs != nil {
		out.Prefs = &ExecutionHostPrefsResponse{
			Content: inventory.Prefs.Content, SHA256: inventory.Prefs.SHA256,
			Exists: inventory.Prefs.Exists, ConfirmedAt: inventory.Prefs.ConfirmedAt,
		}
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

func (c *ExecutionController) putHostPreferences(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPut, "/api/v1/execution/hosts/{hostId}/preferences")
		return
	}
	var in PutExecutionPreferencesRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	prefs, err := c.Svc.PutPreferences(r.Context(),
		domain.ExecutionHostID(chi.URLParam(r, "hostId")), in.Content, in.BaseSHA256)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ExecutionPreferencesEnvelope{Prefs: ExecutionHostPrefsResponse{
		Content: prefs.Content, SHA256: prefs.SHA256, Exists: prefs.Exists, ConfirmedAt: prefs.ConfirmedAt,
	}})
}

// ExecutionScheduleIDParams identifies one schedule on one host.
type ExecutionScheduleIDParams struct {
	HostID     string `path:"hostId" description:"Registered execution host."`
	ScheduleID string `path:"scheduleId" description:"Schedule identifier as the host's daemon reports it."`
}

// ExecutionScheduleResponse is one recurring schedule as the host's daemon
// reports it, live at request time.
type ExecutionScheduleResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Cadence   string    `json:"cadence"`
	Target    string    `json:"target,omitempty"`
	Status    string    `json:"status"`
	NextRunAt time.Time `json:"nextRunAt,omitzero"`
	LastRunAt time.Time `json:"lastRunAt,omitzero"`
	// PolicyViolation is true on every row by decision D6: AO owns scheduling
	// and offers no schedule create, so anything present on an AO-driven host
	// was created outside AO.
	PolicyViolation bool `json:"policyViolation"`
}

// ListExecutionSchedulesResponse is the body of GET
// /api/v1/execution/hosts/{hostId}/schedules.
//
// The heartbeat blind spot is structural, not an omission: the pinned Paseo
// CLI has no heartbeat listing, so an empty schedules list proves nothing
// about heartbeats on the host.
type ListExecutionSchedulesResponse struct {
	Schedules []ExecutionScheduleResponse `json:"schedules"`
}

func (c *ExecutionController) listHostSchedules(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/execution/hosts/{hostId}/schedules")
		return
	}
	schedules, err := c.Svc.HostSchedules(r.Context(), domain.ExecutionHostID(chi.URLParam(r, "hostId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]ExecutionScheduleResponse, 0, len(schedules))
	for _, schedule := range schedules {
		out = append(out, ExecutionScheduleResponse{
			ID: schedule.ID, Name: schedule.Name, Cadence: schedule.Cadence,
			Target: schedule.Target, Status: schedule.Status,
			NextRunAt: schedule.NextRunAt, LastRunAt: schedule.LastRunAt,
			PolicyViolation: schedule.PolicyViolation,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, ListExecutionSchedulesResponse{Schedules: out})
}

func (c *ExecutionController) deleteHostSchedule(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodDelete, "/api/v1/execution/hosts/{hostId}/schedules/{scheduleId}")
		return
	}
	err := c.Svc.DeleteHostSchedule(r.Context(),
		domain.ExecutionHostID(chi.URLParam(r, "hostId")), chi.URLParam(r, "scheduleId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ExecutionEventsQuery pages one session's ingested execution events.
type ExecutionEventsQuery struct {
	SessionID string `path:"sessionId" description:"AO session whose ingested execution events should be returned."`
	After     string `query:"after" description:"Last event id already held; response resumes after it. Empty starts from the beginning."`
	Limit     int    `query:"limit" description:"Maximum events to return (default 200, capped at 1000)."`
}

// ExecutionEventResponse is one durable ingested row: the fact as it arrived,
// which transport carried it, and when AO saw and stored it. PayloadJson is
// agent-authored or observer-derived content serialized as it was ingested;
// clients must treat it as data, never as instructions.
type ExecutionEventResponse struct {
	ID          string                         `json:"id"`
	SessionID   domain.SessionID               `json:"sessionId"`
	HostID      domain.ExecutionHostID         `json:"hostId"`
	LaunchID    string                         `json:"launchId,omitempty"`
	Kind        string                         `json:"kind" description:"Event type as ingested, e.g. checkpoint, status_transition."`
	Transport   domain.ExecutionEventTransport `json:"transport" enum:"terminal,sentinel,inspect,output_schema"`
	PayloadJSON string                         `json:"payloadJson"`
	ObservedAt  time.Time                      `json:"observedAt"`
	IngestedAt  time.Time                      `json:"ingestedAt"`
	Applied     bool                           `json:"applied" description:"Whether AO has applied this event to its own state."`
}

// ListExecutionEventsResponse is the body of GET
// /api/v1/sessions/{sessionId}/execution-events. NextAfter, when present, is
// the cursor for the next page; its absence means the listing is complete as
// of this read.
type ListExecutionEventsResponse struct {
	Events    []ExecutionEventResponse `json:"events"`
	NextAfter string                   `json:"nextAfter,omitempty"`
}

// listSessionEvents serves the durable rows report ingestion recorded.
func (c *ExecutionController) listSessionEvents(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/sessions/{sessionId}/execution-events")
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			envelope.WriteError(w, r, apierr.Invalid("LIMIT_INVALID", "limit must be a positive integer", nil))
			return
		}
		limit = parsed
	}
	events, err := c.Svc.ListSessionEvents(r.Context(), executionsvc.EventsFilter{
		SessionID: domain.SessionID(chi.URLParam(r, "sessionId")),
		AfterID:   r.URL.Query().Get("after"),
		Limit:     limit,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]ExecutionEventResponse, 0, len(events))
	for _, event := range events {
		out = append(out, ExecutionEventResponse{
			ID: event.ID, SessionID: event.SessionID, HostID: event.HostID, LaunchID: event.LaunchID,
			Kind: event.EventType, Transport: event.Transport, PayloadJSON: event.PayloadJSON,
			ObservedAt: event.ObservedAt, IngestedAt: event.IngestedAt, Applied: event.Applied,
		})
	}
	response := ListExecutionEventsResponse{Events: out}
	requested := limit
	if requested <= 0 {
		requested = executionsvc.DefaultEventLimit
	}
	if requested > executionsvc.MaxEventLimit {
		requested = executionsvc.MaxEventLimit
	}
	// A full page may end exactly at the last row; the next request then
	// returns empty, which is the unambiguous end-of-listing signal.
	if len(out) == requested {
		response.NextAfter = out[len(out)-1].ID
	}
	envelope.WriteJSON(w, http.StatusOK, response)
}

// ExecutionProviderModelResponse is one launchable model with its thinking
// vocabulary. ThinkingOptionIds is the complete valid set for
// settings.thinkingOptionId on this model.
type ExecutionProviderModelResponse struct {
	ID                      string   `json:"id"`
	Label                   string   `json:"label"`
	Description             string   `json:"description,omitempty"`
	ThinkingOptionIDs       []string `json:"thinkingOptionIds"`
	DefaultThinkingOptionID string   `json:"defaultThinkingOptionId,omitempty"`
}

// ExecutionProviderModeResponse is one launchable mode id with its label,
// learned by inspecting a live agent of the provider on the host.
type ExecutionProviderModeResponse struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// ExecutionProviderResponse is one provider as the host's daemon reports it.
// ModeLabels are display labels, not mode ids; they describe, never validate.
// Modes carries real (id, label) pairs when the host has an agent of this
// provider to learn them from; it is re-derived live on every discovery.
type ExecutionProviderResponse struct {
	Provider    string                           `json:"provider"`
	Label       string                           `json:"label"`
	Status      string                           `json:"status" description:"Provider availability as the host daemon reports it, e.g. available or unavailable."`
	Enabled     bool                             `json:"enabled"`
	DefaultMode string                           `json:"defaultMode,omitempty"`
	ModeLabels  []string                         `json:"modeLabels"`
	Modes       []ExecutionProviderModeResponse  `json:"modes" description:"Mode ids with labels, learned from a live agent of this provider on the host. Empty when the host has none yet."`
	Models      []ExecutionProviderModelResponse `json:"models" description:"Populated for available providers only."`
}

// ListExecutionProvidersResponse is the body of GET
// /api/v1/execution/hosts/{hostId}/providers.
type ListExecutionProvidersResponse struct {
	Providers []ExecutionProviderResponse `json:"providers"`
}

// hostProviders reports what one host can launch, for dispatch settings UIs.
func (c *ExecutionController) hostProviders(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/execution/hosts/{hostId}/providers")
		return
	}
	providers, err := c.Svc.HostProviders(r.Context(), domain.ExecutionHostID(chi.URLParam(r, "hostId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]ExecutionProviderResponse, 0, len(providers))
	for _, provider := range providers {
		models := make([]ExecutionProviderModelResponse, 0, len(provider.Models))
		for _, model := range provider.Models {
			ids := model.ThinkingOptionIDs
			if ids == nil {
				ids = []string{}
			}
			models = append(models, ExecutionProviderModelResponse{
				ID: model.ID, Label: model.Label, Description: model.Description,
				ThinkingOptionIDs: ids, DefaultThinkingOptionID: model.DefaultThinkingOptionID,
			})
		}
		labels := provider.ModeLabels
		if labels == nil {
			labels = []string{}
		}
		modes := make([]ExecutionProviderModeResponse, 0, len(provider.Modes))
		for _, mode := range provider.Modes {
			modes = append(modes, ExecutionProviderModeResponse{ID: mode.ID, Label: mode.Label})
		}
		out = append(out, ExecutionProviderResponse{
			Provider: provider.Provider, Label: provider.Label, Status: provider.Status,
			Enabled: provider.Enabled, DefaultMode: provider.DefaultMode,
			ModeLabels: labels, Modes: modes, Models: models,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, ListExecutionProvidersResponse{Providers: out})
}

// ExecutionCommandIDParam identifies one outbox command.
type ExecutionCommandIDParam struct {
	CommandID string `path:"commandId" description:"Outbox command identifier, as returned by dispatch and decision responses."`
}

// ExecutionCommandResponse is one outbox command's delivery state. The payload
// is deliberately absent: it can carry a prompt, and progress needs state.
type ExecutionCommandResponse struct {
	CommandID      string                       `json:"commandId"`
	SessionID      domain.SessionID             `json:"sessionId"`
	HostID         domain.ExecutionHostID       `json:"hostId"`
	CommandType    domain.ExecutionCommandType  `json:"commandType" enum:"start_agent,send_message,answer_permission,deny_permission"`
	CommandState   domain.ExecutionCommandState `json:"commandState" enum:"pending,delivering,acknowledged,failed"`
	AttemptCount   int                          `json:"attemptCount" description:"Delivery attempts so far, including escalations."`
	LastError      string                       `json:"lastError,omitempty" description:"Most recent delivery failure, cleared by success."`
	CreatedAt      time.Time                    `json:"createdAt"`
	NextAttemptAt  time.Time                    `json:"nextAttemptAt,omitzero"`
	AcknowledgedAt time.Time                    `json:"acknowledgedAt,omitzero"`
}

// getCommand answers what happened to one queued command after its 201.
func (c *ExecutionController) getCommand(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/execution/commands/{commandId}")
		return
	}
	command, err := c.Svc.GetCommand(r.Context(), chi.URLParam(r, "commandId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ExecutionCommandResponse{
		CommandID: command.ID, SessionID: command.SessionID, HostID: command.HostID,
		CommandType: command.Type, CommandState: command.State,
		AttemptCount: command.AttemptCount, LastError: command.LastError,
		CreatedAt: command.CreatedAt, NextAttemptAt: command.NextAttemptAt,
		AcknowledgedAt: command.AcknowledgedAt,
	})
}

// createSecret stores a credential and returns the ref that names it, closing
// the loop the register API opens: register demands a secret ref, and until
// this route only a shell on the daemon machine could create one.
func (c *ExecutionController) createSecret(w http.ResponseWriter, r *http.Request) {
	if c.Secrets == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/execution/secrets")
		return
	}
	var in CreateExecutionSecretRequest
	// Strict: an unknown key on a credential write is a caller confused about
	// the contract, and this is the worst route to guess on.
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON",
			"Invalid JSON body: a secret accepts only name, value, and replace", nil)
		return
	}
	ref, err := c.Secrets.Save(secretstore.SaveInput{Name: in.Name, Value: in.Value, Replace: in.Replace})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, ExecutionSecretEnvelope{Ref: ref})
}

func (c *ExecutionController) listSecrets(w http.ResponseWriter, r *http.Request) {
	if c.Secrets == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/execution/secrets")
		return
	}
	names, err := c.Secrets.List()
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListExecutionSecretsResponse{Refs: names})
}

func (c *ExecutionController) listHosts(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/execution/hosts")
		return
	}
	hosts, err := c.Svc.ListHosts(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]ExecutionHostResponse, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, executionHostResponse(host))
	}
	envelope.WriteJSON(w, http.StatusOK, ListExecutionHostsResponse{Hosts: out})
}

func (c *ExecutionController) registerHost(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPut, "/api/v1/execution/hosts/{hostId}")
		return
	}
	var in RegisterExecutionHostRequest
	// Strict: an unrecognised key here is most likely a capability the caller
	// believes it is setting. Silently dropping it would register a host with
	// weaker constraints than the operator asked for.
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	host, err := c.Svc.RegisterHost(r.Context(), executionsvc.HostInput{
		ID:                    domain.ExecutionHostID(strings.TrimSpace(chi.URLParam(r, "hostId"))),
		Name:                  in.Name,
		Transport:             in.Transport,
		Endpoint:              in.Endpoint,
		EndpointSecretRef:     in.EndpointSecretRef,
		TrustZone:             in.TrustZone,
		Enabled:               in.Enabled,
		MaxConcurrentSessions: in.MaxConcurrentSessions,
		RequiresNoMCP:         in.RequiresNoMCP,
		RequiresNoRelay:       in.RequiresNoRelay,
		Capabilities:          in.Capabilities,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ExecutionHostEnvelope{Host: executionHostResponse(host)})
}

// probeHost probes one host now and answers with the refreshed registry entry.
//
// An unreachable host is a 200 whose view says reachable=false with the probe
// error attached: unreachability is a recorded fact about the host, never a
// request failure. Only an unknown host, missing wiring, or a self-target
// identity match (G5) error.
func (c *ExecutionController) probeHost(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/execution/hosts/{hostId}/probe")
		return
	}
	host, err := c.Svc.ProbeHost(r.Context(), domain.ExecutionHostID(chi.URLParam(r, "hostId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ExecutionHostEnvelope{Host: executionHostResponse(host)})
}

// BindProjectPathParams names the path parameters for the bind route. The spec
// generator validates that every {placeholder} in a path has a declared
// parameter, which is what caught this route being added without them.
type BindProjectPathParams struct {
	ProjectID string `path:"projectId" description:"AO project id"`
	HostID    string `path:"hostId" description:"Registered execution host id"`
}

// BindProjectRequest records where a project is checked out on one host.
type BindProjectRequest struct {
	HostRepoPath string `json:"hostRepoPath"`
	BaseBranch   string `json:"baseBranch,omitempty"`
	Priority     int    `json:"priority,omitempty"`
	SetupProfile string `json:"setupProfile,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
}

// BindProjectResponse echoes the stored binding.
type BindProjectResponse struct {
	ProjectID    string `json:"projectId"`
	HostID       string `json:"hostId"`
	HostRepoPath string `json:"hostRepoPath"`
	BaseBranch   string `json:"baseBranch"`
	Priority     int    `json:"priority"`
	Enabled      bool   `json:"enabled"`
}

// bindProject records a project's checkout path on a host.
//
// Dispatch routes over bindings, so an unbound project has no candidate hosts
// at all and fails with ErrNoEligibleHost no matter how many hosts are online.
func (c *ExecutionController) bindProject(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPut, "/api/v1/execution/projects/{projectId}/hosts/{hostId}")
		return
	}
	var in BindProjectRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	binding, err := c.Svc.BindProject(r.Context(), executionsvc.BindingInput{
		ProjectID:    domain.ProjectID(chi.URLParam(r, "projectId")),
		HostID:       domain.ExecutionHostID(chi.URLParam(r, "hostId")),
		HostRepoPath: in.HostRepoPath,
		BaseBranch:   in.BaseBranch,
		Priority:     in.Priority,
		SetupProfile: in.SetupProfile,
		Disabled:     in.Disabled,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, BindProjectResponse{
		ProjectID: string(binding.ProjectID), HostID: string(binding.HostID),
		HostRepoPath: binding.HostRepoPath, BaseBranch: binding.BaseBranch,
		Priority: binding.Priority, Enabled: binding.Enabled,
	})
}

// ListBindingsQuery narrows the bindings list. Both parameters are optional.
type ListBindingsQuery struct {
	ProjectID string `query:"projectId" description:"Only bindings for this project."`
	HostID    string `query:"hostId" description:"Only bindings on this host."`
}

// ExecutionBindingResponse is one stored project↔host binding.
type ExecutionBindingResponse struct {
	ProjectID    string    `json:"projectId"`
	HostID       string    `json:"hostId"`
	HostRepoPath string    `json:"hostRepoPath" description:"Checkout path on the host, which AO cannot infer."`
	BaseBranch   string    `json:"baseBranch"`
	Priority     int       `json:"priority" description:"Lower sorts first when several hosts qualify."`
	Enabled      bool      `json:"enabled"`
	SetupProfile string    `json:"setupProfile,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ListExecutionBindingsResponse is the body of GET /api/v1/execution/bindings.
type ListExecutionBindingsResponse struct {
	Bindings []ExecutionBindingResponse `json:"bindings"`
}

// listBindings answers which projects are bound to which hosts. It exists
// because the router iterates bindings — an unbound project has zero candidate
// hosts however many are online — and until this route nothing could show that.
func (c *ExecutionController) listBindings(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/execution/bindings")
		return
	}
	bindings, err := c.Svc.ListBindings(r.Context(), executionsvc.BindingFilter{
		ProjectID: domain.ProjectID(r.URL.Query().Get("projectId")),
		HostID:    domain.ExecutionHostID(r.URL.Query().Get("hostId")),
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]ExecutionBindingResponse, 0, len(bindings))
	for _, binding := range bindings {
		out = append(out, ExecutionBindingResponse{
			ProjectID: string(binding.ProjectID), HostID: string(binding.HostID),
			HostRepoPath: binding.HostRepoPath, BaseBranch: binding.BaseBranch,
			Priority: binding.Priority, Enabled: binding.Enabled,
			SetupProfile: binding.SetupProfile,
			CreatedAt:    binding.CreatedAt, UpdatedAt: binding.UpdatedAt,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, ListExecutionBindingsResponse{Bindings: out})
}

// dispatchRefusalReason is the refusal sentence without the layered call
// context wrapped around it ("create execution dispatch: dispatch refused:
// …"). The remaining text names only ids the caller already sent, never a
// credential or an endpoint.
func dispatchRefusalReason(err error) string {
	message := err.Error()
	if _, reason, found := strings.Cut(message, domain.ErrDispatchRefused.Error()+": "); found {
		return reason
	}
	return message
}

func (c *ExecutionController) dispatch(w http.ResponseWriter, r *http.Request) {
	if c.Dispatch == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/execution/dispatch")
		return
	}
	var in DispatchExecutionRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if err := in.validate(); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	req := dispatchsvc.Request{
		WorkItemID:           strings.TrimSpace(in.WorkItemID),
		ProjectID:            in.ProjectID,
		TrustZone:            in.TrustZone,
		RequiredCapabilities: in.RequiredCapabilities,
		IssueID:              in.IssueID,
		Harness:              in.Harness,
		DisplayName:          strings.TrimSpace(in.DisplayName),
		Branch:               strings.TrimSpace(in.Branch),
		Provider:             strings.TrimSpace(in.Provider),
		Model:                strings.TrimSpace(in.Model),
		Mode:                 strings.TrimSpace(in.Mode),
		Prompt:               in.Prompt,
	}
	if in.Settings != nil {
		req.ThinkingOptionID = strings.TrimSpace(in.Settings.ThinkingOptionID)
		req.Features = in.Settings.Features
		req.SkillPolicyOverrides = in.Settings.SkillPolicyOverrides
	}
	result, err := c.Dispatch.Dispatch(r.Context(), req)
	if err != nil {
		// "No eligible host" is an expected scheduling outcome, not a server
		// fault: every host may be offline, at capacity, in another zone, or
		// simply not bound to this project. Returning 500 told the caller AO
		// had broken and buried the actual reason, which is how an unbound
		// project looked like an internal error.
		if errors.Is(err, dispatchsvc.ErrNoEligibleHost) {
			envelope.WriteError(w, r, apierr.Conflict(
				"NO_ELIGIBLE_HOST",
				"No registered host is eligible: check that the project is bound to a host in "+
					"the requested trust zone, that the host is enabled and online, and that it "+
					"has free capacity.",
				nil))
			return
		}
		// The dispatch transaction's own preconditions are the same class of
		// answer: the work item is not approved, it is already finished, it
		// belongs to another project, or the chosen host changed under the
		// request. Each is a caller-actionable refusal with a precise reason,
		// and a 500 with "Internal server error" threw that reason away.
		if errors.Is(err, domain.ErrDispatchRefused) {
			envelope.WriteError(w, r, apierr.Conflict("DISPATCH_REFUSED", dispatchRefusalReason(err), nil))
			return
		}
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, DispatchExecutionResponse{
		SessionID:      result.Session.ID,
		HostID:         result.Binding.HostID,
		WorkspaceTitle: result.Binding.WorkspaceTitle,
		IntentID:       result.Binding.IntentID,
		Attempt:        result.Binding.Attempt,
		CommandID:      result.Command.ID,
		CommandState:   result.Command.State,
	})
}

func (c *ExecutionController) listQuestions(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/execution/questions")
		return
	}
	questions, err := c.Svc.ListQuestions(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]ExecutionQuestionResponse, 0, len(questions))
	for _, question := range questions {
		options := question.Options
		if options == nil {
			options = []string{}
		}
		out = append(out, ExecutionQuestionResponse{
			ID:             question.ID,
			SessionID:      question.SessionID,
			WorkItemID:     question.WorkItemID,
			Source:         question.Source,
			ExternalID:     question.ExternalID,
			Question:       question.Question,
			Recommendation: question.Recommendation,
			Options:        options,
			CreatedAt:      question.CreatedAt,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, ListExecutionQuestionsResponse{Questions: out})
}

func (c *ExecutionController) answerQuestion(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/execution/questions/{questionId}/answer")
		return
	}
	var in AnswerExecutionQuestionRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	command, err := c.Svc.Answer(r.Context(), executionsvc.AnswerInput{
		QuestionID: chi.URLParam(r, "questionId"),
		Answer:     in.Answer,
		AnsweredBy: in.AnsweredBy,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, executionDecisionResponse(chi.URLParam(r, "questionId"), command))
}

func (c *ExecutionController) decidePermission(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/execution/permissions/{questionId}/decision")
		return
	}
	var in DecideExecutionPermissionRequest
	// Strict decoding is the enforcement point for "no decision AO cannot keep".
	// A client that sends a broader scope than the host supports (an "always
	// allow", a tool-wide grant) is rejected rather than having the extra field
	// dropped and the narrow decision applied under a wider-sounding name.
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON",
			"Invalid JSON body: a permission decision accepts only decision, requestId, note, and decidedBy", nil)
		return
	}
	command, err := c.Svc.Decide(r.Context(), executionsvc.DecisionInput{
		QuestionID: chi.URLParam(r, "questionId"),
		Decision:   in.Decision,
		RequestID:  in.RequestID,
		Note:       in.Note,
		DecidedBy:  in.DecidedBy,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, executionDecisionResponse(chi.URLParam(r, "questionId"), command))
}

func executionDecisionResponse(questionID string, command domain.ExecutionCommand) ExecutionDecisionResponse {
	return ExecutionDecisionResponse{
		QuestionID:   strings.TrimSpace(questionID),
		SessionID:    command.SessionID,
		CommandID:    command.ID,
		CommandType:  command.Type,
		CommandState: command.State,
	}
}

func executionHostResponse(host executionsvc.Host) ExecutionHostResponse {
	capabilities := host.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	return ExecutionHostResponse{
		ID:                    host.ID,
		Name:                  host.Name,
		BackendType:           host.BackendType,
		Transport:             host.Transport,
		Endpoint:              host.Endpoint,
		EndpointSecretRef:     host.EndpointSecretRef,
		TrustZone:             host.TrustZone,
		Enabled:               host.Enabled,
		MaxConcurrentSessions: host.MaxConcurrentSessions,
		ActiveSessions:        host.ActiveSessions,
		Capabilities:          capabilities,
		Reachable:             host.Reachable,
		ServerID:              host.ServerID,
		PaseoVersion:          host.PaseoVersion,
		RequiresNoMCP:         host.RequiresNoMCP,
		RequiresNoRelay:       host.RequiresNoRelay,
		LastSuccessfulProbeAt: host.LastSuccessfulProbeAt,
		LastFailedProbeAt:     host.LastFailedProbeAt,
		LastProbeError:        host.LastProbeError,
	}
}

// --- wire shapes ------------------------------------------------------------

// ExecutionHostIDParam identifies the host being registered or replaced.
type ExecutionHostIDParam struct {
	HostID string `path:"hostId" description:"Execution host identifier."`
}

// ExecutionQuestionIDParam identifies one human-inbox item.
type ExecutionQuestionIDParam struct {
	QuestionID string `path:"questionId" description:"Human-inbox question identifier."`
}

// ExecutionHostResponse is one registry entry as served.
//
// Endpoint is echoed only because an operator needs to see what they registered;
// it never carries a credential, which is refused on write and masked on read.
type ExecutionHostResponse struct {
	ID                    domain.ExecutionHostID        `json:"id"`
	Name                  string                        `json:"name"`
	BackendType           domain.ExecutionBackendType   `json:"backendType" description:"Execution substrate that owns sessions on this host."`
	Transport             domain.ExecutionHostTransport `json:"transport" enum:"local,tailscale,lan,paseo_relay"`
	Endpoint              string                        `json:"endpoint" description:"Host string used to reach the remote daemon. Always contains a colon."`
	EndpointSecretRef     string                        `json:"endpointSecretRef" description:"Reference to the stored credential. Never the credential itself."`
	TrustZone             domain.ExecutionTrustZone     `json:"trustZone" enum:"hobby,work,mixed"`
	Enabled               bool                          `json:"enabled"`
	MaxConcurrentSessions int                           `json:"maxConcurrentSessions"`
	ActiveSessions        int                           `json:"activeSessions" description:"Live bindings on this host, counted at read time."`
	Capabilities          []string                      `json:"capabilities"`
	Reachable             bool                          `json:"reachable" description:"Derived from the most recent probe. Unreachable is a fact about the host only; it never implies its sessions are dead."`
	ServerID              string                        `json:"serverId" description:"Server identity observed by a probe. A change invalidates every agent id AO holds for this host."`
	PaseoVersion          string                        `json:"paseoVersion"`
	RequiresNoMCP         bool                          `json:"requiresNoMcp" description:"Always true: AO only drives hosts whose daemon disables agent-control tool injection."`
	RequiresNoRelay       bool                          `json:"requiresNoRelay"`
	LastSuccessfulProbeAt time.Time                     `json:"lastSuccessfulProbeAt,omitzero"`
	LastFailedProbeAt     time.Time                     `json:"lastFailedProbeAt,omitzero"`
	LastProbeError        string                        `json:"lastProbeError,omitempty"`
}

// ListExecutionHostsResponse is the body of GET /api/v1/execution/hosts.
type ListExecutionHostsResponse struct {
	Hosts []ExecutionHostResponse `json:"hosts"`
}

// ExecutionHostEnvelope is the body of PUT /api/v1/execution/hosts/{hostId}.
type ExecutionHostEnvelope struct {
	Host ExecutionHostResponse `json:"host"`
}

// RegisterExecutionHostRequest registers or replaces one host. The id comes from
// the path, so the same body is a create and an update.
type RegisterExecutionHostRequest struct {
	Name                  string                        `json:"name"`
	Transport             domain.ExecutionHostTransport `json:"transport" enum:"local,tailscale,lan,paseo_relay"`
	Endpoint              string                        `json:"endpoint" description:"Host string for the remote daemon. Must contain a colon and must not embed a credential."`
	EndpointSecretRef     string                        `json:"endpointSecretRef,omitempty" description:"Reference to a stored credential. Pass a reference, never a password or offer URL."`
	TrustZone             domain.ExecutionTrustZone     `json:"trustZone" enum:"hobby,work,mixed"`
	Enabled               bool                          `json:"enabled"`
	MaxConcurrentSessions int                           `json:"maxConcurrentSessions" minimum:"1" maximum:"64"`
	RequiresNoMCP         bool                          `json:"requiresNoMcp" description:"Must be true. AO refuses hosts whose daemon injects agent-control tools."`
	RequiresNoRelay       bool                          `json:"requiresNoRelay,omitempty"`
	Capabilities          []string                      `json:"capabilities,omitempty" description:"Routable capabilities, matched exactly during host selection."`
}

// DispatchExecutionRequest asks AO to place one approved work-item attempt on a
// host. Host selection is AO's, not the caller's: a request names the
// constraints, never the machine.
type DispatchExecutionRequest struct {
	WorkItemID           string                    `json:"workItemId"`
	ProjectID            domain.ProjectID          `json:"projectId"`
	TrustZone            domain.ExecutionTrustZone `json:"trustZone" enum:"hobby,work,mixed"`
	RequiredCapabilities []string                  `json:"requiredCapabilities,omitempty"`
	IssueID              domain.IssueID            `json:"issueId,omitempty"`
	Harness              domain.AgentHarness       `json:"harness" description:"AO harness recorded for the session. Must be one AO already supports."`
	DisplayName          string                    `json:"displayName,omitempty"`
	Branch               string                    `json:"branch"`
	Provider             string                    `json:"provider" description:"Remote provider to launch, e.g. claude or codex."`
	Model                string                    `json:"model,omitempty"`
	Mode                 string                    `json:"mode,omitempty"`
	Settings             *DispatchSettings         `json:"settings,omitempty"`
	Prompt               string                    `json:"prompt"`
}

// DispatchSettings are provider runtime settings validated against what
// provider discovery reports for the selected host: an id discovery did not
// return is refused, never forwarded.
type DispatchSettings struct {
	ThinkingOptionID string `json:"thinkingOptionId,omitempty" description:"Thinking option id from the host's provider discovery; requires model to be set."`
	// Features are refused by the pinned Paseo CLI (no discovery, no run flag);
	// the field exists so the request shape is stable when the CLI grows one.
	Features map[string]bool `json:"features,omitempty" description:"Provider feature toggles. Not supported by the pinned Paseo CLI; any entry is refused."`
	// SkillPolicyOverrides are audited, never forwarded: they record which
	// policy-gated skills the operator explicitly enabled for this task.
	SkillPolicyOverrides []string `json:"skillPolicyOverrides,omitempty" description:"Policy-gated skills explicitly enabled for this task; each is recorded in the audit log with the dispatch."`
}

// DispatchExecutionResponse reports what AO committed. Nothing remote exists
// yet: the command is queued and delivered by the outbox worker.
type DispatchExecutionResponse struct {
	SessionID      domain.SessionID             `json:"sessionId"`
	HostID         domain.ExecutionHostID       `json:"hostId"`
	WorkspaceTitle string                       `json:"workspaceTitle"`
	IntentID       domain.ExecutionIntentID     `json:"intentId" description:"Correlates the launch with later reconciliation if AO dies mid-create."`
	Attempt        int                          `json:"attempt"`
	CommandID      string                       `json:"commandId"`
	CommandState   domain.ExecutionCommandState `json:"commandState" enum:"pending,delivering,acknowledged,failed"`
}

// validate rejects a dispatch request before anything is committed.
//
// Beyond the required fields, every value that eventually becomes one argv
// element for the remote CLI is checked here: a value with whitespace or a
// leading dash would be read as a flag rather than as data, and the adapter
// should never be the first place that is noticed.
func (in DispatchExecutionRequest) validate() error {
	for _, required := range []struct{ code, field, value string }{
		{"WORK_ITEM_ID_REQUIRED", "workItemId", in.WorkItemID},
		{"PROJECT_ID_REQUIRED", "projectId", string(in.ProjectID)},
		{"BRANCH_REQUIRED", "branch", in.Branch},
		{"PROVIDER_REQUIRED", "provider", in.Provider},
		{"PROMPT_REQUIRED", "prompt", in.Prompt},
	} {
		if strings.TrimSpace(required.value) == "" {
			return apierr.Invalid(required.code, required.field+" is required", nil)
		}
	}
	switch in.TrustZone {
	case domain.ExecutionTrustZoneHobby, domain.ExecutionTrustZoneWork, domain.ExecutionTrustZoneMixed:
	default:
		return apierr.Invalid("TRUST_ZONE_INVALID", "trustZone must be one of hobby, work, mixed", nil)
	}
	// The harness is validated against what AO already supports rather than
	// widened for remote sessions: the sessions table constrains the column, so an
	// unknown value would be rejected by the database mid-dispatch instead of here.
	if !in.Harness.IsKnown() {
		return apierr.Invalid("HARNESS_INVALID", "harness "+string(in.Harness)+" is not a supported AO harness", nil)
	}
	if len(in.Prompt) > maxPromptLen {
		return apierr.Invalid("PROMPT_TOO_LONG", "prompt exceeds the maximum length", nil)
	}
	for _, arg := range []struct{ code, field, value string }{
		{"BRANCH_INVALID", "branch", in.Branch},
		{"PROVIDER_INVALID", "provider", in.Provider},
		{"MODEL_INVALID", "model", in.Model},
		{"MODE_INVALID", "mode", in.Mode},
	} {
		if err := validateExecutionArg(arg.code, arg.field, arg.value); err != nil {
			return err
		}
	}
	if in.Settings != nil {
		// Refused here rather than silently dropped: a caller who asked for a
		// feature and got a normal launch would trust a setting that never
		// applied. The pinned Paseo CLI exposes no feature discovery
		// (inspect_provider is MCP-only) and no run flag to forward one.
		if len(in.Settings.Features) > 0 {
			return apierr.Invalid("FEATURES_UNSUPPORTED",
				"settings.features is not supported by the pinned Paseo CLI; only settings.thinkingOptionId can be forwarded", nil)
		}
		if err := validateExecutionArg("THINKING_OPTION_INVALID", "settings.thinkingOptionId",
			in.Settings.ThinkingOptionID); err != nil {
			return err
		}
	}
	for _, capability := range in.RequiredCapabilities {
		if strings.TrimSpace(capability) == "" {
			return apierr.Invalid("CAPABILITY_INVALID", "requiredCapabilities must not contain empty entries", nil)
		}
	}
	return nil
}

func validateExecutionArg(code, field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "-") {
		return apierr.Invalid(code, field+" must not start with '-'", nil)
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return apierr.Invalid(code, field+" must not contain whitespace", nil)
	}
	return nil
}

// ExecutionQuestionResponse is one open inbox item.
type ExecutionQuestionResponse struct {
	ID         string                         `json:"id"`
	SessionID  domain.SessionID               `json:"sessionId"`
	WorkItemID string                         `json:"workItemId,omitempty"`
	Source     domain.ExecutionQuestionSource `json:"source" enum:"agent_event,paseo_permission" description:"agent_event is answerable with text; paseo_permission requires an allow/deny decision."`
	ExternalID string                         `json:"externalId" description:"Source identifier: the report event id, or the host's full permission request id."`
	Question   string                         `json:"question"`
	// Recommendation and Options are what the agent proposed. They are advisory:
	// an agent-authored report can never authorize anything on its own.
	Recommendation string    `json:"recommendation,omitempty"`
	Options        []string  `json:"options"`
	CreatedAt      time.Time `json:"createdAt"`
}

// ListExecutionQuestionsResponse is the body of GET /api/v1/execution/questions.
type ListExecutionQuestionsResponse struct {
	Questions []ExecutionQuestionResponse `json:"questions"`
}

// AnswerExecutionQuestionRequest answers an agent-authored question with text.
type AnswerExecutionQuestionRequest struct {
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answeredBy,omitempty" description:"Recorded in the audit log. Defaults to \"human\"."`
}

// DecideExecutionPermissionRequest decides a host-side permission request.
//
// There is deliberately no scope field. The host enforces one pending request at
// a time and has no durable per-tool grant, so a broader decision would be a
// promise AO could not keep. RequestID is an optional confirmation of the full id
// AO already holds; AO always sends its own stored id.
type DecideExecutionPermissionRequest struct {
	Decision  domain.ExecutionPermissionDecision `json:"decision" enum:"allow,deny"`
	RequestID string                             `json:"requestId,omitempty" description:"Optional confirmation. Must equal the full host request id AO observed; a truncated id is rejected."`
	Note      string                             `json:"note,omitempty"`
	DecidedBy string                             `json:"decidedBy,omitempty" description:"Recorded in the audit log. Defaults to \"human\"."`
}

// CreateExecutionSecretRequest stores one credential behind a ref. This is the
// only surface that ever carries the value; every other request and response
// speaks in refs.
type CreateExecutionSecretRequest struct {
	Name    string `json:"name" description:"Bare file name the ref will use: no path separators, whitespace, or leading dot."`
	Value   string `json:"value" description:"The credential. Stored 0600 under the daemon's data dir; never returned, logged, or persisted anywhere else."`
	Replace bool   `json:"replace,omitempty" description:"Must be true to rotate an existing ref; otherwise an existing name is refused."`
}

// ExecutionSecretEnvelope is the body of POST /api/v1/execution/secrets.
type ExecutionSecretEnvelope struct {
	Ref string `json:"ref" description:"The stored ref, as passed to endpointSecretRef at host registration."`
}

// ListExecutionSecretsResponse is the body of GET /api/v1/execution/secrets.
// Names only, by construction.
type ListExecutionSecretsResponse struct {
	Refs []string `json:"refs"`
}

// ExecutionDecisionResponse reports the queued delivery for an answered item.
type ExecutionDecisionResponse struct {
	QuestionID   string                       `json:"questionId"`
	SessionID    domain.SessionID             `json:"sessionId"`
	CommandID    string                       `json:"commandId"`
	CommandType  domain.ExecutionCommandType  `json:"commandType" enum:"send_message,answer_permission,deny_permission"`
	CommandState domain.ExecutionCommandState `json:"commandState" enum:"pending,delivering,acknowledged,failed"`
}
