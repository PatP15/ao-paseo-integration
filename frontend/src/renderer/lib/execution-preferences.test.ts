import { describe, expect, it } from "vitest";
import { parseExecutionPreferences, serializeExecutionPreferences } from "./execution-preferences";

describe("execution preferences editing", () => {
	it("preserves unknown top-level fields and provider roles", () => {
		const parsed = parseExecutionPreferences(
			JSON.stringify({
				providers: { impl: "claude/sonnet", custom: "codex/custom" },
				preferences: ["old"],
				loop: { worker: { thinking: "high" } },
			}),
		);

		const saved = JSON.parse(
			serializeExecutionPreferences(parsed, ["impl", "ui"], { impl: "codex/gpt-5.6", ui: "" }, ["new"]),
		);
		expect(saved).toEqual({
			providers: { impl: "codex/gpt-5.6", custom: "codex/custom" },
			preferences: ["new"],
			loop: { worker: { thinking: "high" } },
		});
	});

	it.each([
		["not json", "Unexpected token"],
		["[]", "root must be a JSON object"],
		['{"providers":[]}', "providers must be a JSON object"],
		['{"preferences":[1]}', "preferences must be an array of strings"],
	])("refuses malformed content instead of making it overwritable", (content, message) => {
		expect(() => parseExecutionPreferences(content)).toThrow(message);
	});
});
