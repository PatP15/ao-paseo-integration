import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AutoResumeSection } from "./AutoResumeSection";

const getMock = vi.fn();
const putMock = vi.fn();
vi.mock("../../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		PUT: (...args: unknown[]) => putMock(...args),
	},
	apiErrorMessage: (error: unknown) => String(error),
}));

const DEFAULT_PROMPT = "You were interrupted by a provider usage limit.";

function settings(overrides: { enabled?: boolean; resumePrompt?: string } = {}) {
	return {
		enabled: overrides.enabled ?? false,
		resumePrompt: overrides.resumePrompt ?? "",
		defaultResumePrompt: DEFAULT_PROMPT,
		maxResumesPerSession: 5,
	};
}

function renderSection() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<AutoResumeSection />
		</QueryClientProvider>,
	);
}

describe("AutoResumeSection", () => {
	beforeEach(() => {
		getMock.mockReset();
		putMock.mockReset();
		getMock.mockResolvedValue({ data: settings(), error: undefined });
	});

	it("hides the prompt field until the toggle is on", async () => {
		renderSection();
		expect(await screen.findByLabelText("Resume after a usage limit")).not.toBeChecked();
		expect(screen.queryByLabelText("Resume prompt")).not.toBeInTheDocument();
	});

	it("writes the toggle through and reveals the prompt field", async () => {
		putMock.mockResolvedValue({ data: settings({ enabled: true }), error: undefined });
		renderSection();

		await userEvent.click(await screen.findByLabelText("Resume after a usage limit"));

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		expect(putMock).toHaveBeenCalledWith("/api/v1/settings/auto-resume", {
			body: { enabled: true, resumePrompt: "" },
		});
		// The stored prompt is empty, so the field advertises the daemon's default
		// rather than a second copy of that sentence living in the renderer.
		const field = await screen.findByLabelText("Resume prompt");
		expect(field).toHaveValue("");
		expect(field).toHaveAttribute("placeholder", DEFAULT_PROMPT);
	});

	it("saves an edited prompt only once it differs from what is stored", async () => {
		getMock.mockResolvedValue({ data: settings({ enabled: true }), error: undefined });
		putMock.mockResolvedValue({
			data: settings({ enabled: true, resumePrompt: "Pick the checklist back up." }),
			error: undefined,
		});
		renderSection();

		const field = await screen.findByLabelText("Resume prompt");
		expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();

		await userEvent.type(field, "Pick the checklist back up.");
		await userEvent.click(screen.getByRole("button", { name: "Save" }));

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		expect(putMock).toHaveBeenCalledWith("/api/v1/settings/auto-resume", {
			body: { enabled: true, resumePrompt: "Pick the checklist back up." },
		});
		// Save button retires once the draft matches the stored policy again.
		await waitFor(() => expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument());
	});

	it("restores the stored prompt when the daemon refuses the write", async () => {
		getMock.mockResolvedValue({ data: settings({ enabled: true, resumePrompt: "stored" }), error: undefined });
		putMock.mockResolvedValue({ data: undefined, error: { code: "RESUME_PROMPT_TOO_LONG" } });
		renderSection();

		const field = await screen.findByLabelText("Resume prompt");
		await userEvent.type(field, " and more");
		await userEvent.click(screen.getByRole("button", { name: "Save" }));

		expect(await screen.findByRole("alert")).toBeInTheDocument();
		await waitFor(() => expect(field).toHaveValue("stored"));
	});

	it("reports a failed read instead of rendering an empty policy", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { code: "BOOM" } });
		renderSection();

		// The read retries once before it gives up, so allow for that backoff.
		expect(
			await screen.findByText("Could not load the auto-resume policy.", undefined, { timeout: 5000 }),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Resume after a usage limit")).not.toBeInTheDocument();
	});
});
