import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { TriangleAlert, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import type { components } from "../../api/schema";
import { executionHostsQueryOptions } from "../hooks/useExecutionHostsQuery";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { harnessForProvider } from "../lib/execution-harness";
import { trustZoneLabel } from "./settings/ComputersSection";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
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
type Binding = components["schemas"]["ControllersExecutionBindingResponse"];
type Provider = components["schemas"]["ControllersExecutionProviderResponse"];
type DispatchResponse = components["schemas"]["DispatchExecutionResponse"];

const NONE = "__none__";

/**
 * The branch the remote agent works on, offered as a default.
 *
 * Slugged from the title, because `ao/wi_59e62c92-95b7-488` — a uuid sliced
 * mid-group by a 20-character cap — told the operator nothing about the work and
 * looked like a truncation bug. The id is still the fallback when a title slugs
 * to nothing (punctuation or a non-Latin script), so the field is never empty.
 */
export function branchNameFor(workItem: Pick<WorkItem, "id" | "title">): string {
	const slug = workItem.title
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "")
		.slice(0, 40)
		.replace(/-+$/g, "");
	return `ao/${slug || workItem.id.slice(0, 20)}`;
}

export function DispatchWorkItemDialog({
	projectId,
	workItem,
	open,
	onOpenChange,
}: {
	projectId: string;
	workItem: WorkItem | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const [hostId, setHostId] = useState("");
	const [provider, setProvider] = useState("");
	const [model, setModel] = useState(NONE);
	const [mode, setMode] = useState(NONE);
	const [thinking, setThinking] = useState(NONE);
	const [branch, setBranch] = useState("");
	const [prompt, setPrompt] = useState("");
	const [progress, setProgress] = useState<DispatchResponse | null>(null);
	const [commandState, setCommandState] = useState<string | null>(null);
	const [commandError, setCommandError] = useState<string | null>(null);

	useEffect(() => {
		if (open && workItem) {
			setBranch(branchNameFor(workItem));
			setPrompt(workItem.body || workItem.title);
		}
		if (!open) {
			setHostId("");
			setProvider("");
			setModel(NONE);
			setMode(NONE);
			setThinking(NONE);
			setProgress(null);
			setCommandState(null);
			setCommandError(null);
			setOverrides([]);
			setPendingGate(null);
		}
	}, [open, workItem]);

	const hostsQuery = useQuery({ ...executionHostsQueryOptions, enabled: open });
	const bindingsQuery = useQuery({
		queryKey: ["execution-bindings", projectId],
		queryFn: async (): Promise<Binding[]> => {
			const { data, error } = await apiClient.GET("/api/v1/execution/bindings", {
				params: { query: { projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.bindings;
		},
		enabled: open,
		retry: 1,
	});

	// Host selection is AO's at dispatch time; the dialog narrows to hosts that
	// COULD be selected — bound to this project and enabled — so a dispatch
	// that cannot route is impossible to attempt from here.
	const boundHosts = useMemo(() => {
		const hosts = hostsQuery.data ?? [];
		const bindings = bindingsQuery.data ?? [];
		return hosts.filter((host) => host.enabled && bindings.some((b) => b.hostId === host.id && b.enabled));
	}, [hostsQuery.data, bindingsQuery.data]);
	const host = boundHosts.find((candidate) => candidate.id === hostId);

	// Why a computer cannot take this work right now, in the router's own terms
	// (service/dispatch/router.go): it skips a host that is not online and one
	// whose live bindings already fill MaxConcurrentSessions. Both facts are in
	// the hosts read, so the picker can say so up front instead of letting the
	// dispatch fail with a refusal that names no computer.
	const unavailableReason = (candidate: (typeof boundHosts)[number]): string | null => {
		if (!candidate.reachable) return t("dispatch.computerOffline");
		if (candidate.activeSessions >= candidate.maxConcurrentSessions) {
			return t("dispatch.computerBusy", {
				active: candidate.activeSessions,
				max: candidate.maxConcurrentSessions,
			});
		}
		return null;
	};
	const availableHosts = boundHosts.filter((candidate) => unavailableReason(candidate) === null);

	const providersQuery = useQuery({
		queryKey: ["execution-providers", hostId],
		queryFn: async (): Promise<Provider[]> => {
			const { data, error } = await apiClient.GET("/api/v1/execution/hosts/{hostId}/providers", {
				params: { path: { hostId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.providers;
		},
		enabled: open && hostId !== "",
		retry: 1,
	});
	const providers = (providersQuery.data ?? []).filter((entry) => entry.status === "available");
	const providerInfo = providers.find((entry) => entry.provider === provider);
	const modelInfo = providerInfo?.models.find((entry) => entry.id === model);
	// A provider AO has no harness for cannot be dispatched: the session's
	// harness must describe what actually runs it.
	const harness = harnessForProvider(provider);

	// The host's cached skill inventory drives insertable prompt snippets. A
	// host never inventoried degrades to a plain prompt box — absence of
	// affordances, never a blocked dispatch.
	const inventoryQuery = useQuery({
		queryKey: ["execution-inventory", hostId],
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/execution/hosts/{hostId}/inventory", {
				params: { path: { hostId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.skills;
		},
		enabled: open && hostId !== "",
		retry: 1,
	});
	const skills = inventoryQuery.data ?? [];
	const [pendingGate, setPendingGate] = useState<string | null>(null);
	const [overrides, setOverrides] = useState<string[]>([]);

	const insertSkill = (skill: { name: string; description?: string; policyGated: boolean }) => {
		if (skill.policyGated && !overrides.includes(skill.name)) {
			setPendingGate(skill.name);
			return;
		}
		appendSkillSnippet(skill);
	};
	const appendSkillSnippet = (skill: { name: string; description?: string }) => {
		setPendingGate(null);
		setPrompt(
			(current) =>
				`${current.trimEnd()}\n\n${t("dispatch.skillSnippet", {
					name: skill.name,
					description: skill.description || skill.name,
				})}\n`,
		);
	};

	const dispatchMutation = useMutation({
		mutationFn: async (): Promise<DispatchResponse> => {
			if (!workItem || !host) throw new Error(t("dispatch.hostRequired"));
			if (!harness) throw new Error(t("dispatch.providerUnsupported", { provider }));
			const { data, error } = await apiClient.POST("/api/v1/execution/dispatch", {
				body: {
					workItemId: workItem.id,
					projectId,
					trustZone: host.trustZone,
					harness,
					branch: branch.trim(),
					provider,
					model: model === NONE ? undefined : model,
					mode: mode === NONE ? undefined : mode,
					settings:
						(thinking !== NONE && model !== NONE) || overrides.length > 0
							? {
									thinkingOptionId: thinking !== NONE && model !== NONE ? thinking : undefined,
									skillPolicyOverrides: overrides.length > 0 ? overrides : undefined,
								}
							: undefined,
					prompt,
				},
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: (data) => {
			setProgress(data);
			setCommandState(data.commandState);
			setCommandError(null);
			void queryClient.invalidateQueries({
				queryKey: ["work-items", projectId],
			});
			void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
		},
	});

	// Watch the queued command move through the outbox (U4): pending →
	// delivering → acknowledged (agent launched) or failed with the reason.
	useEffect(() => {
		if (!progress || commandState === "acknowledged" || commandState === "failed") return;
		const interval = setInterval(() => {
			void (async () => {
				const { data, error } = await apiClient.GET("/api/v1/execution/commands/{commandId}", {
					params: { path: { commandId: progress.commandId } },
				});
				if (!error) {
					setCommandState(data.commandState);
					setCommandError(data.lastError ?? null);
				}
			})();
		}, 2000);
		return () => clearInterval(interval);
	}, [progress, commandState]);

	const busy = dispatchMutation.isPending;
	const canSubmit =
		!busy &&
		!progress &&
		workItem !== null &&
		host !== undefined &&
		// A computer that went offline or filled up while the dialog was open
		// would refuse this dispatch; do not offer to send it there.
		unavailableReason(host) === null &&
		provider !== "" &&
		harness !== null &&
		branch.trim() !== "" &&
		prompt.trim() !== "";
	// Dispatch has five preconditions and the dialog used to refuse silently:
	// a greyed-out button with no hint at which of them was missing. Named in
	// field order, and only the first one, so the message stays a next step
	// rather than a list of complaints. The unsupported-harness case already
	// prints its own warning next to the provider select.
	const blockedReason = ((): string | null => {
		if (busy || progress || workItem === null) return null;
		// The no-bound-computer case already prints dispatch.bindHint under the
		// select, with the fix in it; do not say it twice.
		if (boundHosts.length === 0) return null;
		if (host === undefined) return t("dispatch.blockedHost");
		const unavailable = unavailableReason(host);
		if (unavailable !== null) {
			return t("dispatch.blockedComputerUnavailable", { name: host.name, reason: unavailable });
		}
		if (provider === "") {
			return providersQuery.isFetching ? null : t("dispatch.blockedProvider");
		}
		if (harness === null) return null;
		if (branch.trim() === "") return t("dispatch.blockedBranch");
		if (prompt.trim() === "") return t("dispatch.blockedPrompt");
		return null;
	})();

	const optionMenus = [
		{
			key: "provider",
			label: t("dispatch.provider"),
			value: provider,
			options: providers.map((entry) => ({
				value: entry.provider,
				label: entry.label || entry.provider,
				disabled: harnessForProvider(entry.provider) === null,
			})),
			placeholder: providersQuery.isFetching ? t("dispatch.discovering") : t("dispatch.selectProvider"),
			disabled: hostId === "" || providers.length === 0,
			onChange: (value: string) => {
				setProvider(value);
				setModel(NONE);
				setMode(NONE);
				setThinking(NONE);
			},
		},
		{
			key: "model",
			label: t("dispatch.model"),
			value: model,
			options: [
				// The trigger renders this option's label, because NONE is a real
				// value and a placeholder only shows for an unmatched one. A disabled
				// select reading "Provider default" looks like a choice already made.
				{
					value: NONE,
					label: providerInfo === undefined ? t("dispatch.awaitingProvider") : t("dispatch.providerDefault"),
				},
				...(providerInfo?.models ?? []).map((entry) => ({
					value: entry.id,
					label: entry.label || entry.id,
				})),
			],
			placeholder: t("dispatch.providerDefault"),
			disabled: providerInfo === undefined,
			onChange: (value: string) => {
				setModel(value);
				setThinking(NONE);
			},
		},
		{
			key: "mode",
			label: t("dispatch.mode"),
			value: mode,
			// Mode ids come from inspecting a live agent of this provider on the
			// host (re-derived per discovery, so it cannot drift); a host with no
			// such agent yet offers only its reported default-mode id.
			options: [
				{
					value: NONE,
					label: providerInfo === undefined ? t("dispatch.awaitingProvider") : t("dispatch.providerDefault"),
				},
				...(providerInfo?.modes.length
					? providerInfo.modes.map((entry) => ({
							value: entry.id,
							label: entry.label ? `${entry.label} (${entry.id})` : entry.id,
						}))
					: providerInfo?.defaultMode
						? [
								{
									value: providerInfo.defaultMode,
									label: providerInfo.defaultMode,
								},
							]
						: []),
			],
			placeholder: t("dispatch.providerDefault"),
			disabled: providerInfo === undefined,
			onChange: setMode,
		},
		{
			key: "thinking",
			label: t("dispatch.thinking"),
			value: thinking,
			options: [
				{
					value: NONE,
					label: modelInfo === undefined ? t("dispatch.awaitingModel") : t("dispatch.providerDefault"),
				},
				...(modelInfo?.thinkingOptionIds ?? []).map((id) => ({
					value: id,
					label: id,
				})),
			],
			placeholder: t("dispatch.providerDefault"),
			disabled: modelInfo === undefined,
			onChange: setThinking,
		},
	];

	return (
		<Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
			<DialogContent className={settingsDialogContentClass}>
				<DialogHeader className={settingsDialogHeaderClass}>
					<DialogTitle className="settings-dialog-title">
						{t("dispatch.title", { title: workItem?.title ?? "" })}
					</DialogTitle>
					<DialogDescription>{t("dispatch.description")}</DialogDescription>
				</DialogHeader>
				{progress ? (
					<div className="flex flex-col gap-3 px-1 pb-2">
						<p className="text-sm text-settings-label">
							{commandState === "acknowledged"
								? t("dispatch.launched")
								: commandState === "failed"
									? t("dispatch.failed")
									: t("dispatch.progress", {
											state: commandState ?? "pending",
										})}
						</p>
						<p className="text-xs text-settings-muted">
							{t("dispatch.sessionLine", {
								sessionId: progress.sessionId,
								host: host?.name ?? progress.hostId,
							})}
						</p>
						{commandState === "failed" && commandError ? (
							<p className="text-xs text-error" role="alert">
								{commandError}
							</p>
						) : null}
						<div className={settingsDialogFooterClass}>
							<button
								type="button"
								className="settings-footer-button settings-footer-button-primary"
								onClick={() => {
									onOpenChange(false);
									void navigate({
										to: "/projects/$projectId/sessions/$sessionId",
										params: { projectId, sessionId: progress.sessionId },
									});
								}}
							>
								{t("dispatch.openSession")}
							</button>
						</div>
					</div>
				) : (
					<form
						className="flex flex-col gap-4 px-1"
						onSubmit={(event) => {
							event.preventDefault();
							if (canSubmit) dispatchMutation.mutate();
						}}
					>
						<div className="flex flex-col gap-1.5">
							<span className="text-xs font-medium text-settings-label">{t("dispatch.computer")}</span>
							<SettingsOptionMenu
								value={hostId}
								options={boundHosts.map((entry) => {
									const reason = unavailableReason(entry);
									return {
										value: entry.id,
										label: reason === null ? entry.name : `${entry.name} — ${reason}`,
										disabled: reason !== null,
									};
								})}
								onChange={(value) => {
									setHostId(value);
									setProvider("");
									setModel(NONE);
									setMode(NONE);
									setThinking(NONE);
									// An override is a decision about a skill ON A COMPUTER: the audit
									// row records the skill together with the dispatch's hostId. Carrying
									// it across would log a decision the operator never made there, for a
									// skill that computer may not even have.
									setOverrides([]);
									setPendingGate(null);
								}}
								placeholder={boundHosts.length === 0 ? t("dispatch.noBoundComputers") : t("dispatch.selectComputer")}
								disabled={boundHosts.length === 0}
								aria-label={t("dispatch.computer")}
							/>
							{boundHosts.length === 0 && !hostsQuery.isFetching && !bindingsQuery.isFetching ? (
								<p className="text-xs text-warning">{t("dispatch.bindHint")}</p>
							) : null}
							{boundHosts.length > 0 && availableHosts.length === 0 ? (
								<p className="text-xs text-warning">{t("dispatch.noAvailableComputers")}</p>
							) : null}
							{host ? (
								<>
									<p className="text-xs text-settings-muted">
										{t("dispatch.trustZoneLine", { zone: trustZoneLabel(host.trustZone, t) })}
									</p>
									<p className="text-xs text-settings-muted">
										{t("dispatch.capacityLine", {
											active: host.activeSessions,
											max: host.maxConcurrentSessions,
										})}
									</p>
								</>
							) : null}
						</div>
						<div className="grid gap-4 sm:grid-cols-2">
							{optionMenus.map((menu) => (
								<div key={menu.key} className="flex flex-col gap-1.5">
									<span className="text-xs font-medium text-settings-label">{menu.label}</span>
									<SettingsOptionMenu
										value={menu.value}
										options={menu.options}
										onChange={menu.onChange}
										placeholder={menu.placeholder}
										disabled={menu.disabled}
										aria-label={menu.label}
									/>
								</div>
							))}
						</div>
						{provider !== "" && harness === null ? (
							<p className="text-xs text-warning" role="alert">
								{t("dispatch.providerUnsupported", { provider })}
							</p>
						) : null}
						{skills.length > 0 ? (
							<div className="flex flex-col gap-1.5">
								<span className="text-xs font-medium text-settings-label">{t("dispatch.skills")}</span>
								<div className="flex flex-wrap items-center gap-1.5">
									{skills.map((skill) => (
										<button
											key={skill.name}
											type="button"
											// Same reasoning as the question chips: a set of values
											// offered in a body area has to read as pressable.
											className="settings-chip-button"
											title={skill.description || skill.name}
											// The gate was a bare "⚠" glyph — meaning carried by a
											// character with no text alternative. The mark stays for
											// sighted scanning; the accessible name says it in words.
											aria-label={
												skill.policyGated ? t("dispatch.gatedSkillLabel", { name: skill.name }) : undefined
											}
											onClick={() => insertSkill(skill)}
										>
											{skill.name}
											{skill.policyGated ? (
												<span aria-hidden="true">{t("dispatch.gatedMark")}</span>
											) : null}
										</button>
									))}
								</div>
								<p className="text-xs text-settings-muted">{t("dispatch.skillsHint")}</p>
								{pendingGate ? (
									// The app's warning box (CreateProjectAgentSheet): icon, a title
									// stating the fact, body in reading colour. This decision sat in a
									// box bordered like an input, with the whole account of it as one
									// warning-coloured paragraph and no lead.
									<div
										role="alert"
										className="flex gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2.5 text-xs leading-body-md"
									>
										<TriangleAlert className="mt-0.5 size-icon-sm shrink-0 text-warning" aria-hidden="true" />
										<div className="flex min-w-0 flex-col gap-1.5">
											<p className="font-medium text-settings-label">
												{t("dispatch.gateTitle", { name: pendingGate, computer: host?.name ?? hostId })}
											</p>
											<p className="text-settings-muted">{t("dispatch.gateExplanation")}</p>
											<div className="flex items-center gap-2 pt-0.5">
												<button
													type="button"
													// A decision pair inside a warning box: pressable, and neither
													// one dressed as the recommended way out. The primary used to
													// read "Enable for this dispatch" — the one thing it cannot do:
													// the override is an audit fact and "alters nothing about the
													// launch" (storage/sqlite/store/execution_dispatch_store.go).
													className="settings-chip-button"
													onClick={() => {
														setOverrides((current) => [...current, pendingGate]);
														const skill = skills.find((entry) => entry.name === pendingGate);
														if (skill) appendSkillSnippet(skill);
													}}
												>
													{t("dispatch.gateInsert")}
												</button>
												{/* "Cancel" here sat a few centimetres from the footer's Cancel,
												    which abandons the whole dispatch. Same word, two scopes; this
												    one names its own. */}
												<button type="button" className="settings-chip-button" onClick={() => setPendingGate(null)}>
													{t("dispatch.gateDismiss")}
												</button>
											</div>
										</div>
									</div>
								) : null}
								{/* The decision left no trace: once inserted, the chip looked exactly as
								    it had before, so an audit fact about to be written under the
								    operator's name was invisible and could not be taken back. */}
								{overrides.length > 0 ? (
									<div className="flex flex-col gap-1.5">
										<span className="text-xs font-medium text-settings-label">{t("dispatch.gateRecorded")}</span>
										<div className="flex flex-wrap items-center gap-1.5">
											{overrides.map((name) => (
												<button
													key={name}
													type="button"
													className="settings-chip-button"
													aria-label={t("dispatch.gateWithdraw", { name })}
													onClick={() => setOverrides((current) => current.filter((entry) => entry !== name))}
												>
													{name}
													<X className="size-icon-sm" aria-hidden="true" />
												</button>
											))}
										</div>
										<p className="text-xs text-settings-muted">{t("dispatch.gateRecordedHint")}</p>
									</div>
								) : null}
							</div>
						) : null}
						<div className="flex flex-col gap-1.5">
							<label className="text-xs font-medium text-settings-label" htmlFor="dispatchBranch">
								{t("dispatch.branch")}
							</label>
							<input
								id="dispatchBranch"
								className="settings-inline-input settings-field"
								value={branch}
								onChange={(e) => setBranch(e.target.value)}
							/>
						</div>
						<div className="flex flex-col gap-1.5">
							<label className="text-xs font-medium text-settings-label" htmlFor="dispatchPrompt">
								{t("dispatch.prompt")}
							</label>
							<textarea
								id="dispatchPrompt"
								className="settings-inline-input settings-field min-h-28 resize-y"
								value={prompt}
								onChange={(e) => setPrompt(e.target.value)}
							/>
						</div>
						{dispatchMutation.isError ? (
							<p className="text-xs text-error" role="alert">
								{dispatchMutation.error instanceof Error
									? dispatchMutation.error.message
									: t("dispatch.dispatchFailed")}
							</p>
						) : null}
						{blockedReason ? <p className="text-xs text-settings-muted">{blockedReason}</p> : null}
						{/* Cancel + primary, the footer every other dialog in the app
						    uses (New task, Create work item, the confirm dialogs). This
						    one offered the primary alone, leaving the header's × as the
						    only way out of a half-filled dispatch. */}
						<div className={settingsDialogFooterClass}>
							<button
								type="button"
								className="settings-footer-button"
								disabled={busy}
								onClick={() => onOpenChange(false)}
							>
								{t("dispatch.cancel")}
							</button>
							<button
								type="submit"
								className="settings-footer-button settings-footer-button-primary"
								disabled={!canSubmit}
							>
								{busy ? t("dispatch.dispatching") : t("dispatch.submit")}
							</button>
						</div>
					</form>
				)}
			</DialogContent>
		</Dialog>
	);
}
