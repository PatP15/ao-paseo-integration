import { render, screen } from "@testing-library/react";
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PendingLine } from "./PendingLine";

describe("PendingLine", () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	// A spinner answers "is it working?" but not "should I still be waiting?".
	// The remote reads hold this line for a minute or more.
	it("withholds the slow hint until the wait is actually long", () => {
		render(<PendingLine slowHint="Still waiting on this computer.">Reading…</PendingLine>);

		expect(screen.getByText("Reading…")).toBeInTheDocument();
		expect(screen.queryByText("Still waiting on this computer.")).not.toBeInTheDocument();

		act(() => void vi.advanceTimersByTime(15_000));
		expect(screen.getByText("Still waiting on this computer.")).toBeInTheDocument();
	});

	it("stays a single line for reads that never leave this machine", () => {
		render(<PendingLine>Loading work items…</PendingLine>);
		act(() => void vi.advanceTimersByTime(60_000));
		expect(screen.getByRole("status")).toHaveTextContent("Loading work items…");
		expect(screen.getByRole("status").parentElement?.textContent).toBe("Loading work items…");
	});
});
