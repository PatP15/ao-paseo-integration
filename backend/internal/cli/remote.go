package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// The CLI mirrors the daemon's execution DTOs by hand, matching the boundary the
// rest of this package keeps. Field names must stay identical to
// controllers.*ExecutionHost*/*Execution* — dto_drift_e2e_test.go drives the real
// controllers over real HTTP to prove they do.
type registerHostRequest struct {
	Name                  string   `json:"name"`
	Transport             string   `json:"transport"`
	Endpoint              string   `json:"endpoint"`
	EndpointSecretRef     string   `json:"endpointSecretRef,omitempty"`
	TrustZone             string   `json:"trustZone"`
	Enabled               bool     `json:"enabled"`
	MaxConcurrentSessions int      `json:"maxConcurrentSessions"`
	RequiresNoMCP         bool     `json:"requiresNoMcp"`
	RequiresNoRelay       bool     `json:"requiresNoRelay,omitempty"`
	Capabilities          []string `json:"capabilities,omitempty"`
}

type executionHostDTO struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Transport             string    `json:"transport"`
	Endpoint              string    `json:"endpoint"`
	TrustZone             string    `json:"trustZone"`
	Enabled               bool      `json:"enabled"`
	MaxConcurrentSessions int       `json:"maxConcurrentSessions"`
	ActiveSessions        int       `json:"activeSessions"`
	Capabilities          []string  `json:"capabilities"`
	Reachable             bool      `json:"reachable"`
	ServerID              string    `json:"serverId"`
	PaseoVersion          string    `json:"paseoVersion"`
	LastProbeError        string    `json:"lastProbeError,omitempty"`
	LastFailedProbeAt     time.Time `json:"lastFailedProbeAt,omitempty"`
}

type listHostsResponse struct {
	Hosts []executionHostDTO `json:"hosts"`
}

type hostEnvelope struct {
	Host executionHostDTO `json:"host"`
}

type dispatchRequest struct {
	WorkItemID           string   `json:"workItemId"`
	ProjectID            string   `json:"projectId"`
	TrustZone            string   `json:"trustZone"`
	RequiredCapabilities []string `json:"requiredCapabilities,omitempty"`
	IssueID              string   `json:"issueId,omitempty"`
	Harness              string   `json:"harness"`
	DisplayName          string   `json:"displayName,omitempty"`
	Branch               string   `json:"branch"`
	Provider             string   `json:"provider"`
	Model                string   `json:"model,omitempty"`
	Mode                 string   `json:"mode,omitempty"`
	Prompt               string   `json:"prompt"`
}

type dispatchResponse struct {
	SessionID      string `json:"sessionId"`
	HostID         string `json:"hostId"`
	WorkspaceTitle string `json:"workspaceTitle"`
	IntentID       string `json:"intentId"`
	Attempt        int    `json:"attempt"`
	CommandID      string `json:"commandId"`
	CommandState   string `json:"commandState"`
}

type executionQuestionDTO struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"sessionId"`
	WorkItemID     string    `json:"workItemId,omitempty"`
	Source         string    `json:"source"`
	ExternalID     string    `json:"externalId"`
	Question       string    `json:"question"`
	Recommendation string    `json:"recommendation,omitempty"`
	Options        []string  `json:"options"`
	CreatedAt      time.Time `json:"createdAt"`
}

type listQuestionsResponse struct {
	Questions []executionQuestionDTO `json:"questions"`
}

type answerQuestionRequest struct {
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answeredBy,omitempty"`
}

type decidePermissionRequest struct {
	Decision  string `json:"decision"`
	RequestID string `json:"requestId,omitempty"`
	Note      string `json:"note,omitempty"`
	DecidedBy string `json:"decidedBy,omitempty"`
}

type executionDecisionDTO struct {
	QuestionID   string `json:"questionId"`
	SessionID    string `json:"sessionId"`
	CommandID    string `json:"commandId"`
	CommandType  string `json:"commandType"`
	CommandState string `json:"commandState"`
}

// newRemoteCommand builds `ao remote`: the operator surface for remote execution
// hosts, dispatch, and the human inbox.
//
// It is one command group rather than three top-level verbs so the whole feature
// occupies a single line of the root command, which is what keeps this fork's
// weekly rebase cheap.
func newRemoteCommand(ctx *commandContext) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage remote execution hosts, dispatch, and the human inbox",
		Long: "Inspect and drive AO's remote execution control plane.\n\n" +
			"Hosts are registered by hand and selected by AO, never named at dispatch time.\n" +
			"The inbox holds two kinds of item: agent questions, answered with text, and\n" +
			"host permission requests, which take an explicit allow or deny.",
		Args: noArgs,
	}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "print the structured response as JSON")

	cmd.AddCommand(newRemoteHostsCommand(ctx, &jsonOutput))
	cmd.AddCommand(newRemoteRegisterCommand(ctx, &jsonOutput))
	cmd.AddCommand(newRemoteDispatchCommand(ctx, &jsonOutput))
	cmd.AddCommand(newRemoteInboxCommand(ctx, &jsonOutput))
	cmd.AddCommand(newRemoteAnswerCommand(ctx, &jsonOutput))
	cmd.AddCommand(newRemoteDecisionCommands(ctx, &jsonOutput)...)
	return cmd
}

