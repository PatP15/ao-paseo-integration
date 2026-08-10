import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PencilLine, TimerReset } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import { Switch } from "../ui/switch";
import { SettingsDetailRow, SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

type AutoResumeSettings = components["schemas"]["AutoResumeSettingsResponse"];

export const autoResumeSettingsQueryKey = ["auto-resume-settings"] as const;

/**
 * U12: app-wide policy for restarting a session whose agent died on a provider
 * usage limit. The daemon owns both the default prompt and the per-session cap
 * and returns them with every read, so nothing here keeps a second copy that
 * could drift from the backend's.
 */
export function AutoResumeSection() {
	const { t } = useTranslation();
	const queryClient = useQueryClient();

	const settingsQuery = useQuery({
		queryKey: autoResumeSettingsQueryKey,
		queryFn: async (): Promise<AutoResumeSettings> => {
			const { data, error } = await apiClient.GET("/api/v1/settings/auto-resume");
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		retry: 1,
	});

	const settings = settingsQuery.data;
	// The prompt is edited, not toggled, so it needs a draft the query cannot
	// stomp mid-keystroke. The toggle writes through immediately instead.
	const [draft, setDraft] = useState("");
	useEffect(() => {
		if (settings) setDraft(settings.resumePrompt);
	}, [settings]);

	const save = useMutation({
		mutationFn: async (next: { enabled: boolean; resumePrompt: string }): Promise<AutoResumeSettings> => {
			const { data, error } = await apiClient.PUT("/api/v1/settings/auto-resume", { body: next });
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: (next) => {
			queryClient.setQueryData(autoResumeSettingsQueryKey, next);
			setDraft(next.resumePrompt);
		},
		onError: () => {
			// The refused write never landed; show the stored policy again.
			if (settings) setDraft(settings.resumePrompt);
		},
	});

	if (settingsQuery.isLoading) {
		return (
			<SettingsSection title={t("settings.autoResume")} sectionId="auto-resume">
				<p className="px-1 text-xs text-settings-muted">{t("settings.autoResume.loading")}</p>
			</SettingsSection>
		);
	}

	if (!settings) {
		return (
			<SettingsSection title={t("settings.autoResume")} sectionId="auto-resume">
				<p className="px-1 text-xs text-error">{t("settings.autoResume.loadFailed")}</p>
			</SettingsSection>
		);
	}

	const dirty = draft !== settings.resumePrompt;

	return (
		<SettingsSection title={t("settings.autoResume")} sectionId="auto-resume">
			<SettingsRow icon={TimerReset} label={t("settings.autoResume.enabled")}>
				<Switch
					aria-label={t("settings.autoResume.enabled")}
					checked={settings.enabled}
					disabled={save.isPending}
					onCheckedChange={(enabled) => save.mutate({ enabled, resumePrompt: settings.resumePrompt })}
				/>
			</SettingsRow>
			{settings.enabled ? (
				<SettingsDetailRow
					icon={PencilLine}
					title={
						<label htmlFor="auto-resume-prompt" className="settings-field-label">
							{t("settings.autoResume.prompt")}
						</label>
					}
					meta={
						<form
							className="mt-1.5 flex flex-col gap-1.5"
							onSubmit={(event) => {
								event.preventDefault();
								if (dirty) save.mutate({ enabled: settings.enabled, resumePrompt: draft });
							}}
						>
							{/* Single line on purpose: the daemon refuses a multi-line
							    resume prompt because a line break submits at the agent's
							    own prompt before the sentence is finished. */}
							<input
								id="auto-resume-prompt"
								type="text"
								className="settings-inline-input settings-field"
								value={draft}
								placeholder={settings.defaultResumePrompt}
								disabled={save.isPending}
								onChange={(event) => setDraft(event.target.value)}
							/>
							<div className="flex items-center justify-between gap-3">
								<span>{t("settings.autoResume.promptHint")}</span>
								{dirty ? (
									<button type="submit" className="settings-option-trigger" disabled={save.isPending}>
										{save.isPending ? t("settings.autoResume.saving") : t("settings.autoResume.save")}
									</button>
								) : null}
							</div>
						</form>
					}
				/>
			) : null}
			<p className="px-1 text-xs text-settings-muted">
				{t("settings.autoResume.hint", { limit: settings.maxResumesPerSession })}
			</p>
			{save.isError ? (
				<p role="alert" className="px-1 text-xs text-error">
					{save.error instanceof Error ? save.error.message : t("settings.autoResume.saveFailed")}
				</p>
			) : null}
		</SettingsSection>
	);
}
