import { beforeEach, describe, expect, it, vi } from "vitest";
import { fetchExecutionEvents } from "./RemoteSessionPane";

const getMock = vi.fn();
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: (...args: unknown[]) => getMock(...args) },
	apiErrorMessage: (error: unknown) => String(error),
}));

function event(id: string) {
	return {
		id,
		sessionId: "session-1",
		hostId: "host-1",
		kind: "checkpoint",
		transport: "terminal" as const,
		payloadJson: "{}",
		observedAt: "2026-08-09T00:00:00Z",
		ingestedAt: "2026-08-09T00:00:00Z",
		applied: true,
	};
}

describe("fetchExecutionEvents", () => {
	beforeEach(() => getMock.mockReset());

	it("continues from the rendered cursor and drains every returned page", async () => {
		getMock
			.mockResolvedValueOnce({
				data: { events: [event("event-501")], nextAfter: "event-501" },
			})
			.mockResolvedValueOnce({ data: { events: [event("event-502")] } });

		await expect(fetchExecutionEvents("session-1", [event("event-500")])).resolves.toEqual([
			event("event-500"),
			event("event-501"),
			event("event-502"),
		]);
		expect(getMock).toHaveBeenNthCalledWith(1, "/api/v1/sessions/{sessionId}/execution-events", {
			params: {
				path: { sessionId: "session-1" },
				query: { limit: 500, after: "event-500" },
			},
		});
		expect(getMock).toHaveBeenNthCalledWith(2, "/api/v1/sessions/{sessionId}/execution-events", {
			params: {
				path: { sessionId: "session-1" },
				query: { limit: 500, after: "event-501" },
			},
		});
	});

	it("surfaces a later-page failure instead of freezing the timeline", async () => {
		getMock
			.mockResolvedValueOnce({
				data: { events: [event("event-1")], nextAfter: "event-1" },
			})
			.mockResolvedValueOnce({ error: { message: "host unavailable" } });
		await expect(fetchExecutionEvents("session-1")).rejects.toThrow("[object Object]");
	});
});
