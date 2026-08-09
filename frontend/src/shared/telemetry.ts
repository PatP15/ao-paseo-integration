import { mkdir, readFile, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { randomUUID } from "node:crypto";

export type TelemetryBootstrap = {
	distinctId: string;
	appVersion: string;
	platform: NodeJS.Platform;
	/**
	 * Event streams the renderer must not send, from
	 * `AO_TELEMETRY_DISABLED_EVENTS`.
	 *
	 * The daemon enforces the same list on its own billed sink, but renderer
	 * events go straight to PostHog and never pass through it. Without carrying
	 * the policy across the boundary, denying `ao.v2.app.active` would silence
	 * the CLI producer while the renderer kept sending under the same exported
	 * name, which is a kill switch that only half works.
	 */
	disabledEvents: string[];
};

/** Parses the comma-separated deny list. Never throws: an unusable entry is inert. */
export function parseDisabledEvents(raw: string | undefined): string[] {
	if (!raw) return [];
	return raw
		.split(",")
		.map((name) => name.trim())
		.filter((name) => name !== "");
}

/**
 * Whether the renderer may export to PostHog. Compiled off in this fork.
 *
 * Upstream returns `isPackaged` here, with `AO_TELEMETRY_RENDERER` as an
 * override. Renderer events — including unhandled exception stack traces — go
 * straight to PostHog without passing through the daemon's sink, so the daemon's
 * kill switch does not cover them and turning the daemon export off is not
 * enough on its own.
 *
 * The signature is kept so callers and their tests are untouched, and so a
 * rebase that changes how the decision is *made* still lands on a function this
 * fork has stubbed rather than on a silently reintroduced export path.
 */
export function rendererTelemetryEnabled(
	env: Record<string, string | undefined>,
	isPackaged: boolean,
): boolean {
	void env;
	void isPackaged;
	return false;
}

export function defaultDataDir(
	platform: NodeJS.Platform,
	env: Record<string, string | undefined>,
	homeDir: string,
): string | null {
	void platform;
	if (env.AO_DATA_DIR) return env.AO_DATA_DIR;
	if (!homeDir) return null;
	return path.join(homeDir, ".ao", "data");
}

export async function loadOrCreateTelemetryInstallId(dataDir: string): Promise<string> {
	const file = path.join(dataDir, "telemetry_install_id");
	try {
		const existing = (await readFile(file, "utf8")).trim();
		if (existing) return existing;
	} catch {
		// Create the id on first use.
	}
	await mkdir(dataDir, { recursive: true });
	const distinctId = `ins_${randomUUID()}`;
	await writeFile(file, `${distinctId}\n`, { mode: 0o600 });
	return distinctId;
}

export async function buildTelemetryBootstrap(
	env: Record<string, string | undefined>,
	appVersion: string,
	platform: NodeJS.Platform,
	homeDir = os.homedir(),
	isPackaged = true,
): Promise<TelemetryBootstrap | null> {
	// Returning null is the whole switch: initTelemetry() bails on a null
	// bootstrap, so the renderer never constructs a PostHog client at all. That
	// is stronger than gating each capture, because it also removes the SDK's
	// own background requests.
	if (!rendererTelemetryEnabled(env, isPackaged)) return null;
	const dataDir = defaultDataDir(platform, env, homeDir);
	if (!dataDir) return null;
	return {
		distinctId: await loadOrCreateTelemetryInstallId(dataDir),
		appVersion,
		platform,
		disabledEvents: parseDisabledEvents(env.AO_TELEMETRY_DISABLED_EVENTS),
	};
}
