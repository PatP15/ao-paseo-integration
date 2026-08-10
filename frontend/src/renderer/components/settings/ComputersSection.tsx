import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { Monitor, Plus } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import {
	type ExecutionHost,
	executionHostsQueryKey,
	executionHostsQueryOptions,
} from "../../hooks/useExecutionHostsQuery";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import { Switch } from "../ui/switch";
import { SettingsDetailRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

type RegisterBody = components["schemas"]["RegisterExecutionHostRequest"];

export function transportLabel(transport: ExecutionHost["transport"], t: TFunction): string {
	switch (transport) {
		case "lan":
			return t("settings.computers.transportLan");
		case "tailscale":
			return t("settings.computers.transportTailscale");
		case "local":
			return t("settings.computers.transportLocal");
		case "paseo_relay":
			return t("settings.computers.transportPaseoRelay");
	}
}

export function trustZoneLabel(zone: ExecutionHost["trustZone"], t: TFunction): string {
	switch (zone) {
		case "hobby":
			return t("settings.computers.trustZoneHobby");
		case "work":
			return t("settings.computers.trustZoneWork");
		case "mixed":
			return t("settings.computers.trustZoneMixed");
	}
}

function registerBodyFromHost(host: ExecutionHost): RegisterBody {
	return {
		name: host.name,
		transport: host.transport,
		endpoint: host.endpoint,
		endpointSecretRef: host.endpointSecretRef || undefined,
		trustZone: host.trustZone,
		enabled: host.enabled,
		maxConcurrentSessions: host.maxConcurrentSessions,
		requiresNoMcp: host.requiresNoMcp,
		requiresNoRelay: host.requiresNoRelay || undefined,
		capabilities: host.capabilities.length ? host.capabilities : undefined,
	};
}

function HostStatusDot({ host }: { host: ExecutionHost }) {
	const { t } = useTranslation();
	const probed = Boolean(host.lastSuccessfulProbeAt || host.lastFailedProbeAt);
	const tone = !probed ? "bg-(--color-text-passive)" : host.reachable ? "bg-(--color-success)" : "bg-(--color-error)";
	const label = !probed
		? t("settings.computers.statusUnprobed")
		: host.reachable
			? t("settings.computers.statusOnline")
			: t("settings.computers.statusOffline");
	return (
		<span className="inline-flex items-center gap-1.5 text-xs text-settings-muted">
			<span className={`size-1.5 rounded-full ${tone}`} aria-hidden="true" />
			{label}
		</span>
	);
}

export function ComputersSection({ onAdd, onEdit }: { onAdd: () => void; onEdit: (host: ExecutionHost) => void }) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const hostsQuery = useQuery(executionHostsQueryOptions);
	const [rowError, setRowError] = useState<string | null>(null);
	const [probing, setProbing] = useState<string | null>(null);

	const toggleMutation = useMutation({
		mutationFn: async (host: ExecutionHost) => {
			const { error } = await apiClient.PUT("/api/v1/execution/hosts/{hostId}", {
				params: { path: { hostId: host.id } },
				body: { ...registerBodyFromHost(host), enabled: !host.enabled },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: () => {
			setRowError(null);
			void queryClient.invalidateQueries({ queryKey: executionHostsQueryKey });
		},
		onError: (error: unknown) =>
			setRowError(error instanceof Error ? error.message : t("settings.computers.updateFailed")),
	});

	const probeMutation = useMutation({
		mutationFn: async (host: ExecutionHost) => {
			setProbing(host.id);
			const { data, error } = await apiClient.POST("/api/v1/execution/hosts/{hostId}/probe", {
				params: { path: { hostId: host.id } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data.host;
		},
		onSettled: () => setProbing(null),
		onSuccess: () => {
			setRowError(null);
			void queryClient.invalidateQueries({ queryKey: executionHostsQueryKey });
		},
		onError: (error: unknown) => {
			void queryClient.invalidateQueries({ queryKey: executionHostsQueryKey });
			setRowError(error instanceof Error ? error.message : t("settings.computers.probeFailed"));
		},
	});

	const hosts = hostsQuery.data ?? [];
	const busy = toggleMutation.isPending || probeMutation.isPending;

	return (
		<SettingsSection title={t("settings.computers.title")} sectionId="computers">
			{hostsQuery.isLoading ? (
				<p className="px-1 text-xs text-settings-muted">{t("settings.computers.loading")}</p>
			) : hostsQuery.isError ? (
				<p className="px-1 text-xs text-error">
					{hostsQuery.error instanceof Error ? hostsQuery.error.message : t("settings.computers.loadFailed")}
				</p>
			) : hosts.length === 0 ? (
				<p className="px-1 text-xs text-settings-muted">{t("settings.computers.empty")}</p>
			) : (
				<div className="flex flex-col gap-1.5">
					{hosts.map((host) => {
						const blocked = host.activeSessions > 0;
						return (
							<SettingsDetailRow
								key={host.id}
								icon={Monitor}
								title={
									<>
										<button
											type="button"
											className="truncate underline-offset-2 hover:underline"
											onClick={() => void navigate({ to: "/computers/$hostId", params: { hostId: host.id } })}
										>
											{host.name}
										</button>
										<HostStatusDot host={host} />
									</>
								}
								meta={
									<>
										<p className="truncate">
											{host.endpoint} · {transportLabel(host.transport, t)} · {trustZoneLabel(host.trustZone, t)} ·{" "}
											{t("settings.computers.sessions", {
												active: host.activeSessions,
												max: host.maxConcurrentSessions,
											})}
										</p>
										{host.lastProbeError ? (
											<p className="mt-0.5 text-error" role="alert">
												{host.lastProbeError}
											</p>
										) : null}
									</>
								}
								actions={
									<>
										<button
											type="button"
											className="settings-option-trigger"
											onClick={() => probeMutation.mutate(host)}
											disabled={busy}
										>
											{probing === host.id ? t("settings.computers.testing") : t("settings.computers.testConnection")}
										</button>
										<button
											type="button"
											className="settings-option-trigger"
											onClick={() => onEdit(host)}
											disabled={busy}
										>
											{t("settings.computers.edit")}
										</button>
										<span title={blocked && host.enabled ? t("settings.computers.disableBlockedTooltip") : undefined}>
											<Switch
												checked={host.enabled}
												onCheckedChange={() => toggleMutation.mutate(host)}
												disabled={busy || (blocked && host.enabled)}
												aria-label={t("settings.computers.enabledSwitch", { name: host.name })}
											/>
										</span>
									</>
								}
							/>
						);
					})}
				</div>
			)}
			{rowError ? (
				<p className="px-1 text-xs text-error" role="alert">
					{rowError}
				</p>
			) : null}
			<button
				type="button"
				className="settings-row-bar w-full justify-start text-left text-sm leading-5 text-settings-label transition-colors hover:bg-settings-menu-selected"
				onClick={onAdd}
			>
				<Plus className="size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" />
				{t("settings.computers.add")}
			</button>
		</SettingsSection>
	);
}
