# Supply-chain audit: prompt injection and data egress

Source-level audit of **Paseo v0.2.5** and **AO `main` @ 742c77bc** before this fork depends on
either. Five auditors plus three adversarial verifiers; every claim below carries a `file:line`.

Method: read-only reads of complete source trees (Paseo 3,309 files; AO 1,710 files), a
byte-level unicode scan ([`spike/scan-invisible-unicode.py`](spike/scan-invisible-unicode.py)),
and a re-rating pass against **this** deployment's threat model. Nothing was executed; no daemon
was started; no network call was made.

---

## 1. The two questions, answered

### Q1: Is there a prompt-injection attempt in either codebase?

**No.** No deliberate injection, no hidden instructions, no invisible-character smuggling, no
manipulative tool descriptions, no obfuscated payloads. Both codebases are, in this respect, clean.

| Check | Paseo v0.2.5 | AO `main` |
|---|---|---|
| Invisible / bidi / **U+E0000–E007F TAG** codepoints | 11 hits, **all benign** | **zero hits** |
| Hardcoded vendor system prompt injected into your agent | **none** — `system-prompt.ts` is a 10-line joiner with zero literal text | AO's own prompts, by design and readable |
| Manipulative phrasing across 39 tool descriptions + 90 schema strings | **clean** — the only imperative is risk-*reducing* | n/a |
| Obfuscated base64/hex payloads | none | none |
| `postinstall` scripts / typosquat dependencies | none | none |

Paseo's 11 unicode hits are two deliberate UI constants (`const ZERO_WIDTH = "​"`, the standard
React Native full-line-height trick) and nine machine-translation artifacts in
`packages/app/src/i18n/resources/{ar,fr,ru}.ts` — the doubled `​​` beside Arabic and
Cyrillic is a Google Translate signature, and the lone U+200F is a *correct* RTL mark for
`"وضع Vim {{mode}}"`. None are in prompt construction, tool descriptions, or docs.

**But** there is a large *design-level* injection surface: attacker-authored text (GitHub PR and
issue bodies) reaches agent prompts **unfenced** in both products, and in AO it lands in an agent
running with approvals and sandbox disabled by default. See §3.

### Q2: Does data go anywhere except the CLI agents (codex, claude)?

**No, for your code and prompts — with five qualifications, two of which are on by default.**

The core claim holds and is worth stating precisely: **neither product holds an inference API key,
and neither proxies prompts or code through a vendor-operated service.** Agents are launched as
local child processes; content flows to Anthropic/OpenAI directly from the CLI you already trust
(`packages/server/src/server/agent/providers/claude/query.ts:64-97`).

What *does* leave, ranked by how surprising it would be:

| # | Destination | Content | Default | Disable |
|---|---|---|---|---|
| E1 | `wss://relay.paseo.sh` | connection metadata only (E2EE payload) | **ON** | `--no-relay` |
| E2 | PostHog (`us.i.posthog.com`) | AO events, allowlisted props, renderer stack traces | **ON when packaged** | env opt-out **is broken** for Dock launches — needs a patch |
| E3 | Expo → APNs/FCM | **220 chars of assistant output; code deliberately retained** | only if a phone is paired | don't pair, or patch |
| E4 | `github.com` / your forge | branches, PRs, API polling with your `gh` token | expected | — |
| E5 | Anthropic / OpenAI | **everything the agent reads, including the whole repo** | inherent | **none** |
| E6 | `chatgpt.com/backend-api/wham/usage` | OAuth token, undocumented endpoint, spoofed browser UA | on UI hover | remove `services/quota-fetcher/` |

E5 is the honest floor: `claude` and `codex` have whole-repo read access, so "only codex and claude"
means "and therefore Anthropic and OpenAI." The achievable statement is **"nothing leaves except to
Anthropic, OpenAI, and my own forge"** — reachable, but only after items 1–9 of §6.

**Clean results worth keeping:** no Sentry, Segment, Datadog, Amplitude, or Mixpanel anywhere in
Paseo's daemon, CLI, desktop, or mobile. AO's renderer CSP pins loopback + PostHog only. AO's
browser runtime is metadata-only, memory-only, and never accesses cookies. AO's reviewer read-only
guarantee is **actually enforced** in all three adapters (`ports/agent.go:221-228`), not merely
requested in prose — the strongest positive finding in either codebase.

