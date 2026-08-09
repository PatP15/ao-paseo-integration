import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, FileText, Monitor, PencilLine } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import { useUiStore } from "../../stores/ui-store";
import { SettingsSection } from "./SettingsSection";

type ProjectInstructions = components["schemas"]["ControllersProjectInstructionsResponse"];

export function projectInstructionsQueryKey(projectId: string) {
	return ["project-instructions", projectId] as const;
}

// F9: the project's committed instruction files rendered read-only — they are
// versioned code, so the only edit path is a reviewed task (Q2) — plus each
// host binding's live drift against them with a one-click fast-forward sync.
// No path in this section writes to any checkout directly.
export function ProjectInstructionsSection({ projectId }: { projectId: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const requestNewTask = useUiStore((state) => state.requestNewTask);
	const [openFile, setOpenFile] = useState<string | null>(null);
	const [syncError, setSyncError] = useState<string | null>(null);
	const [syncingHost, setSyncingHost] = useState<string | null>(null);

	const instructionsQuery = useQuery({
		queryKey: projectInstructionsQueryKey(projectId),
		queryFn: async (): Promise<ProjectInstructions> => {
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}/instructions", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			// Normalized so a malformed body can never crash the render.
			return { branch: data.branch ?? "", files: data.files ?? [], bindings: data.bindings ?? [] };
		},
		retry: 1,
		// Each read runs live per-binding repo hashing on the hosts; do not
		// re-pay that on every focus.
		staleTime: 60 * 1000,
	});

	const syncMutation = useMutation({
		mutationFn: async (hostId: string) => {
			setSyncingHost(hostId);
			const { data, error } = await apiClient.POST("/api/v1/execution/bindings/{projectId}/{hostId}/sync", {
				params: { path: { projectId, hostId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.binding;
		},
		onSettled: () => setSyncingHost(null),
		onSuccess: () => {
			setSyncError(null);
			void queryClient.invalidateQueries({ queryKey: projectInstructionsQueryKey(projectId) });
		},
		onError: (error: unknown) =>
			setSyncError(error instanceof Error ? error.message : t("instructions.syncFailed")),
	});

	const view = instructionsQuery.data;

	return (
		<SettingsSection title={t("instructions.title")} sectionId="project-instructions">
			{instructionsQuery.isLoading ? (
				<p className="px-1 text-xs text-settings-muted">{t("instructions.loading")}</p>
			) : instructionsQuery.isError ? (
				<p className="px-1 text-xs text-error">
					{instructionsQuery.error instanceof Error
						? instructionsQuery.error.message
						: t("instructions.loadFailed")}
				</p>
			) : view ? (
				<>
					{view.files.length === 0 ? (
						<p className="px-1 text-xs text-settings-muted">
							{t("instructions.empty", { branch: view.branch })}
						</p>
					) : (
						<div className="flex flex-col gap-2">
							{view.files.map((file) => {
								const expanded = openFile === file.path;
								return (
									<div
										key={file.path}
										className="rounded-(--radius-settings-row) border border-(--color-border-settings-input) bg-(--color-bg-settings-row) px-3.5 py-3"
									>
										<div className="flex items-center justify-between gap-3">
											<button
												type="button"
												className="flex min-w-0 items-center gap-2 text-sm font-medium text-settings-label"
												onClick={() => setOpenFile(expanded ? null : file.path)}
												aria-expanded={expanded}
											>
												{expanded ? (
													<ChevronDown className="size-icon-base shrink-0 text-settings-muted" aria-hidden="true" />
												) : (
													<ChevronRight className="size-icon-base shrink-0 text-settings-muted" aria-hidden="true" />
												)}
												<FileText className="size-icon-base shrink-0 text-settings-muted" aria-hidden="true" />
												<span className="truncate font-mono text-xs">{file.path}</span>
											</button>
											<button
												type="button"
												className="settings-option-trigger inline-flex shrink-0 items-center gap-1.5"
												onClick={() =>
													requestNewTask(projectId, {
														title: t("instructions.editTaskTitle", { path: file.path }),
														prompt: t("instructions.editTaskPrompt", {
															path: file.path,
															branch: view.branch,
														}),
													})
												}
											>
												<PencilLine className="size-icon-base" aria-hidden="true" />
												{t("instructions.editViaTask")}
											</button>
										</div>
										{expanded ? (
											<pre className="mt-2 max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded-md border border-(--color-border-settings-input) px-3 py-2 font-mono text-xs text-settings-muted">
												{file.content}
											</pre>
										) : null}
									</div>
								);
							})}
						</div>
					)}
					{view.bindings.length > 0 ? (
						<div className="flex flex-col gap-2">
							{view.bindings.map((binding) => (
								<div
									key={binding.hostId}
									className="rounded-(--radius-settings-row) border border-(--color-border-settings-input) bg-(--color-bg-settings-row) px-3.5 py-3"
								>
									<div className="flex items-center justify-between gap-3">
										<div className="min-w-0">
											<div className="flex items-center gap-2">
												<Monitor className="size-icon-base shrink-0 text-settings-muted" aria-hidden="true" />
												<span className="truncate text-sm font-medium text-settings-label">{binding.hostId}</span>
												{binding.error ? (
													<span className="text-xs text-error">{t("instructions.bindingError")}</span>
												) : binding.inSync ? (
													<span className="text-xs text-(--color-success)">{t("instructions.inSync")}</span>
												) : (
													<span className="text-xs text-warning">
														{t("instructions.drifted", { count: binding.driftedPaths.length })}
													</span>
												)}
											</div>
											{binding.error ? (
												<p className="mt-0.5 break-words text-xs text-error">{binding.error}</p>
											) : !binding.inSync ? (
												<p className="mt-0.5 truncate font-mono text-xs text-settings-muted">
													{binding.driftedPaths.join(", ")}
												</p>
											) : null}
										</div>
										{!binding.error && !binding.inSync ? (
											<button
												type="button"
												className="settings-option-trigger shrink-0"
												disabled={syncMutation.isPending}
												onClick={() => syncMutation.mutate(binding.hostId)}
											>
												{syncingHost === binding.hostId
													? t("instructions.syncing")
													: t("instructions.sync")}
											</button>
										) : null}
									</div>
								</div>
							))}
						</div>
					) : null}
					{syncError ? (
						<p className="px-1 text-xs text-error" role="alert">
							{syncError}
						</p>
					) : null}
				</>
			) : null}
		</SettingsSection>
	);
}
