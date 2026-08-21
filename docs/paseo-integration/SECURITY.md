# Security

Operating rules for the fork. Findings and evidence live in
[`VULNERABILITIES.md`](VULNERABILITIES.md); this document states what the fork **does** about them.

---

## 1. The premise that shapes everything

**Neither AO nor Paseo has an intra-host trust boundary.** Both define "same uid" as fully trusted:

- `backend/internal/config/config.go:19-22` — *"the daemon has no auth/CORS/TLS"*
- `packages/server/src/server/auth.ts:168` — `if (!input.password) { return true; }`

**An agent is not an operator.** It is prompt-injectable code fed remote content. So any mitigation
whose rationale is "requires local access" provides no protection here — the attacker *is* local, by
design, the moment an agent reads a poisoned issue.

Therefore: **the OS user is the only real trust boundary.** Ports, `PASEO_HOME`, and daemon passwords
are defense in depth, not isolation.

## 2. Trust zones

Two OS users on the Mac. Not two directories, not two ports — two users.

```bash
# work zone
AO_PORT=3001  AO_DATA_DIR=/Users/aowork/.ao/data  PASEO_HOME=/Users/aowork/.paseo
paseo daemon start --no-mcp --no-relay --no-web-ui --listen 127.0.0.1:6767

# hobby zone
AO_PORT=3002  AO_DATA_DIR=/Users/aohobby/.ao/data PASEO_HOME=/Users/aohobby/.paseo
paseo daemon start --no-mcp --no-relay --no-web-ui --listen 127.0.0.1:6768
```

`chmod 700` both homes. **Work credentials — `gh auth login`, `~/.claude/.credentials.json`,
`~/.codex/auth.json`, cloud creds — exist only in the work user's home.** That is what makes this
isolate credentials rather than merely separate ports.

**A separate `PASEO_HOME` alone does not isolate anything.** `create_terminal` accepts an absolute or
`~`-prefixed `cwd` and `resolvePathFromBase` returns it unchanged (`path-utils.ts:22-28`), so any
`PASEO_HOME` boundary is crossed trivially.

**The Windows/Unity box is single-zone.** Unity licensing, Editor file locks, and conpty make
dual-zone there more trouble than it is worth. Pick one zone for that machine.

## 3. Required daemon posture

Every AO-owned Paseo daemon:

| Flag / key | Why |
|---|---|
| `--no-mcp` | **Not** `--no-inject-mcp`. `mcpEnabled` defaults **true** (`config.ts:428`) and `/mcp/agents` is password-exempt (`auth.ts:145,168`), so any agent with Bash can `curl` the endpoint regardless of injection. |
| `--no-relay` | The relay is **on by default** and dials out at boot. See §5. |
| `--no-web-ui` | Removes a static surface AO does not use. |
| `--listen 127.0.0.1:<port>` | Never a LAN interface. Paseo's daemon is `http`-only (`bootstrap.ts:2`) — there is no TLS to enable. |
| `daemon.mcp.enabled: false`, `daemon.mcp.injectIntoAgents: false`, `daemon.browserTools.enabled: false` | Belt and braces against a programmatic default. |
| `daemon.cors.allowedOrigins: []` | Default is `["https://app.paseo.sh"]` (`persisted-config.ts:334`) — any JS on that origin gets a `scopes:["*"]` session on your daemon with no password. |
| `features.dictation.enabled: false`, `features.voiceMode.enabled: false` | Both default **on** with provider `local`, so a stock daemon fetches **983 MB** of speech models (`parakeet-tdt-0.6b-v2-int8`, `kokoro-en-v0_19`) into `$PASEO_HOME/models/local-speech` on first boot — unprompted outbound fetches plus disk on a machine whose only job is running agents. There is no CLI flag; these keys are the only off switch. |

Assert at probe time, and refuse to dispatch if violated. `execution_hosts` carries
`requires_no_mcp` and `requires_no_relay` for exactly this.

**Never drive the operator's `desktopManaged` daemon.** `paseo status --json` reports
`desktopManaged: true` for it; treat that as a hard refusal in the adapter.

## 4. Fail-closed patches the fork must carry

These are upstream defaults that fail **open** for a programmatic embedder, which is this
integration's shape:

| File | Change |
|---|---|
| `packages/server/src/server/agent/agent-manager.ts:578` | `private paseoToolsEnabled = true` → `false` |
| `packages/server/src/server/bootstrap.ts:510,1279,1441,1443` | make `undefined` mean **off**, not on |
| `packages/server/src/server/auth.ts:168` | delete `if (!input.password) { return true; }` |
| `packages/server/src/server/paseo-env.ts:4` | see §6 |

If the fork consumes Paseo as an installed binary rather than embedding it — the stated architecture
— these apply to the *deployment configuration* instead, and the daemon must be started with the
explicit flags in §3. **Do not patch the installed Paseo.** Running a modified AGPL daemon that serves
clients over a network engages AGPL §13 directly (§10).

