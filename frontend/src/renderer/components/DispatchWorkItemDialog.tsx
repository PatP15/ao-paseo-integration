import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import type { components } from "../../api/schema";
import { executionHostsQueryOptions } from "../hooks/useExecutionHostsQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
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

// AO harness recorded on the session for each remote provider. The store
// constrains the column, so only values AO already supports may be sent.
const HARNESS_BY_PROVIDER: Record<string, string> = {
	claude: "claude-code",
	codex: "codex",
};

const NONE = "__none__";

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
	const [hostId, setHostId] = useState("");
	const [provider, setProvider] = useState("");
	const [model, setModel] = useState(NONE);
	const [mode, setMode] = useState(NONE);
	const [thinking, setThinking] = useState(NONE);
	const [branch, setBranch] = useState("");
	const [prompt, setPrompt] = useState("");
	const [progress, setProgress] = useState<DispatchResponse | null>(null);
	const [commandState, setCommandState] = useState<string | null>(null);

	useEffect(() => {
		if (open && workItem) {
			setBranch(`ao/${workItem.id.slice(0, 20)}`);
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
			const { data, error } = await apiClient.POST("/api/v1/execution/dispatch", {
				body: {
					workItemId: workItem.id,
					projectId,
					trustZone: host.trustZone,
					harness: HARNESS_BY_PROVIDER[provider] ?? "claude-code",
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
				if (!error) setCommandState(data.commandState);
			})();
		}, 2000);
		return () => clearInterval(interval);
	}, [progress, commandState]);

	const busy = dispatchMutation.isPending;
	const canSubmit =
		!busy && !progress && workItem !== null && host !== undefined && provider !== "" && branch.trim() !== "" && prompt.trim() !== "";

	const optionMenus = [
		{
			key: "provider",
			label: t("dispatch.provider"),
			value: provider,
			options: providers.map((entry) => ({ value: entry.provider, label: entry.label || entry.provider })),
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
				{ value: NONE, label: t("dispatch.providerDefault") },
				...(providerInfo?.models ?? []).map((entry) => ({ value: entry.id, label: entry.label || entry.id })),
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
				{ value: NONE, label: t("dispatch.providerDefault") },
				...(providerInfo?.modes.length
					? providerInfo.modes.map((entry) => ({
							value: entry.id,
							label: entry.label ? `${entry.label} (${entry.id})` : entry.id,
						}))
					: providerInfo?.defaultMode
						? [{ value: providerInfo.defaultMode, label: providerInfo.defaultMode }]
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
				{ value: NONE, label: t("dispatch.providerDefault") },
				...(modelInfo?.thinkingOptionIds ?? []).map((id) => ({ value: id, label: id })),
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
									: t("dispatch.progress", { state: commandState ?? "pending" })}
						</p>
						<p className="text-xs text-settings-muted">
							{t("dispatch.sessionLine", { sessionId: progress.sessionId, hostId: progress.hostId })}
						</p>
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
								options={boundHosts.map((entry) => ({ value: entry.id, label: entry.name }))}
								onChange={(value) => {
									setHostId(value);
									setProvider("");
									setModel(NONE);
									setMode(NONE);
									setThinking(NONE);
								}}
								placeholder={
									boundHosts.length === 0 ? t("dispatch.noBoundComputers") : t("dispatch.selectComputer")
								}
								disabled={boundHosts.length === 0}
								aria-label={t("dispatch.computer")}
							/>
							{boundHosts.length === 0 && !hostsQuery.isFetching && !bindingsQuery.isFetching ? (
								<p className="text-xs text-warning">{t("dispatch.bindHint")}</p>
							) : null}
							{host ? (
								<p className="text-xs text-settings-muted">
									{t("dispatch.trustZoneLine", { zone: host.trustZone })}
								</p>
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
						{skills.length > 0 ? (
							<div className="flex flex-col gap-1.5">
								<span className="text-xs font-medium text-settings-label">{t("dispatch.skills")}</span>
								<div className="flex flex-wrap items-center gap-1.5">
									{skills.map((skill) => (
										<button
											key={skill.name}
											type="button"
											className="settings-option-trigger"
											title={skill.description || skill.name}
											onClick={() => insertSkill(skill)}
										>
											{skill.name}
											{skill.policyGated ? ` ${t("dispatch.gatedMark")}` : ""}
										</button>
									))}
								</div>
								<p className="text-xs text-settings-muted">{t("dispatch.skillsHint")}</p>
								{pendingGate ? (
									<div className="flex flex-col gap-1.5 rounded-md border border-(--color-border-settings-input) px-3 py-2">
										<p className="text-xs text-warning">
											{t("dispatch.gateExplanation", { name: pendingGate })}
										</p>
										<div className="flex items-center gap-2">
											<button
												type="button"
												className="settings-option-trigger"
												onClick={() => {
													setOverrides((current) => [...current, pendingGate]);
													const skill = skills.find((entry) => entry.name === pendingGate);
													if (skill) appendSkillSnippet(skill);
												}}
											>
												{t("dispatch.gateEnable")}
											</button>
											<button
												type="button"
												className="settings-option-trigger"
												onClick={() => setPendingGate(null)}
											>
												{t("dispatch.gateCancel")}
											</button>
										</div>
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
								className="settings-inline-input w-full"
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
								className="settings-inline-input min-h-28 w-full resize-y"
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
						<div className={settingsDialogFooterClass}>
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
