// Blanked in this fork. Upstream compiles its production PostHog project key in
// here, and both the daemon override (main.ts) and the renderer
// (renderer/lib/telemetry.ts) fall back to it whenever their env var is unset or
// empty — so emptying VITE_AO_POSTHOG_KEY alone does not stop an export, it just
// re-selects this constant. Blanking it is what makes the opt-out real, and it
// removes any path by which a fork build could bill events to upstream's
// project. Keep the export: it is imported in four places and its absence would
// be a build error, not a silent success.
export const DEFAULT_POSTHOG_PROJECT_KEY = "";
export const DEFAULT_POSTHOG_HOST = "https://us.i.posthog.com";