func newRemoteHostsCommand(ctx *commandContext, jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "hosts",
		Short: "List registered remote execution hosts",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out listHostsResponse
			if err := ctx.getJSON(cmd.Context(), "execution/hosts", &out); err != nil {
				return err
			}
			if *jsonOutput {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			if len(out.Hosts) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No execution hosts registered.")
				return err
			}
			for _, host := range out.Hosts {
				state := "offline"
				if host.Reachable {
					state = "online"
				}
				if !host.Enabled {
					state += ", disabled"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%d/%d\t%s\n",
					host.ID, host.Name, host.Endpoint, host.TrustZone,
					host.ActiveSessions, host.MaxConcurrentSessions, state); err != nil {
					return err
				}
				if host.LastProbeError != "" && !host.Reachable {
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\tlast probe error: %s\n", host.LastProbeError); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

func newRemoteRegisterCommand(ctx *commandContext, jsonOutput *bool) *cobra.Command {
	req := registerHostRequest{RequiresNoMCP: true, RequiresNoRelay: true}
	var disabled, allowRelay bool
	cmd := &cobra.Command{
		Use:   "register <host-id>",
		Short: "Register or replace a remote execution host",
		Long: "Register a host AO may dispatch to.\n\n" +
			"--endpoint must contain a colon: the remote CLI resolves a colonless host to\n" +
			"nothing and falls through to the local daemon, which would run remote work on\n" +
			"this machine. Pass credentials as --secret-ref, never inside the endpoint.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Enabled = !disabled
			req.RequiresNoRelay = !allowRelay
			hostID := strings.TrimSpace(args[0])
			if hostID == "" {
				return usageError{errors.New("host id must not be empty")}
			}
			var out hostEnvelope
			if err := ctx.putJSON(cmd.Context(), "execution/hosts/"+url.PathEscape(hostID), req, &out); err != nil {
				return err
			}
			if *jsonOutput {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Registered host %s (%s, %s, max %d sessions)\n",
				out.Host.ID, out.Host.Endpoint, out.Host.TrustZone, out.Host.MaxConcurrentSessions)
			return err
		},
	}
	cmd.Flags().StringVar(&req.Name, "name", "", "display name")
	cmd.Flags().StringVar(&req.Transport, "transport", "tailscale", "transport: local, tailscale, lan, or paseo_relay")
	cmd.Flags().StringVar(&req.Endpoint, "endpoint", "", "host string for the remote daemon, e.g. worker.example.ts.net:6780")
	cmd.Flags().StringVar(&req.EndpointSecretRef, "secret-ref", "", "reference to the stored credential (never the credential)")
	cmd.Flags().StringVar(&req.TrustZone, "trust-zone", "", "trust zone: hobby, work, or mixed")
	cmd.Flags().IntVar(&req.MaxConcurrentSessions, "max-sessions", 4, "maximum concurrent sessions on this host")
	cmd.Flags().StringSliceVar(&req.Capabilities, "capability", nil, "routable capability (repeatable)")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "register the host without making it dispatchable")
	cmd.Flags().BoolVar(&allowRelay, "allow-relay", false, "the host's daemon may keep its relay enabled")
	return cmd
}

func newRemoteDispatchCommand(ctx *commandContext, jsonOutput *bool) *cobra.Command {
	var req dispatchRequest
	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Dispatch one approved work item to a routed host",
		Long: "Place one attempt of an approved work item on a host AO selects.\n\n" +
			"The host is chosen from the registry by trust zone, project binding, required\n" +
			"capabilities, and free capacity. Nothing remote happens before this returns:\n" +
			"the command is queued durably and delivered by the daemon.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out dispatchResponse
			if err := ctx.postJSON(cmd.Context(), "execution/dispatch", req, &out); err != nil {
				return err
			}
			if *jsonOutput {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"Queued %s on host %s (attempt %d, command %s %s)\n",
				out.SessionID, out.HostID, out.Attempt, out.CommandID, out.CommandState)
			return err
		},
	}
	cmd.Flags().StringVar(&req.WorkItemID, "work-item", "", "approved work item id")
	cmd.Flags().StringVar(&req.ProjectID, "project", "", "project id")
	cmd.Flags().StringVar(&req.TrustZone, "trust-zone", "", "trust zone the host must belong to: hobby, work, or mixed")
	cmd.Flags().StringSliceVar(&req.RequiredCapabilities, "capability", nil, "capability the host must expose (repeatable)")
	cmd.Flags().StringVar(&req.IssueID, "issue", "", "tracker issue id")
	cmd.Flags().StringVar(&req.Harness, "harness", "", "AO harness recorded for the session")
	cmd.Flags().StringVar(&req.DisplayName, "name", "", "session display name")
	cmd.Flags().StringVar(&req.Branch, "branch", "", "branch the remote agent works on")
	cmd.Flags().StringVar(&req.Provider, "provider", "", "remote provider to launch, e.g. claude or codex")
	cmd.Flags().StringVar(&req.Model, "model", "", "provider model")
	cmd.Flags().StringVar(&req.Mode, "mode", "", "provider permission mode")
	cmd.Flags().StringVar(&req.Prompt, "prompt", "", "work instructions for the agent")
	return cmd
}

