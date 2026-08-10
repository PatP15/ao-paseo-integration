import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { ArrowLeft, Monitor, RefreshCw, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import type { components } from "../../api/schema";
import { executionHostsQueryOptions, useExecutionHostName } from "../hooks/useExecutionHostsQuery";
import { apiClient, apiErrorCode, apiErrorMessage, apiErrorWithoutCode } from "../lib/api-client";
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
import { PendingLine } from "./PendingLine";
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
type PrefRole = (typeof PREF_ROLES)[number];

// A role is a file key, and the tab showed nothing else: five mono words —
// impl, ui, research, planning, audit — against five model pickers, with
// nothing saying what kind of work each one covers or who reads the answer.
// The key stays visible beside the name, because it is what the file contains
// and what a human editing that file by hand will look for.
function roleName(role: PrefRole, t: TFunction): string {
	switch (role) {
		case "impl":
			return t("hostDetail.roleImpl");
		case "ui":
			return t("hostDetail.roleUi");
		case "research":
			return t("hostDetail.roleResearch");
		case "planning":
			return t("hostDetail.rolePlanning");
		case "audit":
			return t("hostDetail.roleAudit");
	}
}

function roleHint(role: PrefRole, t: TFunction): string {
	switch (role) {
		case "impl":
			return t("hostDetail.roleImplHint");
		case "ui":
			return t("hostDetail.roleUiHint");
		case "research":
			return t("hostDetail.roleResearchHint");
		case "planning":
			return t("hostDetail.rolePlanningHint");
		case "audit":
			return t("hostDetail.roleAuditHint");
	}
}

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

// Host detail is a center-panel surface, so its rows wear the same card chrome
// as the board's session cards — rounded-lg, --border, --color-bg-surface —
// rather than the settings tokens, whose 16px radius, transparent fill and 3%
// hairline make an otherwise identical card read as a different app.
const rowClass = "rounded-lg border border-border bg-surface px-3.5 py-3";

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
	if (!host) return <PendingLine>{t("hostDetail.loading")}</PendingLine>;
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
		// source is the registry id the sync route wants; sourceName is what the
		// row shows, because every other surface names a computer by its display
		// name and this row was the last one printing a raw id.
		const rows: Array<{ name: string; source: string; sourceName: string }> = [];
		for (const [otherId, otherSkills] of otherInventories.data ?? new Map()) {
			const sourceName = otherHosts.find((candidate) => candidate.id === otherId)?.name ?? otherId;
			for (const skill of otherSkills) {
				if (!names.has(skill.name) && !rows.some((row) => row.name === skill.name)) {
					rows.push({ name: skill.name, source: otherId, sourceName });
				}
			}
		}
		return rows;
	}, [otherInventories.data, otherHosts, names]);

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
				<PendingLine slowHint={t("hostDetail.slowRead")}>{t("hostDetail.loading")}</PendingLine>
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
								<Badge variant="warning" className="text-caption">
									{t("hostDetail.gatedBadge")}
								</Badge>
							) : null}
						</div>
						{skill.description ? <p className="mt-0.5 text-xs text-settings-muted">{skill.description}</p> : null}
					</div>
				))
			)}
			{/* A badge that flags a refusal has to say so on screen. This lived in a
			    `title` tooltip, which no touch or keyboard user ever reads — and it
			    was the only explanation of the warning. */}
			{skills.some((skill) => skill.policyGated) ? (
				<p className="text-xs text-settings-muted">{t("hostDetail.gatedExplanation")}</p>
			) : null}
			{missingElsewhere.map((row) => (
				<div key={row.name} className={`${rowClass} flex items-center justify-between gap-3`}>
					<span className="min-w-0 truncate text-xs text-settings-muted">
						{t("hostDetail.missingSkill", {
							name: row.name,
							source: row.sourceName,
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
						className="settings-inline-input settings-field min-w-0 flex-1"
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

export function PreferencesTab({ hostId }: { hostId: string }) {
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
	// Same typed shape as the Instructions tab: this write carries a base digest
	// too, so it meets the same drift refusal.
	const [saveError, setSaveError] = useState<{ code?: string; detail: string } | null>(null);
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
			if (error) {
				const failure = new Error(apiErrorMessage(error)) as Error & { code?: string };
				failure.code = apiErrorCode(error);
				throw failure;
			}
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
			setSaveError({
				code: (error as { code?: string })?.code,
				detail: error instanceof Error ? error.message : t("hostDetail.saveFailed"),
			});
		},
	});

	if (inventoryQuery.isLoading) return <PendingLine slowHint={t("hostDetail.slowRead")}>{t("hostDetail.loading")}</PendingLine>;
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
			{/* What the file is for, before five model pickers with no context. */}
			<p className="text-xs text-settings-muted">{t("hostDetail.prefsExplanation")}</p>
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
					<div key={role} className={`${rowClass} flex items-start justify-between gap-4`}>
						<div className="min-w-0">
							<p className="flex items-baseline gap-1.5 text-xs font-medium text-settings-label">
								{roleName(role, t)}
								<span className="font-mono text-caption text-settings-muted">{role}</span>
							</p>
							<p className="mt-0.5 text-caption text-settings-muted">{roleHint(role, t)}</p>
						</div>
						<SettingsOptionMenu
							value={current}
							options={options.map((entry) => ({
								value: entry,
								label: catalog.includes(entry) ? entry : t("hostDetail.roleStale", { value: entry }),
							}))}
							onChange={(value) => setRoles((existing) => ({ ...existing, [role]: value }))}
							placeholder={providersQuery.isFetching ? t("dispatch.discovering") : t("hostDetail.roleUnset")}
							disabled={catalog.length === 0}
							aria-label={roleName(role, t)}
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
					className="settings-inline-input settings-field min-h-24 resize-y font-mono text-xs"
					value={freeform}
					onChange={(event) => setFreeform(event.target.value)}
					placeholder={t("hostDetail.freeformPlaceholder")}
				/>
				<p className="text-caption text-settings-muted">{t("hostDetail.freeformHint")}</p>
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
				<HostWriteFailure
					hostId={hostId}
					failure={saveError}
					rereading={inventoryQuery.isFetching}
					onReread={() => {
						setSaveError(null);
						// Same reset as the Instructions tab: the file's values are what the
						// re-read is for, so let them land even on a digest seen before.
						setLoadedHash(null);
						void inventoryQuery.refetch();
					}}
				/>
			) : null}
		</>
	);
}

