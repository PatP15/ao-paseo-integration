import type { components } from "../../api/schema";

/** The harness names AO records on a session. */
export type AoHarness = NonNullable<components["schemas"]["SpawnSessionRequest"]["harness"]>;

// Kept in the same order as the backend's AllHarnesses. The assertion below
// fails to compile if the generated schema gains a harness this list lacks.
const AO_HARNESSES = [
	"claude-code",
	"codex",
	"aider",
	"opencode",
	"grok",
	"droid",
	"amp",
	"agy",
	"crush",
	"cursor",
	"qwen",
	"copilot",
	"goose",
	"auggie",
	"continue",
	"devin",
	"cline",
	"kimi",
	"kiro",
	"kilocode",
	"vibe",
	"pi",
	"autohand",
] as const satisfies readonly AoHarness[];

type MissingHarness = Exclude<AoHarness, (typeof AO_HARNESSES)[number]>;
type AssertNever<T extends never> = T;
export type _EveryHarnessListed = AssertNever<MissingHarness>;

const HARNESS_SET: ReadonlySet<string> = new Set<string>(AO_HARNESSES);

// Paseo names its providers; AO names its harnesses. Every provider id AO also
// knows as a harness maps to itself, and `claude` is the one rename.
const HARNESS_BY_PROVIDER: Record<string, AoHarness> = {
	claude: "claude-code",
};

/**
 * The AO harness for a discovered Paseo provider, or null when AO has none.
 *
 * The harness is a fact about what actually runs the session: AO reads a
 * transcript, resolves a reviewer, and renders session chrome from it. A
 * provider AO cannot name is refused at the dispatch boundary rather than
 * recorded as some other harness, which would make every later read wrong.
 */
export function harnessForProvider(provider: string): AoHarness | null {
	const normalized = provider.trim().toLowerCase();
	if (normalized === "") return null;
	const aliased = HARNESS_BY_PROVIDER[normalized];
	if (aliased) return aliased;
	return HARNESS_SET.has(normalized) ? (normalized as AoHarness) : null;
}