---

## 2. CRITICAL — a composite no single auditor caught

**An agent with shell access can mint permanent, internet-scope remote control of its own daemon,
bypassing the daemon password entirely.**

Four separately-benign facts compose:

1. The pairing offer is exactly `{v, serverId, daemonPublicKeyB64, relay:{endpoint}}`
   (`packages/protocol/src/connection-offer.ts:9-17`).
2. Both inputs live in readable files: `$PASEO_HOME/server-id`, `$PASEO_HOME/daemon-keypair.json`
   (`server-id.ts:63-72`, `daemon-keypair.ts:38-52`). Mode `0600` is **meaningless** here — the agent
   runs as the same uid.
3. `--host` accepts any string containing `#offer=<base64url>`, and the relay path **does not consult
   `PASEO_PASSWORD`** (`websocket-server.ts:894-900`, scopes `["*"]` at `:1231`).
4. The relay is **on by default** (`config.ts:212-217`) and dials out at boot
   (`bootstrap.ts:1543`).

So: a prompt-injected agent reads two files, constructs a URL, and obtains full remote control from
anywhere on the internet — password-proof, over an E2EE channel you cannot inspect.

Worse, the documented rotation procedure does not work. `public-docs/security.md:53` says restarting
rotates it; both files are **persisted and reused across restarts**. **Rotation requires deleting
both files.**

This is the strongest single argument for `--no-relay` in the work zone.

---

## 3. Prompt-injection surface (design, not malice)

### 3.1 The entry point — attacker-authored text, unfenced

| Where | Detail |
|---|---|
| **AO** `lifecycle/reactions.go:722-755` | PR review-comment bodies concatenated into an imperative envelope, **unfenced and uncapped**, then typed into a live pane. **HIGH** |
| **Paseo** `agent/prompt-attachments.ts:127-172` | PR/issue bodies spliced into the real prompt unfenced — while **the same file fences correctly at `:206,:214`** for a throwaway metadata call. The fix is a copy-paste. **HIGH** |
| **AO** `observe/trackerintake/observer.go:269-291` | Raw issue body as user prompt, unsanitized, imperative footer. **MEDIUM** (**HIGH** if `Assignee:"*"`) |
| **AO** `lifecycle/reactions.go:602` | Tracker bot-comment nudge skips `SanitizeControlChars` entirely. **MEDIUM** |

AO *has* the right primitives and doesn't use them here: `<<<BEGIN UNTRUSTED EXTERNAL CONTENT>>>`
markers exist at `frontend/src/main/browser-view-host.ts:237-238`, and a standing instruction exists
at `skillassets/using-ao/commands/browser.md:9-12`. Issue context **is** fenced at
`session_manager/prompt.go:154-158` — so the pattern is established and simply missing from the
comment path.

**`SanitizeControlChars` is Cc-only** (`domain/text.go:19-25`, `unicode.IsControl`). Category **Cf**
passes: U+200B, U+202E, and U+E0041 all survive. So an attacker can hide instructions from a human
reviewer while the model still reads them — the exact vector §1's scan proved absent from the
*source* remains open on *runtime input*.

### 3.2 The multiplier — AO's default worker has no approvals and no sandbox

```go
// backend/internal/adapters/agent/codex/codex.go:383-389
case ports.PermissionModeDefault:
    *cmd = append(*cmd, "--dangerously-bypass-approvals-and-sandbox")
```

Plus `--dangerously-bypass-hook-trust` unconditionally (`codex.go:368`). Claude workers emit no
`--permission-mode` and inherit `~/.claude/settings.json defaultMode` (`claudecode.go:430-433`) —
**shared between both trust zones**.

This converts every MEDIUM in §3.1 into "attacker text reaching an unsandboxed, unapproved agent
holding work credentials." **HIGH, and a PR-0 decision.**

### 3.3 Paseo's invisible elevation channel

