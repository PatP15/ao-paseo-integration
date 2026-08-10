import * as Dialog from "@radix-ui/react-dialog";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { TriangleAlert, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import {
	type ExecutionHost,
	executionHostsQueryKey,
	executionHostsQueryOptions,
} from "../../hooks/useExecutionHostsQuery";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import { transportLabel, trustZoneLabel } from "./ComputersSection";
import { SettingsOptionMenu } from "./SettingsOptionMenu";

type RegisterBody = components["schemas"]["RegisterExecutionHostRequest"];

const TRANSPORTS = ["lan", "tailscale", "local", "paseo_relay"] as const;
const TRUST_ZONES = ["hobby", "work", "mixed"] as const;

type Step = "connection" | "details" | "review";

interface FormState {
	id: string;
	idTouched: boolean;
	name: string;
	endpoint: string;
	password: string;
	transport: (typeof TRANSPORTS)[number];
	trustZone: (typeof TRUST_ZONES)[number];
	maxSessions: string;
	capabilities: string;
	requiresNoMcp: boolean;
	requiresNoRelay: boolean;
}

function emptyForm(): FormState {
	return {
		id: "",
		idTouched: false,
		name: "",
		endpoint: "",
		password: "",
		transport: "lan",
		trustZone: "hobby",
		maxSessions: "2",
		capabilities: "",
		// Checked from the start, like requiresNoRelay: both are preconditions AO
		// enforces, not choices. Unchecked-by-default opened step 2 with Next
		// already disabled, which read as a broken dialog rather than as a posture
		// the operator has to confirm. Unticking it still blocks the step — now
		// with the reason spelled out next to the button.
		requiresNoMcp: true,
		requiresNoRelay: true,
	};
}

function formFromHost(host: ExecutionHost): FormState {
	return {
		id: host.id,
		idTouched: true,
		name: host.name,
		endpoint: host.endpoint,
		password: "",
		transport: host.transport,
		trustZone: host.trustZone,
		maxSessions: String(host.maxConcurrentSessions),
		capabilities: host.capabilities.join(", "),
		requiresNoMcp: host.requiresNoMcp,
		requiresNoRelay: host.requiresNoRelay,
	};
}

function slugify(name: string): string {
	return name
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "")
		.slice(0, 40);
}

