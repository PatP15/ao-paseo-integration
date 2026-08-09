import { describe, expect, it } from "vitest";
import { harnessForProvider } from "./execution-harness";

describe("harnessForProvider", () => {
	it("renames the one provider whose AO harness has a different name", () => {
		expect(harnessForProvider("claude")).toBe("claude-code");
	});

	it("maps a provider AO knows under the same name to itself", () => {
		expect(harnessForProvider("codex")).toBe("codex");
		expect(harnessForProvider("cursor")).toBe("cursor");
		expect(harnessForProvider("copilot")).toBe("copilot");
	});

	it("refuses a provider AO has no harness for instead of guessing one", () => {
		// The old fallback recorded these as claude-code, which made every later
		// read of the session's harness wrong.
		expect(harnessForProvider("gemini")).toBeNull();
		expect(harnessForProvider("some-future-provider")).toBeNull();
		expect(harnessForProvider("")).toBeNull();
		expect(harnessForProvider("   ")).toBeNull();
	});

	it("normalizes case and surrounding whitespace", () => {
		expect(harnessForProvider(" Claude ")).toBe("claude-code");
		expect(harnessForProvider("CODEX")).toBe("codex");
	});
});