`<paseo-system>` messages are treated as elevated-trust **and hidden from every UI**
(`agent-prompt.ts:108-113`, `agent-manager.ts:3516-3520`), the envelope is **spoofable** through
unsanitized `send_agent_prompt` (`agent-prompt.ts:116-119`), and chat mentions carry another agent's
free text inside it (`chat-mentions.ts:118-127`; `@everyone` fans to 25). Net: make another agent do
something, with no trace in its timeline. **HIGH** — transcript visibility is the only cross-zone
detector available, and this is precisely what transcripts don't show.

Also persistent and agent-writable: `~/.paseo/orchestration-preferences.json` is woven into every
new agent prompt (`skills/paseo/SKILL.md:82,87`), is **not** gated by `mcpInjectIntoAgents`, and any
agent with a shell can append to it. Self-propagating across zones and restarts. **HIGH.**

### 3.4 Two things shipped in the tree that publish or over-permission

- **`skills/bug-triage/`** (AO) files issues and PRs on **`AgentWrapper/agent-orchestrator`** — a
  public repo you don't control (`:56-57`, `:322-324`). An auto-triggering skill that publishes logs,
  repro steps, and paths. **HIGH — delete in PR 0.**
- **`skills/paseo/SKILL.md:124-125`** ships canonical examples using `--mode full-access`. Agents are
  *instructed* to read this file and will pattern-match it. **HIGH, one-line fix.**
- **AO reviewer allowlist `Bash(gh:*)`** (`adapters/reviewer/claudecode/claudecode.go:44-55`),
  **default-on** (`domain/projectconfig.go:85`), consuming attacker-influenced PR context. With a work
  token that's `gh api --method POST/DELETE` against every private repo in the org. **HIGH.**
- **AO's compiled-in "push the branch and open a PR"** instruction (`prompt.go:61,67,228,252`) — **no
  flag exists**; `ProjectConfig.AgentRules` only appends after it. **HIGH for work projects.**

---

## 4. The structural finding: neither product has an intra-host trust boundary

Both define "same uid" as fully trusted:

- `backend/internal/config/config.go:19-22` — *"the daemon has no auth/CORS/TLS"*
- `packages/server/src/server/auth.ts:168` — `if (!input.password) { return true; }`

**An agent is not an operator.** It is prompt-injectable code fed remote content. So every finding
whose mitigation reads "requires local access" is, in a two-zone single-uid setup, reachable by a
hobby agent that read a poisoned GitHub issue — with work credentials on the other side.

Concrete cross-zone paths, all verified:

| Path | Evidence |
|---|---|
| Hobby agent reads work agent's **full transcript** (source, diffs) — and, being an LLM session, transmits it to *its own* vendor | `paseo-tools.ts:2979` `get_agent_activity`, unscoped; `ensureAgentLoaded` rehydrates archived agents from disk |
| Hobby agent **approves** work agent's pending permission prompts | `paseo-tools.ts:3093` + `:3059`; `permission-response.ts:22-40` has no ownership check |
| Hobby agent flips work agent to `bypassPermissions` | `paseo-tools.ts:3035`; `lifecycle-command.ts:206-215`, no ownership check |
| Arbitrary code execution in the work checkout, `~/.ssh`, `~/.aws`, `~/.codex/auth.json` | `create_terminal(cwd:"/abs")` + `send_terminal_keys` (`:2353`, `:2467`); `path-utils.ts:22-28` returns absolute/`~` paths unchanged; `lockedCwd` wired only for voice |
| Work `gh` token / `PASEO_PASSWORD` readable by any agent, landing in its transcript → its vendor | `paseo-env.ts:4-10` strips only **5** runtime-control keys |
| Hobby agent → `curl 127.0.0.1:3001/api/v1/projects` → work project `env` **secrets**; → `POST /sessions/{id}/send` → command execution in the work agent | AO loopback daemon, no auth (`config.go:19-27`); `service/project/types.go:25` |
| No agent identity exists to scope against | `bootstrap.ts:1357-1364` reads `callerAgentId` from the **query string**; one daemon-wide token at `:570` |

### Correcting an earlier statement

