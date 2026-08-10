import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, FileText, Monitor, PencilLine } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { useExecutionHostName } from "../../hooks/useExecutionHostsQuery";
import { apiClient, apiErrorCode, apiErrorMessage } from "../../lib/api-client";
import { useUiStore } from "../../stores/ui-store";
import { PendingLine } from "../PendingLine";
import { SettingsDetailRow } from "./SettingsRow";
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
	// A refused sync is kept as its code plus the channel's own words, not as one
	// flattened string: git's refusal is worth showing verbatim for the audit, but
	// only under a sentence that says what it means for this computer.
	const [syncError, setSyncError] = useState<{ hostId: string; code?: string; detail: string } | null>(null);
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
			if (error) {
				const failure = new Error(apiErrorMessage(error)) as Error & { code?: string };
				failure.code = apiErrorCode(error);
				throw failure;
			}
			return data.binding;
		},
		onSettled: () => setSyncingHost(null),
		onSuccess: () => {
			setSyncError(null);
			void queryClient.invalidateQueries({ queryKey: projectInstructionsQueryKey(projectId) });
		},
		onError: (error: unknown, hostId: string) =>
			setSyncError({
				hostId,
				code: (error as { code?: string })?.code,
				detail: error instanceof Error ? error.message : t("instructions.syncFailed"),
			}),
	});

	const view = instructionsQuery.data;

	return (
		<SettingsSection title={t("instructions.title")} sectionId="project-instructions">
			{instructionsQuery.isLoading ? (
				<PendingLine className="px-1 text-xs" slowHint={t("instructions.slowRead")}>
					{t("instructions.loading")}
				</PendingLine>
			) : instructionsQuery.isError ? (
				<p className="px-1 text-xs text-error">
					{instructionsQuery.error instanceof Error ? instructionsQuery.error.message : t("instructions.loadFailed")}
				</p>
			) : view ? (
				<>
					{view.files.length === 0 ? (
						<p className="px-1 text-xs text-settings-muted">{t("instructions.empty", { branch: view.branch })}</p>
					) : (
						<div className="flex flex-col gap-1.5">
							{view.files.map((file) => {
								const expanded = openFile === file.path;
								return (
									<SettingsDetailRow
										key={file.path}
										icon={FileText}
										title={
											<button
												type="button"
												className="flex min-w-0 items-center gap-2"
												onClick={() => setOpenFile(expanded ? null : file.path)}
												aria-expanded={expanded}
											>
												{expanded ? (
													<ChevronDown className="size-icon-base shrink-0 text-settings-muted" aria-hidden="true" />
												) : (
													<ChevronRight className="size-icon-base shrink-0 text-settings-muted" aria-hidden="true" />
												)}
												<span className="truncate font-mono text-xs">{file.path}</span>
											</button>
										}
										meta={
											expanded ? (
												<pre className="mt-1 max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-(--color-bg-settings-input) px-3 py-2 font-mono text-xs text-settings-muted">
													{file.content}
												</pre>
											) : null
										}
										actions={
											<button
												type="button"
												className="settings-option-trigger"
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
										}
									/>
								);
							})}
						</div>
					)}
					{view.bindings.length > 0 ? (
						<div className="flex flex-col gap-1.5">
							{/* The rows below carry three states and an action that writes to
							    another machine, so they say what they compare and which way
							    Sync moves — the same lead-with-the-explanation idiom the
							    Skills and Schedules tabs use for their own flagged rows. */}
							<p className="px-1 text-xs text-settings-muted">{t("instructions.bindingsExplanation")}</p>
							{view.bindings.map((binding) => (
								<BindingDriftRow
									key={binding.hostId}
									binding={binding}
									actions={
										!binding.error && !binding.inSync ? (
											<button
												type="button"
												className="settings-option-trigger"
												disabled={syncMutation.isPending}
												onClick={() => syncMutation.mutate(binding.hostId)}
											>
												{syncingHost === binding.hostId ? t("instructions.syncing") : t("instructions.sync")}
											</button>
										) : null
									}
								/>
							))}
						</div>
					) : null}
					{syncError ? <SyncFailureNote failure={syncError} /> : null}
				</>
			) : null}
		</SettingsSection>
	);
}

/**
 * One refused or failed Sync, told as a consequence before a transcript.
 *
 * The channel returns git's own words on purpose — AO never resolves another
 * machine's divergence, so the operator needs the real reason — but git answers
 * a refused fast-forward with several lines of `hint:` advice, which reflowed
 * into one paragraph reads as "hint: hint: git merge --no-ff hint: hint: or:"
 * with an error code stapled to the end. Lead with what it means for this
 * computer, keep the transcript underneath as sent.
 */
function SyncFailureNote({ failure }: { failure: { hostId: string; code?: string; detail: string } }) {
	const { t } = useTranslation();
	const hostName = useExecutionHostName(failure.hostId);
	const { code, detail } = failure;
	// `apiErrorMessage` staples "(CODE)" on for surfaces with nowhere else to put
	// it; here the code has already chosen the sentence above.
	const transcript = code && detail.endsWith(`(${code})`) ? detail.slice(0, -(code.length + 2)).trim() : detail;
	return (
		<div className="flex flex-col gap-1 px-1 text-xs" role="alert">
			<p className="break-words text-error">
				{code === "MAINTENANCE_REFUSED"
					? t("instructions.syncRefused", { host: hostName })
					: t("instructions.syncFailedOn", { host: hostName })}
			</p>
			<p className="whitespace-pre-wrap break-words font-mono text-settings-muted">{transcript}</p>
		</div>
	);
}

/**
 * One bound computer's drift against the project's committed instruction files.
 *
 * Named by its display name, like every other mention of the same machine — the
 * row used to print the raw host id next to a Computers section calling it
 * something else. A failed read leads with a sentence the operator can act on
 * and keeps the channel's own words underneath as detail, instead of handing
 * over `maintenance repo-status on host X: context deadline exceeded` alone.
 */
function BindingDriftRow({
	binding,
	actions,
}: {
	binding: ProjectInstructions["bindings"][number];
	actions: ReactNode;
}) {
	const { t } = useTranslation();
	const hostName = useExecutionHostName(binding.hostId);
	return (
		<SettingsDetailRow
			icon={Monitor}
			title={
				<>
					<span className="truncate">{hostName}</span>
					{binding.error ? (
						<span className="text-xs text-error">{t("instructions.bindingError")}</span>
					) : binding.inSync ? (
						<span className="text-xs text-(--color-success)">{t("instructions.inSync")}</span>
					) : (
						<span className="text-xs text-warning">
							{t("instructions.drifted", { count: binding.driftedPaths.length })}
						</span>
					)}
				</>
			}
			meta={
				binding.error ? (
					<>
						{/* Two lines, no overlap: what the failed read means for this row,
					    then the reason the daemon gave — which since `ea6b008` is itself an
					    operator-facing sentence naming the next step, so the line above no
					    longer repeats "test the connection". */}
					<p className="break-words text-error">{t("instructions.bindingErrorHint", { host: hostName })}</p>
						<p className="break-words text-settings-muted">{binding.error}</p>
					</>
				) : !binding.inSync ? (
					<p className="truncate font-mono">{binding.driftedPaths.join(", ")}</p>
				) : null
			}
			actions={actions}
		/>
	);
}
