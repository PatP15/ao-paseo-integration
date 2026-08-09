package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// seedBoundSession creates the session and execution binding an inbox item needs
// before it can be resolved: a decision is delivered to the host the session is
// bound to, so a question on an unbound session has nowhere to go.
func seedBoundSession(t *testing.T, s *sqlite.Store, at time.Time) domain.SessionID {
	t.Helper()
	ctx := context.Background()
	seedProject(t, s, "project")
	if err := s.UpsertWorkItem(ctx, domain.WorkItem{
		ID: "work-1", ProjectID: "project", Title: "Approved work", ApprovalState: domain.WorkItemApproved,
		LifecycleFact: domain.WorkItemOpen, CreatedByType: "human", CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("seed work item: %v", err)
	}
	if err := s.UpsertExecutionHost(ctx, domain.ExecutionHost{
		ID: "worker-1", Name: "worker", BackendType: domain.ExecutionBackendPaseo,
		Transport: domain.ExecutionTransportTailscale, Endpoint: "worker:6767",
		TrustZone: domain.ExecutionTrustZoneWork, Enabled: true, MaxConcurrentSessions: 4,
		ServerID: "server", RequiresNoMCP: true, RequiresNoRelay: true, CreatedAt: at, UpdatedAt: at,
	}, nil); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	if err := s.UpsertProjectHostBinding(ctx, domain.ProjectHostBinding{
		ProjectID: "project", HostID: "worker-1", HostRepoPath: "/remote/project",
		BaseBranch: "main", Priority: 1, Enabled: true, CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("seed project binding: %v", err)
	}
	dispatched, err := s.CreateExecutionDispatch(ctx, domain.ExecutionDispatchSeed{
		WorkItemID: "work-1",
		Session: domain.SessionRecord{
			ProjectID: "project", Kind: domain.KindWorker, Harness: domain.HarnessCodex,
			DisplayName: "Implement work",
		},
		HostID: "worker-1", BoundServerID: "server", HostRepoPath: "/remote/project",
		BaseBranch: "main", Branch: "ao/work-1", Provider: "codex", Prompt: "Do the work.",
		IntentID: "intent-1", Attempt: 1, DispatchGeneration: 1,
		LaunchID: "launch-1", CommandID: "command-1", CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	return dispatched.Session.ID
}

func TestResolveExecutionQuestionCommitsAnswerCommandAndAudit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	sessionID := seedBoundSession(t, s, at)

	_, opened, err := s.OpenExecutionAgentQuestion(ctx, domain.ExecutionAgentQuestion{
		SessionID: sessionID, WorkItemID: "work-1", EventID: "event-1",
		Question: "Rebase or merge?", Recommendation: "rebase",
		Options: []string{"rebase", "merge"}, CreatedAt: at,
	})
	if err != nil || !opened {
		t.Fatalf("open agent question: opened=%v err=%v", opened, err)
	}
	questions, err := s.ListOpenExecutionQuestions(ctx)
	if err != nil || len(questions) != 1 {
		t.Fatalf("list open questions = (%d, %v)", len(questions), err)
	}
	question := questions[0]
	if question.Source != domain.ExecutionQuestionAgentEvent || question.ExternalID != "event-1" {
		t.Fatalf("question = %#v", question)
	}
	if want := []string{"rebase", "merge"}; !reflect.DeepEqual(question.Options, want) {
		t.Fatalf("options = %v, want %v", question.Options, want)
	}

	command, err := s.ResolveExecutionQuestion(ctx, domain.ExecutionQuestionResolution{
		QuestionID: question.ID, Answer: "rebase", AnsweredBy: "operator",
		CommandID: "answer-1", CommandType: domain.ExecutionCommandSendMessage,
		PayloadJSON: `{"questionId":"` + question.ID + `","message":"rebase"}`,
		AuditType:   "execution.question_answered", DecidedAt: at,
	})
	if err != nil {
		t.Fatalf("resolve question: %v", err)
	}
	// The delivering command lands on the host the session is bound to, behind the
	// start command that is already in the FIFO.
	if command.HostID != "worker-1" || command.SessionID != sessionID || command.Sequence != 2 {
		t.Fatalf("command = %#v", command)
	}
	if command.State != domain.ExecutionCommandPending {
		t.Fatalf("command state = %q, want pending: nothing is delivered inside this call", command.State)
	}
	stored, ok, err := s.GetExecutionCommand(ctx, "answer-1")
	if err != nil || !ok {
		t.Fatalf("get delivery command: ok=%v err=%v", ok, err)
	}
	if stored.Type != domain.ExecutionCommandSendMessage {
		t.Fatalf("stored command type = %q", stored.Type)
	}
	// The question leaves the inbox in the same transaction that queued its
	// delivery, so it cannot be answered twice.
	open, err := s.ListOpenExecutionQuestions(ctx)
	if err != nil || len(open) != 0 {
		t.Fatalf("open questions after answering = (%d, %v)", len(open), err)
	}
}

func TestResolveExecutionQuestionRefusesASecondAnswer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	sessionID := seedBoundSession(t, s, at)

	if _, _, err := s.OpenExecutionPermissionQuestion(ctx, domain.ExecutionPermissionQuestion{
		SessionID: sessionID, WorkItemID: "work-1",
		ExternalID: "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3", ToolName: "Bash",
		Question: "Allow Bash: rm -rf build", CreatedAt: at,
	}); err != nil {
		t.Fatalf("open permission question: %v", err)
	}
	questions, err := s.ListOpenExecutionQuestions(ctx)
	if err != nil || len(questions) != 1 {
		t.Fatalf("list open questions = (%d, %v)", len(questions), err)
	}
	question := questions[0]
	// The stored external id is the host's full request id, not a display prefix:
	// the host rejects a truncated id and treats a missing one as approve-all.
	if question.ExternalID != "perm_2f6c9a4b8e1d0f37a5c2b9e4d8f1a6c3" {
		t.Fatalf("external id = %q, want the full host request id", question.ExternalID)
	}

	resolution := domain.ExecutionQuestionResolution{
		QuestionID: question.ID, Answer: "allow", AnsweredBy: "operator",
		CommandID: "decision-1", CommandType: domain.ExecutionCommandAnswerPermission,
		PayloadJSON: `{"requestId":"` + question.ExternalID + `"}`, DecidedAt: at,
	}
	if _, err := s.ResolveExecutionQuestion(ctx, resolution); err != nil {
		t.Fatalf("first decision: %v", err)
	}
	resolution.CommandID = "decision-2"
	_, err = s.ResolveExecutionQuestion(ctx, resolution)
	if !errors.Is(err, domain.ErrExecutionQuestionNotOpen) {
		t.Fatalf("second decision err = %v, want ErrExecutionQuestionNotOpen", err)
	}
	// The rejected retry must leave no command behind: a duplicate decision on the
	// wire is a second answer the host would act on.
	if _, ok, err := s.GetExecutionCommand(ctx, "decision-2"); err != nil || ok {
		t.Fatalf("second command persisted: ok=%v err=%v", ok, err)
	}
}

func TestResolveExecutionQuestionRefusesASessionWithNoHost(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)

	if _, _, err := s.OpenExecutionAgentQuestion(ctx, domain.ExecutionAgentQuestion{
		SessionID: "local-1", EventID: "event-1", Question: "Which branch?", CreatedAt: at,
	}); err != nil {
		t.Fatalf("open agent question: %v", err)
	}
	questions, err := s.ListOpenExecutionQuestions(ctx)
	if err != nil || len(questions) != 1 {
		t.Fatalf("list open questions = (%d, %v)", len(questions), err)
	}
	_, err = s.ResolveExecutionQuestion(ctx, domain.ExecutionQuestionResolution{
		QuestionID: questions[0].ID, Answer: "main", CommandID: "answer-1",
		CommandType: domain.ExecutionCommandSendMessage, PayloadJSON: `{}`, DecidedAt: at,
	})
	if !errors.Is(err, domain.ErrSessionNotRemote) {
		t.Fatalf("err = %v, want ErrSessionNotRemote", err)
	}
	// The question stays open: there is nothing wrong with it, only with trying to
	// deliver its answer to a host that does not exist.
	open, err := s.ListOpenExecutionQuestions(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("open questions = (%d, %v)", len(open), err)
	}
}

func TestGetExecutionQuestionDistinguishesMissingFromAnswered(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	sessionID := seedBoundSession(t, s, at)

	if _, _, err := s.OpenExecutionAgentQuestion(ctx, domain.ExecutionAgentQuestion{
		SessionID: sessionID, EventID: "event-1", Question: "Proceed?", CreatedAt: at,
	}); err != nil {
		t.Fatalf("open agent question: %v", err)
	}
	questions, err := s.ListOpenExecutionQuestions(ctx)
	if err != nil || len(questions) != 1 {
		t.Fatalf("list open questions = (%d, %v)", len(questions), err)
	}
	id := questions[0].ID

	if _, _, err := s.GetExecutionQuestion(ctx, "nope"); err != nil {
		t.Fatalf("get unknown question: %v", err)
	}
	if _, found, _ := s.GetExecutionQuestion(ctx, "nope"); found {
		t.Fatal("an unknown id must report not found")
	}
	if _, err := s.ResolveExecutionQuestion(ctx, domain.ExecutionQuestionResolution{
		QuestionID: id, Answer: "yes", CommandID: "answer-1",
		CommandType: domain.ExecutionCommandSendMessage, PayloadJSON: `{}`, DecidedAt: at,
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// An answered question is still fetchable. That is what lets the API tell a
	// human who clicked twice "already answered" instead of "no such question".
	got, found, err := s.GetExecutionQuestion(ctx, id)
	if err != nil || !found {
		t.Fatalf("get answered question: found=%v err=%v", found, err)
	}
	if got.ID != id {
		t.Fatalf("question = %#v", got)
	}
}