I previously told you Paseo's 38-tool MCP catalog is *"injected into every agent."* **That was
wrong** — injection is opt-in and defaults **off**
(`config.ts:429-430`: `?? false`; documented at `public-docs/mcp.md:13`). Two things make the
practical conclusion survive anyway, and they matter more than the default:

1. **It fails open for programmatic embedders.** `agent-manager.ts:578` is
   `private paseoToolsEnabled = true`, and `bootstrap.ts:510` writes `?? true`. A fork that
   constructs config in code — which is this integration's likely shape — inherits injection **ON**.
2. **`--no-inject-mcp` is insufficient.** `mcpEnabled` defaults **true** (`config.ts:428`) and
   `/mcp/agents` is password-exempt (`auth.ts:145,168`), so any agent with Bash can just `curl` it.
   **You need `--no-mcp`.**

There are 39 tools, not 38.

---

## 5. Full findings by severity

**CRITICAL** — §2 pairing-credential composite · cross-agent transcript read · cross-agent
permission approval · cross-agent `bypassPermissions` flip · arbitrary code execution via
terminal tools · daemon env (incl. work tokens) inherited by every agent.

**HIGH** — `--no-mcp` needed not `--no-inject-mcp` · injection fails open for embedders ·
self-asserted `callerAgentId` · relay on by default · password doesn't gate relay sockets ·
non-expiring pairing offer with false rotation docs · Expo push carries code (both products) ·
default CORS allows `https://app.paseo.sh` full local control · provider config can replace binary +
`ANTHROPIC_BASE_URL` · `<paseo-system>` invisible elevation · unfenced PR/issue text (both) ·
`orchestration-preferences.json` · schedule/heartbeat persistence · `capture_terminal` cross-zone
read · AO codex bypass default · AO autonomous push/PR with no flag · reviewer `Bash(gh:*)` ·
`skills/bug-triage` publishing upstream · `--mode full-access` examples · PostHog on with a broken
opt-out · AO LAN bridge (if enabled) · any-loopback-origin trust · AO no-auth loopback daemon ·
plain `ws://` to a LAN worker.

**MEDIUM** — Cf characters survive sanitization · quota-fetcher rotates work OAuth credentials ·
undocumented `chatgpt.com` endpoint with spoofed UA · `/api/files/download` password-exempt ·
`ProjectConfig.Env` plaintext at rest · weak `sha256(folder-name)` project ID · `/mux`
`InsecureSkipVerify` (saved only by CORS) · hook installer rewriting shared `~/.claude` ·
`security.md` omitting the injection class · shared `gh` token resolution · browser-tool page text →
vendor · host-page toggle default mismatch (**UNVERIFIED**).

**LOW / INFORMATIONAL** — voice-mode prompt (opt-in, strippable) · `daemonAppendSystemPrompt`
excluded from snapshots · speech model download · desktop/mobile update checks · prompt on argv
visible in `ps` · `tmux.go:1013` unquoted `export` keys · LAN password modulo bias.

**No mitigation exists** — Anthropic/OpenAI receive everything their CLI reads · `git`/`gh` traffic
to your forge · Expo push content (config-key-free in both) · Paseo desktop update check · AO's
compiled-in push/PR instruction · pairing-offer derivability from `$PASEO_HOME`.

---

## 6. Remediation, prioritized

**Blocking for PR 0** — these change the architecture or the fork decision:

1. **Two OS users on the Mac**, per-zone `AO_DATA_DIR`, `PASEO_HOME`, and ports, `chmod 700` homes,
   work `gh`/`~/.claude`/`~/.codex` credentials **only** in the work home. Every cross-zone finding
   reduces to "same uid," and retrofitting after state exists is expensive. Separate `PASEO_HOME`
   alone is **not** sufficient — `create_terminal` with an absolute `cwd` crosses it trivially.
2. **Windows/Unity box is single-zone.** Never dual-purpose it.
3. **`--no-mcp --no-relay --no-web-ui --listen 127.0.0.1:<port>`** on every AO-owned daemon, plus
   `daemon.mcp.enabled/injectIntoAgents/browserTools.enabled: false`.
4. **Fork-patch injection to fail closed:** `bootstrap.ts:510,1279,1441,1443`,
   `agent-manager.ts:578`.
