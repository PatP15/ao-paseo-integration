import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { ArrowLeft, Check, Plus, Rocket, X } from "lucide-react";
import { type ComponentProps, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { CenterPanelShell } from "./CenterPanelShell";
import { DispatchWorkItemDialog } from "./DispatchWorkItemDialog";
import { TopbarButton, topbarProjectLabelClass } from "./TopbarButton";
import { Badge } from "./ui/badge";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";

type WorkItem = components["schemas"]["WorkItemResponse"];

export function workItemsQueryKey(projectId: string) {
	return ["work-items", projectId] as const;
}

function approvalLabel(item: WorkItem, t: TFunction): string {
	switch (item.approvalState) {
		case "draft":
			return t("workItems.state.draft");
		case "proposed":
			return t("workItems.state.proposed");
		case "approved":
			return t("workItems.state.approved");
		case "rejected":
			return t("workItems.state.rejected");
		default:
			return item.approvalState;
	}
}

function lifecycleLabel(item: WorkItem, t: TFunction): string {
	switch (item.lifecycleFact) {
		case "open":
			return t("workItems.lifecycle.open");
		case "in_progress":
			return t("workItems.lifecycle.inProgress");
		case "done":
			return t("workItems.lifecycle.done");
		case "cancelled":
			return t("workItems.lifecycle.cancelled");
		default:
			return item.lifecycleFact;
	}
}

// Badge already owns these tones (success/error/outline); the pill only has to
// pick one, not restate the border and text colours.
function approvalVariant(state: WorkItem["approvalState"]): ComponentProps<typeof Badge>["variant"] {
	switch (state) {
		case "approved":
			return "success";
		case "rejected":
			return "error";
		default:
			return "outline";
	}
}

export function WorkItemsView({ projectId }: { projectId: string }) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const [createOpen, setCreateOpen] = useState(false);
	const [dispatchItem, setDispatchItem] = useState<WorkItem | null>(null);
	const [decisionError, setDecisionError] = useState<string | null>(null);

	const itemsQuery = useQuery({
		queryKey: workItemsQueryKey(projectId),
		queryFn: async (): Promise<WorkItem[]> => {
			const { data, error } = await apiClient.GET("/api/v1/work-items", {
				params: { query: { projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.workItems;
		},
		retry: 1,
	});

	const decideMutation = useMutation({
		mutationFn: async ({ id, decision }: { id: string; decision: "approved" | "rejected" }) => {
			// No approver named: the daemon records the OS user running it.
			const { error } = await apiClient.POST("/api/v1/work-items/{id}/approval", {
				params: { path: { id } },
				body: { decision },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: () => {
			setDecisionError(null);
			void queryClient.invalidateQueries({ queryKey: workItemsQueryKey(projectId) });
		},
		onError: (error: unknown) =>
			setDecisionError(error instanceof Error ? error.message : t("workItems.decisionFailed")),
	});

	const items = itemsQuery.data ?? [];

	return (
		<CenterPanelShell>
			<div className="center-panel-titlebar flex h-toolbar shrink-0 items-center gap-2 border-b border-border-strong pr-4">
				<TopbarButton
					aria-label={t("workItems.backToBoard")}
					onClick={() => void navigate({ to: "/projects/$projectId", params: { projectId } })}
				>
					<ArrowLeft className="size-icon-md" aria-hidden="true" />
				</TopbarButton>
				<span className={topbarProjectLabelClass}>{t("workItems.title")}</span>
				<div className="min-w-0 flex-1" />
				<TopbarButton aria-label={t("workItems.create")} variant="accent" onClick={() => setCreateOpen(true)}>
					<Plus className="size-icon-md" aria-hidden="true" />
					{t("workItems.create")}
				</TopbarButton>
			</div>
			<div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
				{itemsQuery.isLoading ? (
					<p className="text-sm text-settings-muted">{t("workItems.loading")}</p>
				) : itemsQuery.isError ? (
					<p className="text-sm text-error">
						{itemsQuery.error instanceof Error ? itemsQuery.error.message : t("workItems.loadFailed")}
					</p>
				) : items.length === 0 ? (
					<p className="text-sm text-settings-muted">{t("workItems.empty")}</p>
				) : (
					<div className="mx-auto flex w-full max-w-3xl flex-col gap-2">
						{decisionError ? (
							<p className="text-xs text-error" role="alert">
								{decisionError}
							</p>
						) : null}
						{items.map((item) => {
							const decidable = item.approvalState === "draft" || item.approvalState === "proposed";
							const dispatchable = item.approvalState === "approved" && item.lifecycleFact === "open";
							return (
								<div
									key={item.id}
									className="rounded-(--radius-settings-row) border border-(--color-border-settings-input) bg-(--color-bg-settings-row) px-4 py-3"
								>
									<div className="flex items-start justify-between gap-3">
										<div className="min-w-0">
											<p className="truncate text-sm font-medium text-settings-label">{item.title}</p>
											{item.body ? (
												<p className="mt-0.5 line-clamp-2 text-xs text-settings-muted">{item.body}</p>
											) : null}
											<div className="mt-1.5 flex items-center gap-1.5">
												<Badge variant={approvalVariant(item.approvalState)} className="text-caption">
													{approvalLabel(item, t)}
												</Badge>
												<Badge variant="outline" className="text-caption text-settings-muted">
													{lifecycleLabel(item, t)}
												</Badge>
												{item.approvedBy ? (
													<span className="text-caption text-settings-muted">
														{t("workItems.decidedBy", { name: item.approvedBy })}
													</span>
												) : null}
											</div>
										</div>
										{dispatchable ? (
											<div className="flex shrink-0 items-center gap-2">
												<button
													type="button"
													className="settings-option-trigger inline-flex items-center gap-1.5"
													onClick={() => setDispatchItem(item)}
												>
													<Rocket className="size-icon-base" aria-hidden="true" />
													{t("workItems.dispatch")}
												</button>
											</div>
										) : null}
										{decidable ? (
											<div className="flex shrink-0 items-center gap-2">
												<button
													type="button"
													className="settings-option-trigger inline-flex items-center gap-1.5"
													disabled={decideMutation.isPending}
													onClick={() => decideMutation.mutate({ id: item.id, decision: "approved" })}
												>
													<Check className="size-icon-base" aria-hidden="true" />
													{t("workItems.approve")}
												</button>
												<button
													type="button"
													className="settings-option-trigger inline-flex items-center gap-1.5"
													disabled={decideMutation.isPending}
													onClick={() => decideMutation.mutate({ id: item.id, decision: "rejected" })}
												>
													<X className="size-icon-base" aria-hidden="true" />
													{t("workItems.reject")}
												</button>
											</div>
										) : null}
									</div>
								</div>
							);
						})}
					</div>
				)}
			</div>
			<CreateWorkItemDialog projectId={projectId} open={createOpen} onOpenChange={setCreateOpen} />
			<DispatchWorkItemDialog
				projectId={projectId}
				workItem={dispatchItem}
				open={dispatchItem !== null}
				onOpenChange={(next) => {
					if (!next) setDispatchItem(null);
				}}
			/>
		</CenterPanelShell>
	);
}

function CreateWorkItemDialog({
	projectId,
	open,
	onOpenChange,
}: {
	projectId: string;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [title, setTitle] = useState("");
	const [body, setBody] = useState("");

	useEffect(() => {
		if (!open) {
			setTitle("");
			setBody("");
		}
	}, [open]);

	const createMutation = useMutation({
		mutationFn: async () => {
			const { error } = await apiClient.POST("/api/v1/work-items", {
				body: { projectId, title: title.trim(), body: body.trim() },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({ queryKey: workItemsQueryKey(projectId) });
			onOpenChange(false);
		},
	});

	const canSubmit = title.trim() !== "" && !createMutation.isPending;

	return (
		<Dialog open={open} onOpenChange={(next) => !createMutation.isPending && onOpenChange(next)}>
			<DialogContent className={settingsDialogContentClass}>
				<DialogHeader className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">{t("workItems.createTitle")}</DialogTitle>
					<DialogDescription>{t("workItems.createDescription")}</DialogDescription>
				</DialogHeader>
				<form
					className="flex flex-col gap-4 px-1"
					onSubmit={(event) => {
						event.preventDefault();
						if (canSubmit) createMutation.mutate();
					}}
				>
					<div className="flex flex-col gap-1.5">
						<label className="text-xs font-medium text-settings-label" htmlFor="workItemTitle">
							{t("workItems.fieldTitle")}
						</label>
						<input
							id="workItemTitle"
							className="settings-inline-input w-full"
							value={title}
							onChange={(e) => setTitle(e.target.value)}
							autoFocus
						/>
					</div>
					<div className="flex flex-col gap-1.5">
						<label className="text-xs font-medium text-settings-label" htmlFor="workItemBody">
							{t("workItems.fieldBody")}
						</label>
						<textarea
							id="workItemBody"
							className="settings-inline-input min-h-24 w-full resize-y"
							value={body}
							onChange={(e) => setBody(e.target.value)}
						/>
					</div>
					{createMutation.isError ? (
						<p className="text-xs text-error" role="alert">
							{createMutation.error instanceof Error ? createMutation.error.message : t("workItems.createFailed")}
						</p>
					) : null}
					<div className={settingsDialogFooterClass}>
						<button
							type="submit"
							className="settings-footer-button settings-footer-button-primary"
							disabled={!canSubmit}
						>
							{createMutation.isPending ? t("workItems.creating") : t("workItems.create")}
						</button>
					</div>
				</form>
			</DialogContent>
		</Dialog>
	);
}
