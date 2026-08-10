import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { ArrowLeft, Monitor, RefreshCw, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import type { components } from "../../api/schema";
import { executionHostsQueryOptions } from "../hooks/useExecutionHostsQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import {
	parseExecutionPreferences,
	serializeExecutionPreferences,
	type ParsedExecutionPreferences,
} from "../lib/execution-preferences";
import { formatTimeCompact } from "../lib/format-time";
import { transportLabel, trustZoneLabel } from "./settings/ComputersSection";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { CenterPanelShell } from "./CenterPanelShell";
import { ConfirmDialog } from "./ConfirmDialog";
import { TopbarButton, topbarProjectLabelClass } from "./TopbarButton";
import { Badge } from "./ui/badge";

type Inventory = components["schemas"]["ControllersExecutionHostInventoryResponse"];
type Providers = components["schemas"]["ControllersListExecutionProvidersResponse"];
type Instructions = components["schemas"]["ControllersExecutionInstructionsEnvelope"];
type Schedules = components["schemas"]["ControllersListExecutionSchedulesResponse"];

const TABS = ["overview", "skills", "preferences", "instructions", "schedules"] as const;
type Tab = (typeof TABS)[number];

// The five prefs roles every Paseo skill reads (mirrors the file's contract).
const PREF_ROLES = ["impl", "ui", "research", "planning", "audit"] as const;

function tabLabel(tab: Tab, t: TFunction): string {
	switch (tab) {
		case "overview":
			return t("hostDetail.tabOverview");
		case "skills":
			return t("hostDetail.tabSkills");
		case "preferences":
			return t("hostDetail.tabPreferences");
		case "instructions":
			return t("hostDetail.tabInstructions");
		case "schedules":
			return t("hostDetail.tabSchedules");
	}
}

export function inventoryQueryKey(hostId: string) {
	return ["execution-inventory", hostId] as const;
}

// F7: everything AO knows about one computer, on the U5/U9/U9a/U10 read
// surfaces. Every save path goes through the channel's write → confirm-read
// cycle and renders its typed refusals verbatim; nothing here bypasses the
// worker's own drift preconditions.
export function HostDetailView({ hostId }: { hostId: string }) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const [tab, setTab] = useState<Tab>("overview");
	const hostsQuery = useQuery(executionHostsQueryOptions);
	const host = (hostsQuery.data ?? []).find((candidate) => candidate.id === hostId);

	return (
		<CenterPanelShell>
			<div className="center-panel-titlebar flex h-toolbar shrink-0 items-center gap-2 border-b border-border-strong pr-4">
				<TopbarButton aria-label={t("hostDetail.back")} onClick={() => void navigate({ to: "/settings" })}>
					<ArrowLeft className="size-icon-md" aria-hidden="true" />
				</TopbarButton>
				<Monitor className="size-icon-md text-settings-muted" aria-hidden="true" />
				<span className={topbarProjectLabelClass}>{host?.name ?? hostId}</span>
				<div className="min-w-0 flex-1" />
				<nav className="flex items-center gap-1" aria-label={t("hostDetail.tabs")}>
					{TABS.map((candidate) => (
						<button
							key={candidate}
							type="button"
							className={
								candidate === tab ? "settings-option-trigger bg-settings-menu-selected" : "settings-option-trigger"
							}
							aria-current={candidate === tab ? "page" : undefined}
							onClick={() => setTab(candidate)}
						>
							{tabLabel(candidate, t)}
						</button>
					))}
				</nav>
			</div>
			<div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
				<div className="mx-auto flex w-full max-w-3xl flex-col gap-3">
					{tab === "overview" ? <OverviewTab hostId={hostId} /> : null}
					{tab === "skills" ? <SkillsTab hostId={hostId} /> : null}
					{tab === "preferences" ? <PreferencesTab hostId={hostId} /> : null}
					{tab === "instructions" ? <InstructionsTab hostId={hostId} /> : null}
					{tab === "schedules" ? <SchedulesTab hostId={hostId} /> : null}
				</div>
			</div>
		</CenterPanelShell>
	);
}

const rowClass =
	"rounded-(--radius-settings-row) border border-(--color-border-settings-input) bg-(--color-bg-settings-row) px-3.5 py-3";