5. **Fork-patch agent env** (`paseo-env.ts:4`, or make `createProviderEnv` an allowlist) so
   `PASEO_PASSWORD`, `GH_TOKEN`, `GITHUB_TOKEN`, `AWS_*`, `AO_*`, `*_API_KEY` never reach an agent.
6. **Relay off in the work zone**; Mac↔Windows over **Tailscale + SSH/TLS**. Paseo's daemon is
   `http`-only (`bootstrap.ts:2`) — never bind it to a LAN interface.
7. **Compile telemetry off:** `frontend/src/main.ts:413-416` → `"off"`,
   `frontend/src/shared/telemetry.ts:47` → `return false`, empty `VITE_AO_POSTHOG_KEY`. The env
   opt-out does **not** survive a Dock launch (`main.ts:519` merges overrides last over
   `shell-env.ts:86-89`).
8. **AO codex permission default** off `--dangerously-bypass-approvals-and-sandbox`.
9. **`rm -rf skills/bug-triage/`.**
10. **Decide push posture now** — no device on the work daemon, or patch
    `agent-attention-notification.ts:56` and `push/dispatcher.go:224`.
11. **Bake config assertions into a setup script:** `daemon.cors.allowedOrigins: []`;
    `hub-relationship.json` and `push-tokens.json` absent; no custom provider `command`/`env`; AO LAN
    bridge off; AO auto-update off.

**Scheduled hardening** — fence and cap AO's comment nudge with AO's own markers
(`reactions.go:722-755`); `SanitizeControlChars` at `reactions.go:602` and
`trackerintake/observer.go:288`; extend `domain/text.go:19-25` to strip Cf and U+E0000–E007F; fence
Paseo's `prompt-attachments.ts:127-172`; patch out AO's push/PR instruction; narrow the reviewer
allowlist to `Bash(gh pr review:*)` / `Bash(gh api --method GET:*)`; make `<paseo-system>` visible in
the timeline; skip the skills that read `orchestration-preferences.json`; drop `--mode full-access`
from the examples; `/mux` cross-origin regression test; remove `services/quota-fetcher/`; delete
`packages/server/src/server/hub/`; encrypt or externalize `ProjectConfig.Env`; add an in-app
telemetry toggle; quote `export` keys at `tmux.go:1013-1017`. If Paseo's tool catalog is ever
re-enabled, scope caller identity to a per-agent token first (`bootstrap.ts:570,1357`) and add
ownership checks to the ten cross-agent tools plus `lockedCwd` confinement.

**Rotation runbook.** If a pairing offer is ever exposed: delete **both** `$PASEO_HOME/server-id`
and `$PASEO_HOME/daemon-keypair.json`, then restart. Restarting alone does nothing.

---

## 7. Coverage and gaps

**Covered:** complete Paseo and AO trees for injection surface and egress; byte-level unicode scan of
every text file; `package.json`/`go.mod` for `postinstall` and typosquats; adversarial verification of
every "clean" conclusion, of egress completeness, and of severity calibration.

**Not covered, ranked by how much it matters here:**

1. **Windows-specific conpty / `cmd.exe` paths** — the Unity box is half this topology and no auditor
   examined it.
2. **Paseo's browser tools** — an untrusted-web-content-to-model path nobody looked at.
3. Image-borne injection; dependency internals; the Fly.io relay upstream;
   `packages/{app,desktop,website}`; AO's `frontend/src/landing`; ~15 unaudited AO agent adapters;
   CI/supply chain.

**Unverified, worth settling cheaply:** whether the host-page toggle
(`packages/app/.../host-page.tsx:1041`, renders ON when `undefined`) can be observed disagreeing with
the server default; and whether an agent can force a daemon restart to activate a hostile
`config.json` it wrote.

**Method limits:** this was a source read. Nothing was executed, so runtime behavior, actual network
traffic, and provider-specific streaming were not observed. Findings are code-level, and a few
exploitability judgements are marked UNVERIFIED above.

*Not legal or compliance advice. For work-restricted projects, treat §1 Q2 and §6 as inputs to
whatever review your employer requires — several items are policy questions, not just technical ones.*