/**
 * A refused write to a file on another computer, with the re-read it demands.
 *
 * Both write tabs send the digest they read (`baseSha256`), so both can be
 * refused because someone edited that file on the computer in between — which is
 * the guard working. What reached the user was the channel's own line: `drift:
 * the file on disk hashes to <64 hex>, not the expected <64 hex>; re-read before
 * writing (MAINTENANCE_REFUSED)`. Two digests nobody can act on, and an
 * instruction to do something the tab offered no way to do. The sentence names
 * the computer and what AO declined to do, the button performs the re-read, and
 * the digests stay as the transcript underneath.
 */
function HostWriteFailure({
	hostId,
	failure,
	rereading,
	onReread,
}: {
	hostId: string;
	failure: { code?: string; detail: string };
	rereading: boolean;
	onReread: () => void;
}) {
	const { t } = useTranslation();
	const hostName = useExecutionHostName(hostId);
	if (failure.code !== "MAINTENANCE_REFUSED") {
		// Every other refusal already arrives as an operator-facing sentence.
		return (
			<p className="break-words text-xs text-error" role="alert">
				{failure.detail}
			</p>
		);
	}
	return (
		<div className="flex flex-col items-start gap-1.5 text-xs" role="alert">
			<p className="break-words text-error">{t("hostDetail.saveRefusedDrift", { host: hostName })}</p>
			<p className="text-caption text-settings-muted">{t("hostDetail.rereadWarning")}</p>
			<button type="button" className="settings-option-trigger" disabled={rereading} onClick={onReread}>
				<RefreshCw className={rereading ? "size-icon-base animate-spin" : "size-icon-base"} aria-hidden="true" />
				{rereading ? t("hostDetail.rereading") : t("hostDetail.reread")}
			</button>
			<p className="whitespace-pre-wrap break-words font-mono text-settings-muted">
				{apiErrorWithoutCode(failure.detail, failure.code)}
			</p>
		</div>
	);
}

export function InstructionsTab({ hostId }: { hostId: string }) {
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
	// The refusal code is kept, not flattened into the message: a save refused
	// because the file moved under AO needs a different sentence — and a control —
	// from a save the channel could not deliver at all.
	const [saveError, setSaveError] = useState<{ code?: string; detail: string } | null>(null);
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
			if (error) {
				const failure = new Error(apiErrorMessage(error)) as Error & { code?: string };
				failure.code = apiErrorCode(error);
				throw failure;
			}
			return data;
		},
		onSuccess: (data) => {
			setSaveError(null);
			setSavedAt(data.instructions?.confirmedAt ?? null);
			queryClient.setQueryData(queryKey, data);
		},
		onError: (error: unknown) => {
			setSavedAt(null);
			setSaveError({
				code: (error as { code?: string })?.code,
				detail: error instanceof Error ? error.message : t("hostDetail.saveFailed"),
			});
		},
	});

	if (instructionsQuery.isLoading) {
		return <PendingLine slowHint={t("hostDetail.slowRead")}>{t("hostDetail.readingInstructions")}</PendingLine>;
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
				className="settings-inline-input settings-field min-h-64 resize-y font-mono text-xs"
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
			{/* The primary disables itself on an empty file, so it says why rather
			    than going quiet: the write would be refused, and removing the file
			    is not something AO does from here. */}
			{content.trim() === "" ? (
				<p className="text-caption text-settings-muted">{t("hostDetail.saveBlockedEmpty")}</p>
			) : null}
			{saveError ? (
				<HostWriteFailure
					hostId={hostId}
					failure={saveError}
					rereading={instructionsQuery.isFetching}
					onReread={() => {
						setSaveError(null);
						// Clearing the loaded digest lets the read that comes back replace
						// the editor even if this tab has shown that digest before.
						setLoadedHash(null);
						void instructionsQuery.refetch();
					}}
				/>
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
			{/* What this tab is, before a list of red badges: the schedules belong
			    to the computer, not to AO. */}
			<p className="text-xs text-settings-muted">{t("hostDetail.schedulesExplanation")}</p>
			{/* The blind spot is structural: the pinned CLI has no heartbeat
			    listing, so an empty list proves nothing about heartbeats. */}
			<p className="text-xs text-settings-muted">{t("hostDetail.heartbeatBlindSpot")}</p>
			{schedulesQuery.isLoading ? (
				<PendingLine slowHint={t("hostDetail.slowRead")}>{t("hostDetail.loading")}</PendingLine>
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
									<Badge variant="error" className="text-caption">
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
			{/* Same rule as the Skills tab's gate badge: the reason is on screen,
			    not in a tooltip only a mouse can find. */}
			{schedules.some((schedule) => schedule.policyViolation) ? (
				<p className="text-xs text-settings-muted">{t("hostDetail.violationExplanation")}</p>
			) : null}
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