function OverviewTab({ hostId }: { hostId: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const hostsQuery = useQuery(executionHostsQueryOptions);
	const host = (hostsQuery.data ?? []).find((candidate) => candidate.id === hostId);
	const probeMutation = useMutation({
		mutationFn: async () => {
			const { data, error } = await apiClient.POST("/api/v1/execution/hosts/{hostId}/probe", {
				params: { path: { hostId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.host;
		},
		onSettled: () =>
			void queryClient.invalidateQueries({
				queryKey: executionHostsQueryOptions.queryKey,
			}),
	});
	if (!host) return <p className="text-sm text-settings-muted">{t("hostDetail.loading")}</p>;
	const rows: Array<[string, string]> = [
		[t("hostDetail.endpoint"), host.endpoint],
		[t("hostDetail.transport"), transportLabel(host.transport, t)],
		[t("hostDetail.trustZone"), trustZoneLabel(host.trustZone, t)],
		[
			t("hostDetail.status"),
			host.reachable ? t("settings.computers.statusOnline") : t("settings.computers.statusOffline"),
		],
		[t("hostDetail.version"), host.paseoVersion || "—"],
		[t("hostDetail.serverId"), host.serverId || "—"],
		[t("hostDetail.sessions"), `${host.activeSessions}/${host.maxConcurrentSessions}`],
		[t("hostDetail.capabilities"), host.capabilities.join(", ") || "—"],
	];
	return (
		<>
			{rows.map(([label, value]) => (
				<div key={label} className={`${rowClass} flex items-baseline justify-between gap-4`}>
					<span className="text-xs text-settings-muted">{label}</span>
					<span className="truncate text-sm text-settings-label">{value}</span>
				</div>
			))}
			<div className="flex items-center gap-2">
				<button
					type="button"
					className="settings-option-trigger"
					disabled={probeMutation.isPending}
					onClick={() => probeMutation.mutate()}
				>
					{probeMutation.isPending ? t("settings.computers.testing") : t("settings.computers.testConnection")}
				</button>
				{probeMutation.isError ? (
					<span className="text-xs text-error">
						{probeMutation.error instanceof Error ? probeMutation.error.message : t("settings.computers.probeFailed")}
					</span>
				) : null}
			</div>
			{host.lastProbeError ? <p className="text-xs text-error">{host.lastProbeError}</p> : null}
		</>
	);
}

export function SkillsTab({ hostId }: { hostId: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [refreshing, setRefreshing] = useState(false);
	const [syncSource, setSyncSource] = useState("local");
	const [syncName, setSyncName] = useState("");
	const [syncError, setSyncError] = useState<string | null>(null);

	const inventoryQuery = useQuery({
		queryKey: inventoryQueryKey(hostId),
		queryFn: async (): Promise<Inventory> => {
			const { data, error } = await apiClient.GET("/api/v1/execution/hosts/{hostId}/inventory", {
				params: { path: { hostId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		retry: 1,
	});
	const hostsQuery = useQuery(executionHostsQueryOptions);
	const otherHosts = (hostsQuery.data ?? []).filter((candidate) => candidate.id !== hostId && candidate.enabled);
	// Cross-host comparison reads the OTHER hosts' cached inventories only —
	// comparison must not cost a live channel run per host per render.
	const otherInventories = useQuery({
		queryKey: ["execution-inventories", otherHosts.map((candidate) => candidate.id).join(",")],
		queryFn: async () => {
			const entries = await Promise.all(
				otherHosts.map(async (candidate) => {
					const { data } = await apiClient.GET("/api/v1/execution/hosts/{hostId}/inventory", {
						params: { path: { hostId: candidate.id } },
					});
					return [candidate.id, data?.skills ?? []] as const;
				}),
			);
			return new Map(entries);
		},
		enabled: otherHosts.length > 0,
		retry: 0,
	});

	const refreshMutation = useMutation({
		mutationFn: async () => {
			setRefreshing(true);
			const { data, error } = await apiClient.GET("/api/v1/execution/hosts/{hostId}/inventory", {
				params: { path: { hostId }, query: { refresh: true } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSettled: () => setRefreshing(false),
		onSuccess: (data) => queryClient.setQueryData(inventoryQueryKey(hostId), data),
	});
	const syncMutation = useMutation({
		mutationFn: async ({ source, name }: { source: string; name: string }) => {
			const { data, error } = await apiClient.POST("/api/v1/execution/hosts/{hostId}/skills/{name}/sync", {
				params: { path: { hostId, name } },
				body: { source },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: (data) => {
			setSyncError(null);
			setSyncName("");
			queryClient.setQueryData(inventoryQueryKey(hostId), (current: Inventory | undefined) =>
				current ? { ...current, skills: data.skills, skillsAsOf: data.skillsAsOf } : data,
			);
		},
		onError: (error: unknown) => setSyncError(error instanceof Error ? error.message : t("hostDetail.syncFailed")),
	});

	const inventory = inventoryQuery.data;
	const skills = inventory?.skills ?? [];
	const names = new Set(skills.map((skill) => skill.name));
	const missingElsewhere = useMemo(() => {
		const rows: Array<{ name: string; source: string }> = [];
		for (const [otherId, otherSkills] of otherInventories.data ?? new Map()) {
			for (const skill of otherSkills) {
				if (!names.has(skill.name) && !rows.some((row) => row.name === skill.name)) {
					rows.push({ name: skill.name, source: otherId });
				}
			}
		}
		return rows;
	}, [otherInventories.data, names]);

	return (
		<>
			<div className="flex items-center justify-between gap-2">
				<span className="text-xs text-settings-muted">
					{inventory?.skillsAsOf
						? t("hostDetail.asOf", {
								time: formatTimeCompact(inventory.skillsAsOf),
							})
						: t("hostDetail.neverInventoried")}
				</span>
				<button
					type="button"
					className="settings-option-trigger inline-flex items-center gap-1.5"
					disabled={refreshing}
					onClick={() => refreshMutation.mutate()}
				>
					<RefreshCw className={refreshing ? "size-icon-base animate-spin" : "size-icon-base"} aria-hidden="true" />
					{refreshing ? t("hostDetail.refreshing") : t("hostDetail.refresh")}
				</button>
			</div>
			{refreshMutation.isError ? (
				<p className="text-xs text-error" role="alert">
					{refreshMutation.error instanceof Error ? refreshMutation.error.message : t("hostDetail.refreshFailed")}
				</p>
			) : null}
			{inventoryQuery.isLoading ? (
				<p className="text-sm text-settings-muted">{t("hostDetail.loading")}</p>
			) : inventoryQuery.isError ? (
				<p className="text-sm text-error" role="alert">
					{inventoryQuery.error instanceof Error ? inventoryQuery.error.message : t("hostDetail.loadFailed")}
				</p>
			) : skills.length === 0 ? (
				<p className="text-sm text-settings-muted">{t("hostDetail.noSkills")}</p>
			) : (
				skills.map((skill) => (
					<div key={skill.name} className={rowClass}>
						<div className="flex items-center gap-2">
							<span className="text-sm font-medium text-settings-label">{skill.name}</span>
							{skill.policyGated ? (
								<Badge variant="warning" className="text-caption" title={t("hostDetail.gatedTooltip")}>
									{t("hostDetail.gatedBadge")}
								</Badge>
							) : null}
						</div>
						{skill.description ? <p className="mt-0.5 text-xs text-settings-muted">{skill.description}</p> : null}
					</div>
				))
			)}
			{missingElsewhere.map((row) => (
				<div key={row.name} className={`${rowClass} flex items-center justify-between gap-3`}>
					<span className="min-w-0 truncate text-xs text-settings-muted">
						{t("hostDetail.missingSkill", {
							name: row.name,
							source: row.source,
						})}
					</span>
					<button
						type="button"
						className="settings-option-trigger shrink-0"
						disabled={syncMutation.isPending}
						onClick={() => {
							setSyncSource(row.source);
							setSyncName(row.name);
							syncMutation.mutate({ source: row.source, name: row.name });
						}}
					>
						{t("hostDetail.syncHere")}
					</button>
				</div>
			))}
			<div className={`${rowClass} flex flex-col gap-2`}>
				<span className="text-xs font-medium text-settings-label">{t("hostDetail.syncTitle")}</span>
				<div className="flex flex-wrap items-center gap-2">
					<SettingsOptionMenu
						value={syncSource}
						options={[
							{ value: "local", label: t("hostDetail.sourceLocal") },
							...otherHosts.map((candidate) => ({
								value: candidate.id,
								label: candidate.name,
							})),
						]}
						onChange={setSyncSource}
						aria-label={t("hostDetail.syncSource")}
					/>
					<input
						className="settings-inline-input min-w-0 flex-1"
						value={syncName}
						onChange={(event) => setSyncName(event.target.value)}
						placeholder={t("hostDetail.syncNamePlaceholder")}
					/>
					<button
						type="button"
						className="settings-option-trigger"
						disabled={syncMutation.isPending || syncName.trim() === ""}
						onClick={() => syncMutation.mutate({ source: syncSource, name: syncName.trim() })}
					>
						{syncMutation.isPending ? t("hostDetail.syncing") : t("hostDetail.syncHere")}
					</button>
				</div>
				{syncError ? (
					<p className="text-xs text-error" role="alert">
						{syncError}
					</p>
				) : null}
			</div>
		</>
	);
}

function PreferencesTab({ hostId }: { hostId: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const inventoryQuery = useQuery({
		queryKey: inventoryQueryKey(hostId),
		queryFn: async (): Promise<Inventory> => {
			const { data, error } = await apiClient.GET("/api/v1/execution/hosts/{hostId}/inventory", {
				params: { path: { hostId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		retry: 1,
	});
	const providersQuery = useQuery({
		queryKey: ["execution-providers", hostId],
		queryFn: async (): Promise<Providers["providers"]> => {
			const { data, error } = await apiClient.GET("/api/v1/execution/hosts/{hostId}/providers", {
				params: { path: { hostId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.providers;
		},
		retry: 1,
	});
	// The provider strings a preferences file may name, from the live catalog:
	// bare providers and provider/model pairs. A string outside this list is
	// unselectable, so a stale or misspelled provider cannot be saved.
	const catalog = useMemo(() => {
		const options: string[] = [];
		for (const provider of providersQuery.data ?? []) {
			if (provider.status !== "available") continue;
			options.push(provider.provider);
			for (const model of provider.models) {
				options.push(`${provider.provider}/${model.id}`);
			}
		}
		return options;
	}, [providersQuery.data]);

	const prefs = inventoryQuery.data?.prefs;
	const [roles, setRoles] = useState<Record<string, string>>({});
	const [freeform, setFreeform] = useState("");
	const [loadedHash, setLoadedHash] = useState<string | null>(null);
	const [parsedPrefs, setParsedPrefs] = useState<ParsedExecutionPreferences | null>(null);
	const [parseError, setParseError] = useState<string | null>(null);
	const [saveError, setSaveError] = useState<string | null>(null);
	const [savedAt, setSavedAt] = useState<string | null>(null);

	useEffect(() => {
		if (!prefs || prefs.sha256 === loadedHash) return;
		try {
			const parsed = parseExecutionPreferences(prefs.content);
			setParsedPrefs(parsed);
			setRoles(parsed.providers);
			setFreeform(parsed.preferences.join("\n"));
			setParseError(null);
		} catch (error) {
			setParsedPrefs(null);
			setRoles({});
			setFreeform("");
			setParseError(error instanceof Error ? error.message : t("hostDetail.prefsInvalid"));
		}
		setLoadedHash(prefs.sha256);
	}, [prefs, loadedHash, t]);

	const saveMutation = useMutation({
		mutationFn: async () => {
			if (!prefs) throw new Error(t("hostDetail.prefsNotRead"));
			if (!parsedPrefs) throw new Error(t("hostDetail.prefsInvalid"));
			const preferences = freeform
				.split("\n")
				.map((line) => line.trim())
				.filter(Boolean);
			const content = serializeExecutionPreferences(parsedPrefs, PREF_ROLES, roles, preferences);
			const { data, error } = await apiClient.PUT("/api/v1/execution/hosts/{hostId}/preferences", {
				params: { path: { hostId } },
				body: { content, baseSha256: prefs.sha256 },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.prefs;
		},
		onSuccess: (confirmed) => {
			setSaveError(null);
			setSavedAt(confirmed.confirmedAt);
			queryClient.setQueryData(inventoryQueryKey(hostId), (current: Inventory | undefined) =>
				current ? { ...current, prefs: confirmed } : current,
			);
		},
		onError: (error: unknown) => {
			setSavedAt(null);
			setSaveError(error instanceof Error ? error.message : t("hostDetail.saveFailed"));
		},
	});

	if (inventoryQuery.isLoading) return <p className="text-sm text-settings-muted">{t("hostDetail.loading")}</p>;
	if (inventoryQuery.isError) {
		return (
			<p className="text-sm text-error" role="alert">
				{inventoryQuery.error instanceof Error ? inventoryQuery.error.message : t("hostDetail.loadFailed")}
			</p>
		);
	}
	if (!prefs) return <p className="text-sm text-settings-muted">{t("hostDetail.prefsNotRead")}</p>;

	return (
		<>
			<p className="text-xs text-settings-muted">
				{t("hostDetail.prefsConfirmed", {
					time: formatTimeCompact(prefs.confirmedAt),
				})}
			</p>
			{parseError ? (
				<p className="text-xs text-error" role="alert">
					{t("hostDetail.prefsInvalidDetail", { error: parseError })}
				</p>
			) : null}
			{PREF_ROLES.map((role) => {
				const current = roles[role] ?? "";
				// The file's current value stays visible even when the live
				// catalog no longer contains it — replacing it is how the drift
				// gets fixed, and only catalog values are offered as replacements.
				const options = catalog.includes(current) || current === "" ? catalog : [current, ...catalog];
				return (
					<div key={role} className={`${rowClass} flex items-center justify-between gap-4`}>
						<span className="font-mono text-xs text-settings-label">{role}</span>
						<SettingsOptionMenu
							value={current}
							options={options.map((entry) => ({
								value: entry,
								label: catalog.includes(entry) ? entry : t("hostDetail.roleStale", { value: entry }),
							}))}
							onChange={(value) => setRoles((existing) => ({ ...existing, [role]: value }))}
							placeholder={providersQuery.isFetching ? t("dispatch.discovering") : t("hostDetail.roleUnset")}
							disabled={catalog.length === 0}
							aria-label={role}
						/>
					</div>
				);
			})}
			<div className={`${rowClass} flex flex-col gap-1.5`}>
				<label className="text-xs font-medium text-settings-label" htmlFor="prefsFreeform">
					{t("hostDetail.freeform")}
				</label>
				<textarea
					id="prefsFreeform"
					className="settings-inline-input min-h-24 w-full resize-y font-mono text-xs"
					value={freeform}
					onChange={(event) => setFreeform(event.target.value)}
					placeholder={t("hostDetail.freeformPlaceholder")}
				/>
			</div>
			<div className="flex items-center gap-2">
				<button
					type="button"
					className="settings-footer-button settings-footer-button-primary"
					disabled={saveMutation.isPending || parsedPrefs === null}
					onClick={() => saveMutation.mutate()}
				>
					{saveMutation.isPending ? t("hostDetail.saving") : t("hostDetail.save")}
				</button>
				{savedAt ? <span className="text-xs text-(--color-success)">{t("hostDetail.savedConfirmed")}</span> : null}
			</div>
			{saveError ? (
				<p className="text-xs text-error" role="alert">
					{saveError}
				</p>
			) : null}
		</>
	);
}

function InstructionsTab({ hostId }: { hostId: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const queryKey = ["execution-instructions", hostId] as const;
	const instructionsQuery = useQuery({
		queryKey,
		queryFn: async (): Promise<Instructions> => {
			const { data, error } = await apiClient.GET("/api/v1/execution/hosts/{hostId}/instructions", {
				params: { path: { hostId }, query: { refresh: true } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		retry: 1,
	});
	const stored = instructionsQuery.data?.instructions;
	const [content, setContent] = useState("");
	const [loadedHash, setLoadedHash] = useState<string | null>(null);
	const [saveError, setSaveError] = useState<string | null>(null);
	const [savedAt, setSavedAt] = useState<string | null>(null);

	useEffect(() => {
		if (!stored || stored.sha256 === loadedHash) return;
		setContent(stored.content);
		setLoadedHash(stored.sha256);
	}, [stored, loadedHash]);

	const saveMutation = useMutation({
		mutationFn: async () => {
			if (!stored) throw new Error(t("hostDetail.instructionsNotRead"));
			const { data, error } = await apiClient.PUT("/api/v1/execution/hosts/{hostId}/instructions", {
				params: { path: { hostId } },
				body: { content, baseSha256: stored.sha256 },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: (data) => {
			setSaveError(null);
			setSavedAt(data.instructions?.confirmedAt ?? null);
			queryClient.setQueryData(queryKey, data);
		},
		onError: (error: unknown) => {
			setSavedAt(null);
			setSaveError(error instanceof Error ? error.message : t("hostDetail.saveFailed"));
		},
	});

	if (instructionsQuery.isLoading) {
		return <p className="text-sm text-settings-muted">{t("hostDetail.readingInstructions")}</p>;
	}
	if (instructionsQuery.isError) {
		return (
			<p className="text-sm text-error">
				{instructionsQuery.error instanceof Error ? instructionsQuery.error.message : t("hostDetail.loadFailed")}
			</p>
		);
	}
	return (
		<>
			<p className="text-xs text-settings-muted">
				{stored?.exists ? t("hostDetail.machineClaude") : t("hostDetail.machineClaudeMissing")}
			</p>
			<textarea
				className="settings-inline-input min-h-64 w-full resize-y font-mono text-xs"
				value={content}
				onChange={(event) => setContent(event.target.value)}
				aria-label={t("hostDetail.tabInstructions")}
			/>
			<div className="flex items-center gap-2">
				<button
					type="button"
					className="settings-footer-button settings-footer-button-primary"
					disabled={saveMutation.isPending || content.trim() === ""}
					onClick={() => saveMutation.mutate()}
				>
					{saveMutation.isPending ? t("hostDetail.saving") : t("hostDetail.save")}
				</button>
				{savedAt ? <span className="text-xs text-(--color-success)">{t("hostDetail.savedConfirmed")}</span> : null}
			</div>
			{saveError ? (
				<p className="text-xs text-error" role="alert">
					{saveError}
				</p>
			) : null}
		</>
	);
}

export function SchedulesTab({ hostId }: { hostId: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [pendingDelete, setPendingDelete] = useState<Schedules["schedules"][number] | null>(null);
	const queryKey = ["execution-schedules", hostId] as const;
	const schedulesQuery = useQuery({
		queryKey,
		queryFn: async (): Promise<Schedules["schedules"]> => {
			const { data, error } = await apiClient.GET("/api/v1/execution/hosts/{hostId}/schedules", {
				params: { path: { hostId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.schedules;
		},
		retry: 1,
	});
	const deleteMutation = useMutation({
		mutationFn: async (scheduleId: string) => {
			const { error } = await apiClient.DELETE("/api/v1/execution/hosts/{hostId}/schedules/{scheduleId}", {
				params: { path: { hostId, scheduleId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: () => {
			setPendingDelete(null);
			void queryClient.invalidateQueries({ queryKey });
		},
	});
	const schedules = schedulesQuery.data ?? [];
	return (
		<>
			{/* The blind spot is structural: the pinned CLI has no heartbeat
			    listing, so an empty list proves nothing about heartbeats. */}
			<p className="text-xs text-settings-muted">{t("hostDetail.heartbeatBlindSpot")}</p>
			{schedulesQuery.isLoading ? (
				<p className="text-sm text-settings-muted">{t("hostDetail.loading")}</p>
			) : schedulesQuery.isError ? (
				<p className="text-sm text-error">
					{schedulesQuery.error instanceof Error ? schedulesQuery.error.message : t("hostDetail.loadFailed")}
				</p>
			) : schedules.length === 0 ? (
				<p className="text-sm text-settings-muted">{t("hostDetail.noSchedules")}</p>
			) : (
				schedules.map((schedule) => (
					<div key={schedule.id} className={`${rowClass} flex items-center justify-between gap-3`}>
						<div className="min-w-0">
							<div className="flex items-center gap-2">
								<span className="truncate text-sm font-medium text-settings-label">{schedule.name || schedule.id}</span>
								{schedule.policyViolation ? (
									<Badge variant="error" className="text-caption" title={t("hostDetail.violationTooltip")}>
										{t("hostDetail.violationBadge")}
									</Badge>
								) : null}
							</div>
							<p className="mt-0.5 truncate font-mono text-xs text-settings-muted">
								{schedule.cadence} · {schedule.target} · {schedule.status}
							</p>
						</div>
						<button
							type="button"
							className="settings-option-trigger inline-flex shrink-0 items-center gap-1.5"
							disabled={deleteMutation.isPending}
							onClick={() => setPendingDelete(schedule)}
						>
							<Trash2 className="size-icon-base" aria-hidden="true" />
							{t("hostDetail.deleteSchedule")}
						</button>
					</div>
				))
			)}
			<ConfirmDialog
				open={pendingDelete !== null}
				title={t("hostDetail.deleteScheduleTitle")}
				description={t("hostDetail.deleteScheduleDescription", {
					name: pendingDelete?.name || pendingDelete?.id || "",
				})}
				confirmLabel={t("hostDetail.deleteScheduleConfirm")}
				destructive
				busy={deleteMutation.isPending}
				error={
					deleteMutation.isError
						? deleteMutation.error instanceof Error
							? deleteMutation.error.message
							: t("hostDetail.deleteFailed")
						: null
				}
				onConfirm={() => {
					if (pendingDelete) deleteMutation.mutate(pendingDelete.id);
				}}
				onOpenChange={(next) => {
					if (!next && !deleteMutation.isPending) {
						deleteMutation.reset();
						setPendingDelete(null);
					}
				}}
			/>
		</>
	);
}
