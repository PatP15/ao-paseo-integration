import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ExecutionQuestionActions } from "./ExecutionQuestionActions";

const getMock = vi.fn();
const postMock = vi.fn();
vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
	},
	apiErrorMessage: (error: unknown) => String(error),
}));

function renderActions(questionId: string) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<ExecutionQuestionActions questionId={questionId} />
		</QueryClientProvider>,
	);
}

describe("ExecutionQuestionActions", () => {
	beforeEach(() => {
		getMock.mockReset();
		postMock.mockReset();
		postMock.mockResolvedValue({ data: {}, error: undefined });
	});

	it("answers an agent question with a clicked option", async () => {
		getMock.mockResolvedValue({
			data: {
				questions: [
					{
						id: "q-1",
						sessionId: "sess-1",
						source: "agent_event",
						externalId: "evt-1",
						question: "Rebase or merge?",
						recommendation: "rebase",
						options: ["rebase", "merge"],
						createdAt: "2026-08-09T00:00:00Z",
					},
				],
			},
			error: undefined,
		});
		renderActions("q-1");
		await userEvent.click(await screen.findByRole("button", { name: /rebase/ }));
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		const [path, options] = postMock.mock.calls[0] as [string, { body: { answer: string } }];
		expect(path).toBe("/api/v1/execution/questions/{questionId}/answer");
		expect(options.body).toEqual({ answer: "rebase" });
	});

	it("decides a permission request with allow and deny", async () => {
		getMock.mockResolvedValue({
			data: {
				questions: [
					{
						id: "q-2",
						sessionId: "sess-1",
						source: "paseo_permission",
						externalId: "perm_full_id",
						question: "Allow Bash?",
						options: [],
						createdAt: "2026-08-09T00:00:00Z",
					},
				],
			},
			error: undefined,
		});
		renderActions("q-2");
		await userEvent.click(await screen.findByRole("button", { name: /Allow/ }));
		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		const [path, options] = postMock.mock.calls[0] as [string, { body: { decision: string } }];
		expect(path).toBe("/api/v1/execution/permissions/{questionId}/decision");
		expect(options.body).toEqual({ decision: "allow" });
	});

	it("shows already-answered for a question no longer open", async () => {
		getMock.mockResolvedValue({ data: { questions: [] }, error: undefined });
		renderActions("q-gone");
		expect(await screen.findByText("Already answered.")).toBeInTheDocument();
	});
});