func newRemoteInboxCommand(ctx *commandContext, jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "inbox",
		Short: "List open agent questions and pending host permission requests",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out listQuestionsResponse
			if err := ctx.getJSON(cmd.Context(), "execution/questions", &out); err != nil {
				return err
			}
			if *jsonOutput {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			if len(out.Questions) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "Inbox is empty.")
				return err
			}
			for _, question := range out.Questions {
				verb := "answer"
				if question.Source == "paseo_permission" {
					verb = "allow / deny"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n\t%s\n",
					question.ID, question.SessionID, question.Source, verb, question.Question); err != nil {
					return err
				}
				if question.Recommendation != "" {
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\tagent suggests: %s\n", question.Recommendation); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

func newRemoteAnswerCommand(ctx *commandContext, jsonOutput *bool) *cobra.Command {
	var answeredBy string
	cmd := &cobra.Command{
		Use:   "answer <question-id> <text>",
		Short: "Answer an agent-authored question with text",
		Long: "Answer an agent's question.\n\n" +
			"This works only on an agent question. A host permission request is refused:\n" +
			"the agent is paused on a host-side prompt that text cannot release, so use\n" +
			"`ao remote allow` or `ao remote deny` instead.",
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := ctx.answerRemoteQuestion(cmd, args[0], answerQuestionRequest{
				Answer: args[1], AnsweredBy: answeredBy,
			})
			if err != nil {
				return err
			}
			if *jsonOutput {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Answer queued for %s (command %s %s)\n",
				out.SessionID, out.CommandID, out.CommandState)
			return err
		},
	}
	cmd.Flags().StringVar(&answeredBy, "by", "", "who answered, recorded in the audit log")
	return cmd
}

// newRemoteDecisionCommands builds `allow` and `deny`. There are exactly two,
// with no scope flag: the host enforces one pending request at a time and offers
// no durable per-tool grant, so a wider-sounding option would be a promise AO
// could not keep.
func newRemoteDecisionCommands(ctx *commandContext, jsonOutput *bool) []*cobra.Command {
	commands := make([]*cobra.Command, 0, 2)
	for _, decision := range []string{"allow", "deny"} {
		var note, decidedBy, requestID string
		cmd := &cobra.Command{
			Use:   decision + " <question-id>",
			Short: strings.ToUpper(decision[:1]) + decision[1:] + " a pending host permission request",
			Args:  exactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				out, err := ctx.decideRemotePermission(cmd, args[0], decidePermissionRequest{
					Decision: decision, RequestID: requestID, Note: note, DecidedBy: decidedBy,
				})
				if err != nil {
					return err
				}
				if *jsonOutput {
					return writeJSON(cmd.OutOrStdout(), out)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Decision %s queued for %s (command %s %s)\n",
					decision, out.SessionID, out.CommandID, out.CommandState)
				return err
			},
		}
		cmd.Flags().StringVar(&note, "note", "", "note recorded with the decision")
		cmd.Flags().StringVar(&decidedBy, "by", "", "who decided, recorded in the audit log")
		cmd.Flags().StringVar(&requestID, "request-id", "",
			"confirm the host's full request id; the daemon rejects anything but an exact match")
		commands = append(commands, cmd)
	}
	return commands
}

func (c *commandContext) answerRemoteQuestion(
	cmd *cobra.Command, questionID string, body answerQuestionRequest,
) (executionDecisionDTO, error) {
	id := strings.TrimSpace(questionID)
	if id == "" {
		return executionDecisionDTO{}, usageError{errors.New("question id must not be empty")}
	}
	if strings.TrimSpace(body.Answer) == "" {
		return executionDecisionDTO{}, usageError{errors.New("answer must not be empty")}
	}
	var out executionDecisionDTO
	err := c.postJSON(cmd.Context(), "execution/questions/"+url.PathEscape(id)+"/answer", body, &out)
	return out, err
}

func (c *commandContext) decideRemotePermission(
	cmd *cobra.Command, questionID string, body decidePermissionRequest,
) (executionDecisionDTO, error) {
	id := strings.TrimSpace(questionID)
	if id == "" {
		return executionDecisionDTO{}, usageError{errors.New("question id must not be empty")}
	}
	var out executionDecisionDTO
	err := c.postJSON(cmd.Context(), "execution/permissions/"+url.PathEscape(id)+"/decision", body, &out)
	return out, err
}