export function ComputerSheet({
	open,
	onOpenChange,
	host,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** Present when editing; absent when adding. */
	host: ExecutionHost | null;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const editing = host !== null;
	const [step, setStep] = useState<Step>("connection");
	const [form, setForm] = useState<FormState>(emptyForm);
	const [probeOutcome, setProbeOutcome] = useState<string | null>(null);
	const hostsQuery = useQuery({ ...executionHostsQueryOptions, enabled: open });

	useEffect(() => {
		if (open) {
			setForm(host ? formFromHost(host) : emptyForm());
			setStep("connection");
			setProbeOutcome(null);
		}
	}, [open, host]);

	// Registration is an upsert on the id, and the id is derived from the
	// endpoint's host part — so two daemons on one machine slug to the same id.
	// Adding under a taken id would replace that computer's whole registration,
	// rotate the credential every binding still uses, and silently repoint its
	// sessions at a different endpoint. There is no host delete to undo it.
	const idTaken = !editing && (hostsQuery.data ?? []).some((entry) => entry.id === form.id.trim());

	const saveMutation = useMutation({
		mutationFn: async () => {
			// Rechecked here too: the review step is reached before this list can
			// refresh, and the secret write below is not undoable.
			if (idTaken) throw new Error(t("settings.computers.sheet.idTaken", { id: form.id.trim() }));
			let secretRef = editing ? host.endpointSecretRef : "";
			if (form.password !== "") {
				const name = `host-${form.id}-password`;
				const { data, error } = await apiClient.POST("/api/v1/execution/secrets", {
					body: { name, value: form.password, replace: true },
				});
				if (error) throw new Error(apiErrorMessage(error));
				secretRef = data.ref;
			}
			const body: RegisterBody = {
				name: form.name.trim(),
				transport: form.transport,
				endpoint: form.endpoint.trim(),
				endpointSecretRef: secretRef || undefined,
				trustZone: form.trustZone,
				enabled: editing ? host.enabled : true,
				maxConcurrentSessions: Number.parseInt(form.maxSessions, 10),
				requiresNoMcp: form.requiresNoMcp,
				requiresNoRelay: form.requiresNoRelay || undefined,
				capabilities: form.capabilities
					.split(",")
					.map((value) => value.trim())
					.filter(Boolean),
			};
			const { error } = await apiClient.PUT("/api/v1/execution/hosts/{hostId}", {
				params: { path: { hostId: form.id } },
				body,
			});
			if (error) throw new Error(apiErrorMessage(error));

			const probe = await apiClient.POST("/api/v1/execution/hosts/{hostId}/probe", {
				params: { path: { hostId: form.id } },
			});
			if (probe.error) {
				// Registered, but the connection test failed: surface the reason
				// and keep the sheet open so the operator can correct and retry.
				throw new Error(apiErrorMessage(probe.error));
			}
			return probe.data.host;
		},
		onSuccess: (probed) => {
			void queryClient.invalidateQueries({ queryKey: executionHostsQueryKey });
			if (probed.reachable) {
				onOpenChange(false);
			} else {
				setProbeOutcome(probed.lastProbeError || t("settings.computers.sheet.unreachable"));
			}
		},
		onError: () => {
			void queryClient.invalidateQueries({ queryKey: executionHostsQueryKey });
		},
	});

	const isBusy = saveMutation.isPending;
	const connectionValid =
		form.id.trim() !== "" && !idTaken && form.endpoint.includes(":") && !/\s/.test(form.endpoint.trim());
	const maxSessionsValue = Number.parseInt(form.maxSessions, 10);
	const detailsValid =
		form.name.trim() !== "" &&
		form.requiresNoMcp &&
		Number.isInteger(maxSessionsValue) &&
		maxSessionsValue >= 1 &&
		maxSessionsValue <= 64;
	const error = saveMutation.isError
		? saveMutation.error instanceof Error
			? saveMutation.error.message
			: t("settings.computers.sheet.saveFailed")
		: probeOutcome;
	// Why Next is refusing, in the order the fields appear. The id-taken and
	// endpoint cases already carry their own inline message, so they are not
	// repeated here.
	const blockedReason = ((): string | null => {
		if (isBusy) return null;
		if (step === "connection") {
			if (idTaken) return null;
			if (form.endpoint.trim() === "") return t("settings.computers.sheet.blockedEndpoint");
			// The id is required and auto-derived, so clearing it is easy to do by
			// accident — and it used to be reported as a malformed endpoint.
			if (form.id.trim() === "") return t("settings.computers.sheet.blockedId");
			if (!connectionValid) return t("settings.computers.sheet.blockedEndpointPort");
			return null;
		}
		if (step === "details") {
			if (form.name.trim() === "") return t("settings.computers.sheet.blockedName");
			if (!form.requiresNoMcp) return t("settings.computers.sheet.blockedNoMcp");
			if (!Number.isInteger(maxSessionsValue) || maxSessionsValue < 1 || maxSessionsValue > 64) {
				return t("settings.computers.sheet.blockedMaxSessions");
			}
		}
		return null;
	})();

	const field = "flex flex-col gap-1.5";
	const labelClass = "text-xs font-medium text-settings-label";
	const inputClass = "settings-inline-input settings-field";

	return (
		<Dialog.Root open={open} onOpenChange={(next) => !isBusy && onOpenChange(next)}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-[min(480px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 rounded-agents-sheet border border-[var(--color-border-agents-sheet)] bg-[var(--color-bg-agents-sheet)] p-0 text-[var(--color-text-agents-sheet-title)] shadow-[var(--shadow-import-modal)] data-[state=open]:animate-modal-in">
					<div className="flex items-start justify-between gap-4 border-b border-[var(--color-border-agents-sheet)] px-6 py-5">
						<div className="min-w-0">
							<Dialog.Title className="text-subtitle font-semibold text-[var(--color-text-agents-sheet-title)]">
								{editing ? t("settings.computers.sheet.editTitle") : t("settings.computers.sheet.addTitle")}
							</Dialog.Title>
							<Dialog.Description className="mt-1 text-xs text-[var(--color-text-agents-sheet-description)]">
								{step === "connection"
									? t("settings.computers.sheet.stepConnection")
									: step === "details"
										? t("settings.computers.sheet.stepDetails")
										: t("settings.computers.sheet.stepReview")}
							</Dialog.Description>
						</div>
						<Dialog.Close asChild>
							<button
								type="button"
								className="grid size-7 shrink-0 place-items-center rounded-md text-[var(--color-text-agents-sheet-description)] transition hover:bg-interactive-hover hover:text-[var(--color-text-agents-sheet-title)] disabled:pointer-events-none disabled:opacity-50"
								aria-label={t("settings.computers.sheet.close")}
								disabled={isBusy}
							>
								<X className="size-icon-base" aria-hidden="true" />
							</button>
						</Dialog.Close>
					</div>
					<form
						className="space-y-4 px-6 py-5"
						onSubmit={(event) => {
							event.preventDefault();
							if (step === "connection" && connectionValid) setStep("details");
							else if (step === "details" && detailsValid) setStep("review");
							else if (step === "review" && !isBusy) saveMutation.mutate();
						}}
					>
						{step === "connection" ? (
							<>
								<div className={field}>
									<label className={labelClass} htmlFor="computerEndpoint">
										{t("settings.computers.sheet.endpoint")}
									</label>
									<input
										id="computerEndpoint"
										className={inputClass}
										value={form.endpoint}
										onChange={(e) => {
											const endpoint = e.target.value;
											setForm((f) => ({
												...f,
												endpoint,
												id: f.idTouched ? f.id : slugify(endpoint.split(":")[0] ?? ""),
											}));
										}}
										placeholder={t("settings.computers.sheet.endpointPlaceholder")}
										autoFocus
									/>
									<p className="text-xs text-settings-muted">{t("settings.computers.sheet.endpointHint")}</p>
								</div>
								<div className={field}>
									<label className={labelClass} htmlFor="computerId">
										{t("settings.computers.sheet.id")}
									</label>
									<input
										id="computerId"
										className={inputClass}
										value={form.id}
										onChange={(e) => setForm((f) => ({ ...f, id: slugify(e.target.value), idTouched: true }))}
										disabled={editing}
										aria-invalid={idTaken || undefined}
									/>
									{idTaken ? (
										<p className="text-xs text-error" role="alert">
											{t("settings.computers.sheet.idTaken", { id: form.id.trim() })}
										</p>
									) : null}
									{/* The only field on this step with no account of itself, and
									    the one nobody can guess: it is filled in from the endpoint,
									    it is required, it is the key registration upserts on — and
									    when editing it is disabled, which needs a reason on screen
									    rather than a dead control. */}
									<p className="text-xs text-settings-muted">
										{editing ? t("settings.computers.sheet.idHintEditing") : t("settings.computers.sheet.idHint")}
									</p>
								</div>
								<div className={field}>
									<label className={labelClass} htmlFor="computerPassword">
										{t("settings.computers.sheet.password")}
									</label>
									<input
										id="computerPassword"
										className={inputClass}
										type="password"
										value={form.password}
										onChange={(e) => setForm((f) => ({ ...f, password: e.target.value }))}
										placeholder={
											editing && host.endpointSecretRef
												? t("settings.computers.sheet.passwordKeep")
												: t("settings.computers.sheet.passwordPlaceholder")
										}
									/>
									<p className="text-xs text-settings-muted">{t("settings.computers.sheet.passwordHint")}</p>
								</div>
							</>
						) : step === "details" ? (
							<>
								<div className={field}>
									<label className={labelClass} htmlFor="computerName">
										{t("settings.computers.sheet.name")}
									</label>
									<input
										id="computerName"
										className={inputClass}
										value={form.name}
										onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
										autoFocus
									/>
								</div>
								<div className="grid gap-4 sm:grid-cols-2">
									<div className={field}>
										<span className={labelClass}>{t("settings.computers.sheet.transportLabel")}</span>
										<SettingsOptionMenu
											value={form.transport}
											options={TRANSPORTS.map((value) => ({
												value,
												label: transportLabel(value, t),
											}))}
											onChange={(value) => setForm((f) => ({ ...f, transport: value as FormState["transport"] }))}
											aria-label={t("settings.computers.sheet.transportLabel")}
										/>
									</div>
									<div className={field}>
										<span className={labelClass}>{t("settings.computers.sheet.trustZoneLabel")}</span>
										<SettingsOptionMenu
											value={form.trustZone}
											options={TRUST_ZONES.map((value) => ({
												value,
												label: trustZoneLabel(value, t),
											}))}
											onChange={(value) => setForm((f) => ({ ...f, trustZone: value as FormState["trustZone"] }))}
											aria-label={t("settings.computers.sheet.trustZoneLabel")}
										/>
										{/* A routing constraint, not a label: a dispatch can ask for a
										    zone, and only this zone or Mixed will take that work. */}
										<p className="text-xs text-settings-muted">{t("settings.computers.sheet.trustZoneHint")}</p>
									</div>
								</div>
								<div className="grid gap-4 sm:grid-cols-2">
									<div className={field}>
										<label className={labelClass} htmlFor="computerMaxSessions">
											{t("settings.computers.sheet.maxSessions")}
										</label>
										<input
											id="computerMaxSessions"
											className={inputClass}
											inputMode="numeric"
											value={form.maxSessions}
											onChange={(e) => setForm((f) => ({ ...f, maxSessions: e.target.value }))}
										/>
										<p className="text-xs text-settings-muted">{t("settings.computers.sheet.maxSessionsHint")}</p>
									</div>
									<div className={field}>
										<label className={labelClass} htmlFor="computerCapabilities">
											{t("settings.computers.sheet.capabilities")}
										</label>
										<input
											id="computerCapabilities"
											className={inputClass}
											value={form.capabilities}
											onChange={(e) => setForm((f) => ({ ...f, capabilities: e.target.value }))}
											placeholder={t("settings.computers.sheet.capabilitiesPlaceholder")}
										/>
									</div>
								</div>
								<label className="flex items-start gap-2 text-xs text-settings-label">
									<input
										type="checkbox"
										checked={form.requiresNoMcp}
										onChange={(e) => setForm((f) => ({ ...f, requiresNoMcp: e.target.checked }))}
										className="mt-0.5"
									/>
									<span>
										{t("settings.computers.sheet.noMcp")}
										<span className="block text-settings-muted">{t("settings.computers.sheet.noMcpHint")}</span>
									</span>
								</label>
							</>
						) : (
							<dl className="space-y-2 text-sm">
								{[
									[t("settings.computers.sheet.name"), form.name],
									[t("settings.computers.sheet.id"), form.id],
									[t("settings.computers.sheet.endpoint"), form.endpoint],
									[t("settings.computers.sheet.transportLabel"), transportLabel(form.transport, t)],
									[t("settings.computers.sheet.trustZoneLabel"), trustZoneLabel(form.trustZone, t)],
									[t("settings.computers.sheet.maxSessions"), form.maxSessions],
									// Capabilities are submitted, so a step that calls itself a
									// review has to show them: leaving the row out meant the last
									// screen before an unreversible registration silently dropped a
									// field the operator had typed.
									[
										t("settings.computers.sheet.capabilities"),
										form.capabilities.trim() === "" ? t("settings.computers.sheet.none") : form.capabilities.trim(),
									],
									[
										t("settings.computers.sheet.password"),
										form.password
											? t("settings.computers.sheet.passwordSet")
											: editing && host.endpointSecretRef
												? t("settings.computers.sheet.passwordKeep")
												: t("settings.computers.sheet.none"),
									],
								].map(([label, value]) => (
									<div key={label} className="flex items-baseline justify-between gap-4">
										<dt className="text-xs text-settings-muted">{label}</dt>
										<dd className="truncate text-settings-label">{value}</dd>
									</div>
								))}
							</dl>
						)}

						{error ? (
							<div
								role="alert"
								className="flex items-start gap-2 rounded-md border border-(--color-border-settings-input) px-3 py-2 text-xs text-error"
							>
								<TriangleAlert className="mt-0.5 size-icon-base shrink-0" aria-hidden="true" />
								<span>{error}</span>
							</div>
						) : null}

						{/* A disabled Next with no reason is the hardest control in the
						    dialog to recover from: nothing on screen says which field is
						    holding it. Name the blocker instead. */}
						{blockedReason ? <p className="text-xs text-settings-muted">{blockedReason}</p> : null}

						<div className="flex items-center justify-between pt-1">
							{step !== "connection" ? (
								<button
									type="button"
									className="settings-footer-button"
									onClick={() => setStep(step === "review" ? "details" : "connection")}
									disabled={isBusy}
								>
									{t("settings.computers.sheet.back")}
								</button>
							) : (
								<span />
							)}
							<button
								type="submit"
								className="settings-footer-button settings-footer-button-primary"
								disabled={
									isBusy || (step === "connection" ? !connectionValid : step === "details" ? !detailsValid : false)
								}
							>
								{step === "review"
									? isBusy
										? t("settings.computers.sheet.saving")
										: t("settings.computers.sheet.saveAndTest")
									: t("settings.computers.sheet.next")}
							</button>
						</div>
					</form>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}
