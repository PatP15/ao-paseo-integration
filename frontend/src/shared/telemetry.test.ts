import { mkdtemp, readFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, expect, test } from "vitest";
import {
	buildTelemetryBootstrap,
	defaultDataDir,
	loadOrCreateTelemetryInstallId,
	parseDisabledEvents,
	rendererTelemetryEnabled,
} from "./telemetry";

const tempDirs: string[] = [];

afterEach(async () => {
	await Promise.all(
		tempDirs
			.splice(0)
			.map((dir) => import("node:fs/promises").then(({ rm }) => rm(dir, { recursive: true, force: true }))),
	);
});

test("defaultDataDir prefers AO_DATA_DIR", () => {
	expect(defaultDataDir("linux", { AO_DATA_DIR: "/tmp/custom" }, "/home/test")).toBe("/tmp/custom");
});

test("loadOrCreateTelemetryInstallId persists a stable install id", async () => {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-telemetry-"));
	tempDirs.push(dir);

	const first = await loadOrCreateTelemetryInstallId(dir);
	const second = await loadOrCreateTelemetryInstallId(dir);
	const stored = (await readFile(path.join(dir, "telemetry_install_id"), "utf8")).trim();

	expect(first).toMatch(/^ins_/);
	expect(second).toBe(first);
	expect(stored).toBe(first);
});

test("buildTelemetryBootstrap returns null when no home dir is available", async () => {
	await expect(buildTelemetryBootstrap({}, "1.2.3", "linux", "")).resolves.toBeNull();
});

// This fork compiles renderer export off. Upstream returns isPackaged here with
// AO_TELEMETRY_RENDERER as an override; renderer events bypass the daemon's sink
// entirely, so the daemon kill switch does not reach them.
test("renderer telemetry is compiled off, in every posture", () => {
	expect(rendererTelemetryEnabled({}, false)).toBe(false);
	expect(rendererTelemetryEnabled({}, true)).toBe(false);
	expect(rendererTelemetryEnabled({ AO_TELEMETRY_RENDERER: " ON " }, false)).toBe(false);
	expect(rendererTelemetryEnabled({ AO_TELEMETRY_RENDERER: "on" }, true)).toBe(false);
	expect(rendererTelemetryEnabled({ AO_TELEMETRY_RENDERER: "off" }, true)).toBe(false);
});

// A null bootstrap is the switch itself: initTelemetry bails on it, so the
// renderer never constructs a PostHog client at all — which also removes the
// SDK's own background requests, not just the captures.
test("buildTelemetryBootstrap withholds the bootstrap on a packaged build too", async () => {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-telemetry-"));
	tempDirs.push(dir);

	await expect(buildTelemetryBootstrap({}, "0.11.2", "linux", dir, false)).resolves.toBeNull();
	await expect(buildTelemetryBootstrap({}, "0.11.2", "linux", dir, true)).resolves.toBeNull();
	// The opt-in that upstream honors must not resurrect the export path: a
	// packaged app launched from the Dock inherits whatever env the launcher
	// had, which is not the operator's considered choice.
	await expect(
		buildTelemetryBootstrap({ AO_TELEMETRY_RENDERER: "on" }, "0.11.2", "linux", dir, true),
	).resolves.toBeNull();
});

// parseDisabledEvents still parses: the deny list remains the daemon-side kill
// switch, and nothing about compiling the renderer export off should change how
// that policy is read.
test("parseDisabledEvents carries the deny list across the process boundary", () => {
	expect(parseDisabledEvents("ao.v2.app.active, ao.renderer.* ,, ")).toEqual([
		"ao.v2.app.active",
		"ao.renderer.*",
	]);
});

test("parseDisabledEvents treats absent or blank policy as no policy", () => {
	expect(parseDisabledEvents(undefined)).toEqual([]);
	expect(parseDisabledEvents(" , , ")).toEqual([]);
});
