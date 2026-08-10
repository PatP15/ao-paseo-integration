import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, X } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

type ExecutionQuestion = components["schemas"]["ExecutionQuestionResponse"];

export const executionQuestionsQueryKey = ["execution-questions"] as const;

// Inline answer/decide for an execution question, rendered inside its
// notification row (F5). The two shapes are deliberately distinct — an agent
// question takes text, a host permission request takes an explicit allow/deny
// — mirroring the API's own separation; the daemon records the OS user as the
// identity since none is named here.
export function ExecutionQuestionActions({ questionId }: { questionId: string }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [answer, setAnswer] = useState("");
	const [actionError, setActionError] = useState<string | null>(null);

	const questionsQuery = useQuery({
		queryKey: executionQuestionsQueryKey,
		queryFn: async (): Promise<ExecutionQuestion[]> => {
			const { data, error } = await apiClient.GET("/api/v1/execution/questions");
			if (error) throw new Error(apiErrorMessage(error));
			return data.questions;
		},
		staleTime: 5000,
		retry: 1,
	});
	const question = (questionsQuery.data ?? []).find((entry) => entry.id === questionId);

	const settle = () => {
		setActionError(null);
		void queryClient.invalidateQueries({
			queryKey: executionQuestionsQueryKey,
		});
		// Covers both history variants (["notifications","history",…]).
		void queryClient.invalidateQueries({
			queryKey: ["notifications", "history"],
		});
	};
	const answerMutation = useMutation({
		mutationFn: async (text: string) => {
			const { error } = await apiClient.POST("/api/v1/execution/questions/{questionId}/answer", {
				params: { path: { questionId } },
				body: { answer: text },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: settle,
		onError: (error: unknown) => setActionError(error instanceof Error ? error.message : t("inbox.actionFailed")),
	});
	const decideMutation = useMutation({
		mutationFn: async (decision: "allow" | "deny") => {
			const { error } = await apiClient.POST("/api/v1/execution/permissions/{questionId}/decision", {
				params: { path: { questionId } },
				body: { decision },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: settle,
		onError: (error: unknown) => setActionError(error instanceof Error ? error.message : t("inbox.actionFailed")),
	});

	if (questionsQuery.isLoading) {
		return <p className="mt-1.5 text-caption text-passive">{t("inbox.loading")}</p>;
	}
	if (questionsQuery.isError) {
		return (
			<p className="mt-1.5 text-caption text-error" role="alert">
				{questionsQuery.error instanceof Error ? questionsQuery.error.message : t("inbox.actionFailed")}
			</p>
		);
	}
	if (!question) {
		// Gone from the open list: someone already answered or decided it.
		return <p className="mt-1.5 text-caption text-passive">{t("inbox.resolved")}</p>;
	}
	const busy = answerMutation.isPending || decideMutation.isPending;

	return (
		// The row above navigates on click; the action block must not.
		// eslint-disable-next-line jsx-a11y/no-static-element-interactions
		<div className="mt-2 flex flex-col gap-1.5" onClick={(event) => event.stopPropagation()}>
			{question.source === "paseo_permission" ? (
				<div className="flex items-center gap-2">
					<button
						type="button"
						className="settings-option-trigger inline-flex items-center gap-1.5"
						disabled={busy}
						onClick={() => decideMutation.mutate("allow")}
					>
						<Check className="size-icon-base" aria-hidden="true" />
						{t("inbox.allow")}
					</button>
					<button
						type="button"
						className="settings-option-trigger inline-flex items-center gap-1.5"
						disabled={busy}
						onClick={() => decideMutation.mutate("deny")}
					>
						<X className="size-icon-base" aria-hidden="true" />
						{t("inbox.deny")}
					</button>
				</div>
			) : (
				<>
					{question.options.length > 0 ? (
						<div className="flex flex-wrap items-center gap-1.5">
							{question.options.map((option) => (
								<button
									key={option}
									type="button"
									className="settings-option-trigger"
									disabled={busy}
									onClick={() => answerMutation.mutate(option)}
								>
									{option}
									{question.recommendation === option ? ` ${t("inbox.recommended")}` : ""}
								</button>
							))}
						</div>
					) : null}
					<form
						className="flex items-center gap-1.5"
						onSubmit={(event) => {
							event.preventDefault();
							if (answer.trim() !== "" && !busy) answerMutation.mutate(answer.trim());
						}}
					>
						<input
							className="settings-inline-input settings-field min-w-0 flex-1"
							value={answer}
							onChange={(event) => setAnswer(event.target.value)}
							placeholder={t("inbox.answerPlaceholder")}
							disabled={busy}
						/>
						<button type="submit" className="settings-option-trigger shrink-0" disabled={busy || answer.trim() === ""}>
							{busy ? t("inbox.sending") : t("inbox.send")}
						</button>
					</form>
				</>
			)}
			{actionError ? (
				<p className="text-caption text-error" role="alert">
					{actionError}
				</p>
			) : null}
		</div>
	);
}
