export type ExecutionPreferencesDocument = Record<string, unknown>;

export type ParsedExecutionPreferences = {
	document: ExecutionPreferencesDocument;
	providers: Record<string, string>;
	preferences: string[];
};

function isObject(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

// The settings UI edits the two fields defined by Paseo, but the preferences
// file is also used by local tooling and may carry extension keys. Keep the
// complete parsed document so saving known fields never deletes those keys.
export function parseExecutionPreferences(content: string): ParsedExecutionPreferences {
	const parsed: unknown = JSON.parse(content.trim() || "{}");
	if (!isObject(parsed)) throw new Error("preferences root must be a JSON object");

	const rawProviders = parsed.providers ?? {};
	if (!isObject(rawProviders)) throw new Error("providers must be a JSON object");
	const providers: Record<string, string> = {};
	for (const [role, provider] of Object.entries(rawProviders)) {
		if (typeof provider !== "string") throw new Error(`provider ${role} must be a string`);
		providers[role] = provider;
	}

	const rawPreferences = parsed.preferences ?? [];
	if (!Array.isArray(rawPreferences) || rawPreferences.some((entry) => typeof entry !== "string")) {
		throw new Error("preferences must be an array of strings");
	}

	return {
		document: parsed,
		providers,
		preferences: rawPreferences as string[],
	};
}

export function serializeExecutionPreferences(
	base: ParsedExecutionPreferences,
	roles: readonly string[],
	roleValues: Record<string, string>,
	preferences: string[],
): string {
	const providers = { ...base.providers };
	for (const role of roles) {
		const value = roleValues[role]?.trim() ?? "";
		if (value) providers[role] = value;
		else delete providers[role];
	}
	return JSON.stringify({ ...base.document, providers, preferences }, null, 2);
}