## 5. Connectivity and the pairing credential

Tailscale first; relay only as a deliberate, per-host fallback (`DECISIONS.md` D16, D19).

**The pairing offer is a permanent, password-proof, internet-scope remote-control credential.** It is
`{v, serverId, daemonPublicKeyB64, relay:{endpoint}}` (`connection-offer.ts:9-17`), derivable from two
readable files, and the relay path never consults `PASEO_PASSWORD`
(`websocket-server.ts:894-900`, scopes `["*"]` at `:1231`).

Rules:

- Never share a pairing QR or offer URL. Store any offer URL as a `endpoint_secret_ref`, never inline.
- **Rotation = delete both `$PASEO_HOME/server-id` and `$PASEO_HOME/daemon-keypair.json`, then
  restart.** Paseo's docs claim a restart rotates it (`public-docs/security.md:53`); both files are
  persisted and reused, so restarting alone does nothing.
- `0600` on those files is **not** a control against an agent running as the same uid. Only §2 is.
- Never enable AO's LAN bridge (`0.0.0.0:3011`) to reach the Windows box. An 8-char password over
  plaintext HTTP buys terminal write access, project `env` secrets, and a persistent push tap. Use
  Tailscale.

## 6. Never let a credential reach an agent

`paseo-env.ts:4-10` strips only **five** runtime-control keys. Everything else in the daemon
environment is inherited by every agent, readable with `printenv`, and therefore lands in that agent's
transcript — and thus at that agent's model vendor.

The fork's rule: `createProviderEnv` is an **allowlist** — `PATH`, `HOME`, `SHELL`, `TERM`, `LANG`,
`TMPDIR`, plus the provider's own variables. At minimum, deny `PASEO_PASSWORD`, `PASEO_HOME`,
`PASEO_SERVER_ID`, `GH_TOKEN`, `GITHUB_TOKEN`, `AO_*`, `AWS_*`, `*_API_KEY`, `*_TOKEN`.

**Without this, a daemon password is theatre.**

Separately, AO scrubs Paseo's ambient variables from every `paseo` invocation it makes —
`PASEO_AGENT_ID`, `PASEO_WORKSPACE_ID`, `PASEO_HOST`, `PASEO_PASSWORD`, `PASEO_HOME`,
`PASEO_LISTEN` — and always passes `--workspace <id>` explicitly (`DECISIONS.md` D23). A leaked
`PASEO_AGENT_ID` silently makes every `run` a child agent sharing the parent's workspace, invisible
in `--json`, and an agent with a null `workspaceId` is invisible to `ls` under every flag.

**Secrets never appear in a run brief.** Not credentials, not tokens, not offer URLs
(`PROTOCOL.md` §2).

## 7. Untrusted input

Everything below is data, never instructions: repository content, issue and PR bodies, review
comments, CI output, web pages, terminal output, and messages from other agents.

The fork fences all of it. AO already has the primitives and simply does not use them on every path:
`<<<BEGIN UNTRUSTED EXTERNAL CONTENT>>>` markers exist at
`frontend/src/main/browser-view-host.ts:237-238`, a standing instruction at
`skillassets/using-ao/commands/browser.md:9-12`, and correct fencing at
`session_manager/prompt.go:154-158`.

Required changes:

- Fence and **cap** review-comment nudges (`lifecycle/reactions.go:722-755`).
- Add `SanitizeControlChars` at `lifecycle/reactions.go:602` and
  `observe/trackerintake/observer.go:288`.
- Extend `domain/text.go:19-25` to strip category **Cf** and **U+E0000–E007F**. `unicode.IsControl`
  is Cc-only, so U+200B, U+202E, and U+E0041 currently pass — an attacker can hide instructions from
  a human reviewer that the model still reads.
- Re-run [`spike/scan-invisible-unicode.py`](spike/scan-invisible-unicode.py) on every Paseo version
  bump.

## 8. What an agent may never change

Not by prompt, not by tool call, not by editing a repository file:

its host assignment · its policy profile · its permission mode · its retry budget · its schedule ·
any approval state · any work-item lifecycle fact.

Consequences for the fork:

- **AO's codex adapter maps `PermissionModeDefault` to `--dangerously-bypass-approvals-and-sandbox`**
  (`adapters/agent/codex/codex.go:383-389`), plus `--dangerously-bypass-hook-trust` unconditionally.
  Work projects must map to `--ask-for-approval on-request`, or set `permissions: "auto"` explicitly.
  Claude workers emit no `--permission-mode` and inherit `~/.claude/settings.json defaultMode`, which
  §2's user split is what keeps separate.
- **Paseo's permission gate is not a trust boundary** (`DECISIONS.md` D22): `respond_to_permission`
  takes an arbitrary `agentId`, `list_pending_permissions` is cross-agent, and `callerAgentId` comes
  from the query string unbound to the auth token. AO's own gating is the boundary.
