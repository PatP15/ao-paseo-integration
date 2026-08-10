import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Monitor, PencilLine, Plus } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { executionHostsQueryOptions } from "../../hooks/useExecutionHostsQuery";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "../ui/dialog";
import { SettingsOptionMenu } from "./SettingsOptionMenu";
import { SettingsDetailRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

type Binding = components["schemas"]["ControllersExecutionBindingResponse"];

export function bindingsQueryKey(projectId: string) {
	return ["execution-bindings", projectId] as const;
}

// The section exists so "registered but unbound" cannot happen silently: a
// project with no binding has zero candidate hosts and every remote dispatch
// fails, which the first end-to-end run hit as an inscrutable CLI error. The
// repo path is per-host on purpose — the same repo lives at different paths on
// different machines — so binding is where the operator states it.
export function ComputerBindingsSection({ projectId }: { projectId: string }) {
	const { t } = useTranslation();
	const [dialogOpen, setDialogOpen] = useState(false);
	const [editingBinding, setEditingBinding] = useState<Binding | null>(null);

	const bindingsQuery = useQuery({
		queryKey: bindingsQueryKey(projectId),
		queryFn: async (): Promise<Binding[]> => {
			const { data, error } = await apiClient.GET("/api/v1/execution/bindings", {
				params: { query: { projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.bindings;
		},
		retry: 1,
	});
	const hostsQuery = useQuery(executionHostsQueryOptions);
	const hosts = hostsQuery.data ?? [];
	const bindings = bindingsQuery.data ?? [];
	const hostName = (hostId: string) => hosts.find((host) => host.id === hostId)?.name ?? hostId;

	return (
		<SettingsSection title={t("settings.bindings.title")} sectionId="computer-bindings">
			{bindingsQuery.isLoading ? (
				<p className="px-1 text-xs text-settings-muted">{t("settings.bindings.loading")}</p>
			) : bindingsQuery.isError ? (
				<p className="px-1 text-xs text-error">
					{bindingsQuery.error instanceof Error ? bindingsQuery.error.message : t("settings.bindings.loadFailed")}
				</p>
			) : bindings.length === 0 ? (
				<p className="px-1 text-xs text-warning">{t("settings.bindings.empty")}</p>
			) : (
				<div className="flex flex-col gap-1.5">
					{bindings.map((binding) => (
						<SettingsDetailRow
							key={binding.hostId}
							icon={Monitor}
							title={
								<>
									<span className="truncate">{hostName(binding.hostId)}</span>
									{!binding.enabled ? (
										<span className="text-xs text-settings-muted">{t("settings.bindings.disabled")}</span>
									) : null}
								</>
							}
							meta={
								<p className="truncate">
									{binding.hostRepoPath} ·{" "}
									{t("settings.bindings.baseBranch", {
										branch: binding.baseBranch,
									})}{" "}
									·{" "}
									{t("settings.bindings.priority", {
										priority: binding.priority,
									})}
								</p>
							}
							actions={
								<button
									type="button"
									className="settings-option-trigger"
									onClick={() => {
										setEditingBinding(binding);
										setDialogOpen(true);
									}}
								>
									<PencilLine className="size-icon-base" aria-hidden="true" />
									{t("settings.bindings.edit")}
								</button>
							}
						/>
					))}
				</div>
			)}
			<button
				type="button"
				className="settings-row-bar w-full justify-start text-left text-sm leading-5 text-settings-label transition-colors hover:bg-settings-menu-selected"
				onClick={() => {
					setEditingBinding(null);
					setDialogOpen(true);
				}}
			>
				<Plus className="size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" />
				{t("settings.bindings.bind")}
			</button>
			<BindComputerDialog
				projectId={projectId}
				binding={editingBinding}
				open={dialogOpen}
				onOpenChange={(next) => {
					setDialogOpen(next);
					if (!next) setEditingBinding(null);
				}}
			/>
		</SettingsSection>
	);
}

function BindComputerDialog({
	projectId,
	binding,
	open,
	onOpenChange,
}: {
	projectId: string;
	binding: Binding | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const hostsQuery = useQuery({ ...executionHostsQueryOptions, enabled: open });
	const hosts = (hostsQuery.data ?? []).filter((host) => host.enabled || host.id === binding?.hostId);
	const [hostId, setHostId] = useState("");
	const [hostRepoPath, setHostRepoPath] = useState("");
	const [baseBranch, setBaseBranch] = useState("");
	const [priority, setPriority] = useState("100");
	const [enabled, setEnabled] = useState(true);

	useEffect(() => {
		if (!open) return;
		setHostId(binding?.hostId ?? "");
		setHostRepoPath(binding?.hostRepoPath ?? "");
		setBaseBranch(binding?.baseBranch ?? "");
		setPriority(String(binding?.priority ?? 100));
		setEnabled(binding?.enabled ?? true);
	}, [open, binding]);

	const bindMutation = useMutation({
		mutationFn: async () => {
			const { error } = await apiClient.PUT("/api/v1/execution/projects/{projectId}/hosts/{hostId}", {
				params: { path: { projectId, hostId } },
				body: {
					hostRepoPath: hostRepoPath.trim(),
					baseBranch: baseBranch.trim() || undefined,
					priority: Number.parseInt(priority, 10),
					disabled: !enabled,
				},
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: () => {
			void queryClient.invalidateQueries({
				queryKey: bindingsQueryKey(projectId),
			});
			onOpenChange(false);
		},
	});

	const priorityValue = Number.parseInt(priority, 10);
	const canSubmit =
		hostId !== "" &&
		hostRepoPath.trim() !== "" &&
		Number.isInteger(priorityValue) &&
		priorityValue > 0 &&
		!bindMutation.isPending;

	return (
		<Dialog open={open} onOpenChange={(next) => !bindMutation.isPending && onOpenChange(next)}>
			<DialogContent className={settingsDialogContentClass}>
				<DialogHeader className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">
						{binding ? t("settings.bindings.editTitle") : t("settings.bindings.dialogTitle")}
					</DialogTitle>
					<DialogDescription>
						{binding ? t("settings.bindings.editDescription") : t("settings.bindings.dialogDescription")}
					</DialogDescription>
				</DialogHeader>
				<form
					className="flex flex-col gap-4 px-1"
					onSubmit={(event) => {
						event.preventDefault();
						if (canSubmit) bindMutation.mutate();
					}}
				>
					<div className="flex flex-col gap-1.5">
						<span className="text-xs font-medium text-settings-label">{t("settings.bindings.computer")}</span>
						<SettingsOptionMenu
							value={hostId}
							options={hosts.map((host) => ({
								value: host.id,
								label: host.name,
							}))}
							onChange={setHostId}
							placeholder={
								hosts.length === 0 ? t("settings.bindings.noComputers") : t("settings.bindings.selectComputer")
							}
							disabled={hosts.length === 0 || binding !== null}
							aria-label={t("settings.bindings.computer")}
						/>
					</div>
					<div className="flex flex-col gap-1.5">
						<label className="text-xs font-medium text-settings-label" htmlFor="bindingRepoPath">
							{t("settings.bindings.repoPath")}
						</label>
						<input
							id="bindingRepoPath"
							className="settings-inline-input w-full"
							value={hostRepoPath}
							onChange={(e) => setHostRepoPath(e.target.value)}
							placeholder={t("settings.bindings.repoPathPlaceholder")}
						/>
						<p className="text-xs text-settings-muted">{t("settings.bindings.repoPathHint")}</p>
					</div>
					<div className="flex flex-col gap-1.5">
						<label className="text-xs font-medium text-settings-label" htmlFor="bindingPriority">
							{t("settings.bindings.priorityLabel")}
						</label>
						<input
							id="bindingPriority"
							className="settings-inline-input w-full"
							inputMode="numeric"
							value={priority}
							onChange={(event) => setPriority(event.target.value)}
						/>
						<p className="text-xs text-settings-muted">{t("settings.bindings.priorityHint")}</p>
					</div>
					<label className="flex items-center gap-2 text-xs text-settings-label">
						<input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />
						{t("settings.bindings.enabled")}
					</label>
					<div className="flex flex-col gap-1.5">
						<label className="text-xs font-medium text-settings-label" htmlFor="bindingBaseBranch">
							{t("settings.bindings.baseBranchLabel")}
						</label>
						<input
							id="bindingBaseBranch"
							className="settings-inline-input w-full"
							value={baseBranch}
							onChange={(e) => setBaseBranch(e.target.value)}
							placeholder={t("settings.bindings.baseBranchPlaceholder")}
						/>
					</div>
					{bindMutation.isError ? (
						<p className="text-xs text-error" role="alert">
							{bindMutation.error instanceof Error ? bindMutation.error.message : t("settings.bindings.bindFailed")}
						</p>
					) : null}
					<div className={settingsDialogFooterClass}>
						<button
							type="submit"
							className="settings-footer-button settings-footer-button-primary"
							disabled={!canSubmit}
						>
							{bindMutation.isPending
								? binding
									? t("settings.bindings.saving")
									: t("settings.bindings.binding")
								: binding
									? t("settings.bindings.save")
									: t("settings.bindings.bind")}
						</button>
					</div>
				</form>
			</DialogContent>
		</Dialog>
	);
}
