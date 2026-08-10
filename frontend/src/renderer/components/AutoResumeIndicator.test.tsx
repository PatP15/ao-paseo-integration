import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AutoResumeIndicator } from "./AutoResumeIndicator";

const getMock = vi.fn();
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: (...args: unknown[]) => getMock(...args) },
	apiErrorMessage: (error: unknown) => String(error),
}));

const RESUME_AT = "2026-08-10T22:48:00Z";
// The badge prints the machine's own wall clock, so the expectation is derived
// the same way rather than hardcoded to whatever zone this suite runs in.
const RESUME_CLOCK = new Date(RESUME_AT).toLocaleTimeString("en", { hour: "numeric", minute: "2-digit" });

function pendingRow(overrides: Partial<Record<string, unknown>> = {}) {
	return { sessionId: "session-1", resumeAt: RESUME_AT, attempt: 2, exactReset: true, ...overrides };
}

function renderIndicator(sessionId = "session-1") {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<AutoResumeIndicator sessionId={sessionId} />
		</QueryClientProvider>,
	);
}

describe("AutoResumeIndicator", () => {
	beforeEach(() => {
		getMock.mockReset();
		getMock.mockResolvedValue({ data: { pending: [pendingRow()], maxResumesPerSession: 5 }, error: undefined });
	});

	it("names the reset time the provider published", async () => {
		renderIndicator();
		expect(await screen.findByText(`Resumes ${RESUME_CLOCK}`)).toBeInTheDocument();
	});

	it("marks a guessed reset as approximate so an odd firing time is explainable", async () => {
		getMock.mockResolvedValue({
			data: { pending: [pendingRow({ exactReset: false })], maxResumesPerSession: 5 },
			error: undefined,
		});
		renderIndicator();
		expect(await screen.findByText(`Retries ~${RESUME_CLOCK}`)).toBeInTheDocument();
	});

	it("renders nothing for a session with no scheduled resume", async () => {
		const { container } = renderIndicator("session-2");
		await waitFor(() => expect(getMock).toHaveBeenCalled());
		expect(container).toBeEmptyDOMElement();
	});

	// A daemon too old to know the route, or one that cannot read the schedule,
	// must leave the card alone rather than claim every session is waiting.
	it("stays silent when the read fails", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { code: "NOT_IMPLEMENTED" } });
		const { container } = renderIndicator();
		await waitFor(() => expect(getMock).toHaveBeenCalled());
		expect(container).toBeEmptyDOMElement();
	});
});