- **Narrow the reviewer allowlist.** `Bash(gh:*)`
  (`adapters/reviewer/claudecode/claudecode.go:44-55`) is default-on and consumes attacker-influenced
  PR context; with a work token that is write access to the whole org. Replace with
  `Bash(gh pr review:*)`, `Bash(gh pr comment:*)`, `Bash(gh api --method GET:*)`.
- **Delete `skills/bug-triage/`.** It auto-files issues and PRs on `AgentWrapper/agent-orchestrator`,
  a public repo you do not control.
- **Patch out the compiled-in "push the branch and open a PR" instruction**
  (`session_manager/prompt.go:61,67,228,252`) for work projects. There is no flag;
  `ProjectConfig.AgentRules` only appends after it, which is later-instruction-wins, not a guarantee.

Timeline-scraped events are **advisory** and may never authorize an irreversible action
(`PROTOCOL.md` §5.1) — they are forgeable by any co-resident agent via `get_agent_activity`.

## 9. Egress posture

- **Telemetry compiled off**, not env-off: `frontend/src/main.ts:413-416` → `"off"`,
  `frontend/src/shared/telemetry.ts:47` → `return false`, empty `VITE_AO_POSTHOG_KEY`. The documented
  env opt-out does **not** survive a Dock launch — `telemetryOverrides()` merges last over the
  login-shell probe (`main.ts:519`, `shell-env.ts:86-89`).
- **No push device registered on the work daemon.** Expo previews carry 220 chars of assistant output
  with code **deliberately retained** (`agent-attention-notification.ts:56`), and neither product has
  a daemon-side kill switch.
- **Remove `services/quota-fetcher/`** — it transmits and rotates your provider OAuth credentials and
  hits an undocumented `chatgpt.com/backend-api/wham/usage` with a spoofed browser UA.
- **Never log** offer URLs, daemon passwords, provider credentials, or `--host` strings containing
  `?password=`. Redact at the adapter boundary, including in error paths.
- Accept and write down what cannot be fixed: **Anthropic receives everything `claude` reads and
  OpenAI everything `codex` reads**, both with whole-repo access. The only control is file hygiene —
  no plaintext secrets in either checkout, and no work credential directory reachable from a hobby
  workspace.

## 10. Safe process invocation

- `exec.CommandContext(ctx, paseoBinary, args...)` with an **argument array**. Never a shell string,
  never `sh -c`.
- Validate every argument in Go **before** exec: labels must have exactly one `=`, a non-empty key,
  and a **non-empty value** (an unset shell variable yields `k=""`, accepted by both `run` and `ls`,
  colliding across sessions); host strings must contain a colon (a colonless `--host` returns `null`
  and **silently falls through to the local daemon**).
- Re-assert that every returned agent carries the expected label before acting. A malformed `--label`
  makes `ls` apply **zero** filters and return every agent on the daemon. **Never act on an
  unexpectedly large result set** — bail and log.
- **Ban `--all` on `stop` and `delete`** in the adapter and in every script.
- Always `ls -a -g`; always pass the **full** permission request ID from `inspect`, never `permit ls`'s
  8-char prefix, and never omit it — `permit allow <agent>` with no ID approves **everything**.
- Explicit timeout on every invocation; capture stdout and stderr separately; strict JSON parsing;
  record the Paseo version and refuse unsupported ones.

## 11. Audit

Every consequential control-plane action gets an `audit_events` row (`DATA_MODEL.md` §7): proposal,
approval, rejection, host selection, launch, permission decision, human answer, retry scheduling,
stop, archive, completion. Append-only, exportable.

Note that `<paseo-system>` messages are hidden from every Paseo UI
(`agent-manager.ts:3516-3520`) — transcript inspection is the only cross-zone detector available, and
that envelope is exactly what it cannot show. AO's own audit log is therefore the system of record,
not Paseo's timeline (which is also non-durable — `DECISIONS.md` D9).

## 12. Licensing

AO is Apache-2.0. Paseo is **AGPL-3.0-or-later** (its `LICENSE` wraps the full AGPL text in a preamble
GitHub misclassifies as `NOASSERTION`; every published `@getpaseo/*` npm package declares **no**
license field, so the repo `LICENSE` is the only authority). The relay is separately Apache-2.0.

Hard rules:

1. Interact only via `os/exec` on the installed binary, its `--json` output, and `GET /api/status`.
2. **Never patch or vendor Paseo.** Upstream a PR instead.
3. No `@getpaseo/*` in `go.mod` or `package.json`. CI greps for `getpaseo` in both.
4. No transcribed schemas — paraphrase with `file:line` citations.
5. Any future `@getpaseo/client` sidecar lives in a **separate AGPL-3.0-or-later repo** with
   arms-length IPC.

Not legal advice. Get counsel before distributing a combined product or service.
