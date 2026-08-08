package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// These DTOs intentionally mirror the daemon's work-item wire contract rather
// than importing controller types across the CLI boundary.
type createWorkItemRequest struct {
	ProjectID          string   `json:"projectId"`
	ParentWorkItemID   string   `json:"parentWorkItemId,omitempty"`
	Title              string   `json:"title"`
	Body               string   `json:"body,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	AllowedScope       []string `json:"allowedScope,omitempty"`
	ExcludedScope      []string `json:"excludedScope,omitempty"`
	RiskLevel          string   `json:"riskLevel,omitempty"`
	PolicyProfileID    string   `json:"policyProfileId,omitempty"`
	Priority           int      `json:"priority,omitempty"`
	CreatedBy          string   `json:"createdBy,omitempty"`
}

type approveWorkItemRequest struct {
	Approver string `json:"approver"`
}

type workItemDTO struct {
	ID                 string     `json:"id"`
	ProjectID          string     `json:"projectId"`
	ParentWorkItemID   string     `json:"parentWorkItemId,omitempty"`
	Title              string     `json:"title"`
	Body               string     `json:"body"`
	AcceptanceCriteria []string   `json:"acceptanceCriteria"`
	AllowedScope       []string   `json:"allowedScope"`
	ExcludedScope      []string   `json:"excludedScope"`
	RiskLevel          string     `json:"riskLevel"`
	PolicyProfileID    string     `json:"policyProfileId,omitempty"`
	ApprovalState      string     `json:"approvalState"`
	LifecycleFact      string     `json:"lifecycleFact"`
	Priority           int        `json:"priority"`
	CreatedByType      string     `json:"createdByType"`
	CreatedByID        string     `json:"createdById,omitempty"`
	ApprovedBy         string     `json:"approvedBy,omitempty"`
	ApprovedAt         *time.Time `json:"approvedAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type workItemEnvelope struct {
	WorkItem workItemDTO `json:"workItem"`
}

type listWorkItemsResponse struct {
	WorkItems []workItemDTO `json:"workItems"`
}

func newWorkItemCommand(ctx *commandContext) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "work-item",
		Short: "Create, approve, and list work items",
		Args:  noArgs,
	}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "print the structured response as JSON")
	cmd.AddCommand(newWorkItemAddCommand(ctx, &jsonOutput))
	cmd.AddCommand(newWorkItemApproveCommand(ctx, &jsonOutput))
	cmd.AddCommand(newWorkItemListCommand(ctx, &jsonOutput))
	return cmd
}

func newWorkItemAddCommand(ctx *commandContext, jsonOutput *bool) *cobra.Command {
	req := createWorkItemRequest{RiskLevel: "normal", Priority: 100, CreatedBy: "operator"}
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Create a draft work item",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req.ProjectID = strings.TrimSpace(req.ProjectID)
			req.Title = strings.TrimSpace(req.Title)
			if req.ProjectID == "" {
				return usageError{errors.New("--project is required")}
			}
			if req.Title == "" {
				return usageError{errors.New("--title is required")}
			}
			var out workItemEnvelope
			if err := ctx.postJSON(cmd.Context(), "work-items", req, &out); err != nil {
				return err
			}
			if *jsonOutput {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Created work item %s (%s)\n", out.WorkItem.ID, out.WorkItem.ApprovalState)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.ProjectID, "project", "", "project id (required)")
	f.StringVar(&req.ParentWorkItemID, "parent", "", "parent work-item id")
	f.StringVar(&req.Title, "title", "", "work-item title (required)")
	f.StringVar(&req.Body, "body", "", "work-item description")
	f.StringSliceVar(&req.AcceptanceCriteria, "acceptance", nil, "acceptance criterion (repeatable)")
	f.StringSliceVar(&req.AllowedScope, "allow", nil, "allowed path or scope (repeatable)")
	f.StringSliceVar(&req.ExcludedScope, "exclude", nil, "excluded path or scope (repeatable)")
	f.StringVar(&req.RiskLevel, "risk", "normal", "risk level")
	f.StringVar(&req.PolicyProfileID, "policy-profile", "", "policy profile id")
	f.IntVar(&req.Priority, "priority", 100, "priority (lower sorts first)")
	f.StringVar(&req.CreatedBy, "by", "operator", "creator identity recorded in the audit fact")
	return cmd
}

func newWorkItemApproveCommand(ctx *commandContext, jsonOutput *bool) *cobra.Command {
	req := approveWorkItemRequest{Approver: "operator"}
	cmd := &cobra.Command{
		Use:   "approve <work-item-id>",
		Short: "Approve a draft or proposed work item",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			if id == "" {
				return usageError{errors.New("work-item id must not be empty")}
			}
			req.Approver = strings.TrimSpace(req.Approver)
			if req.Approver == "" {
				return usageError{errors.New("--by must not be empty")}
			}
			var out workItemEnvelope
			path := "work-items/" + url.PathEscape(id) + "/approval"
			if err := ctx.postJSON(cmd.Context(), path, req, &out); err != nil {
				return err
			}
			if *jsonOutput {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Approved work item %s by %s\n", out.WorkItem.ID, out.WorkItem.ApprovedBy)
			return err
		},
	}
	cmd.Flags().StringVar(&req.Approver, "by", "operator", "approver identity recorded in the approval fact")
	return cmd
}

func newWorkItemListCommand(ctx *commandContext, jsonOutput *bool) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List one project's work items",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectID = strings.TrimSpace(projectID)
			if projectID == "" {
				return usageError{errors.New("--project is required")}
			}
			query := url.Values{"projectId": []string{projectID}}
			var out listWorkItemsResponse
			if err := ctx.getJSON(cmd.Context(), "work-items?"+query.Encode(), &out); err != nil {
				return err
			}
			if *jsonOutput {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			if len(out.WorkItems) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No work items.")
				return err
			}
			for _, item := range out.WorkItems {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%d\t%s\n",
					item.ID, item.ApprovalState, item.LifecycleFact, item.Priority, item.Title); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "project id (required)")
	return cmd
}
