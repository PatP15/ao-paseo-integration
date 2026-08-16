> **Fork notice — ao-paseo-integration**
>
> This is a fork of [Untrivial-ai/agent-orchestrator](https://github.com/Untrivial-ai/agent-orchestrator)
> (Apache-2.0) that adds a first-class `ExecutionBackend` port so AO can drive **stock
> [Paseo](https://paseo.sh) installations as remote execution backends**, while remaining the sole
> durable control plane.
>
> Paseo is **not** forked and its source is **not** vendored here — it is a separate installed
> program, interacted with only through its CLI. Paseo is AGPL-3.0-or-later; this fork stays
> Apache-2.0. See [`docs/paseo-integration/`](docs/paseo-integration/) for the design documents,
> the compatibility spike, and the supply-chain audit.
>
> **Setup for this fork is below.** Upstream README follows after it.

---

## Setup (this fork)

Three parts: local work on this machine, adding remote computers, and keeping
the files that steer agents (preferences, instructions, skills) in sync.

### 1. Local work

Everything upstream AO does works unchanged; remote execution is additive.

1. **Prerequisites**: Go 1.25+, Node 20+, and at least one agent CLI you are
   logged into (`claude`, `codex`, …). For remote features you also need the
   **Paseo CLI, pinned 0.2.5**, on your `PATH` (install from [paseo.sh](https://paseo.sh);
   the desktop app bundles it at `/Applications/Paseo.app/Contents/Resources/bin/paseo`).
2. **Install dependencies** from the repo root:

   ```bash
   npm install
   npm --prefix frontend install
   ```

3. **Build the CLI/daemon**:

   ```bash
   cd backend && go build -o "$HOME/.local/bin/ao" ./cmd/ao
   ```

4. **Start AO**: `ao start` opens the desktop app (starting the daemon if
   needed); `ao status` / `ao stop` manage it. All state lives under `~/.ao`
   (override with `AO_DATA_DIR`) — never in `~/Library/Application Support`.
5. **Add a project and work locally**:

   ```bash
   ao project add --path ~/code/my-repo --name my-repo
   ao spawn ...   # or start sessions from the board in the app
   ```

6. **Verify a checkout** before hacking on this fork: `npm run lint`
   (backend tests + golangci-lint, must print `0 issues.`),
   `npm run frontend:typecheck`, and `npm --prefix frontend run test`.

### 2. Remote hosts (computers)

AO dispatches **approved work items** to stock Paseo daemons on other
machines. Hosts are registered by hand and selected by AO's router — never
named at dispatch time.

**On the worker machine:**

1. Install the agent CLIs and **log them in as the user that will run work**
   (an agent on a machine with no `claude` login just answers
   "Not logged in").
2. Install Paseo 0.2.5 and start its daemon with a password, reachable from
   your AO machine (LAN IP or Tailscale name), with MCP injection off:

   ```bash
   PASEO_PASSWORD='<a strong password>' paseo daemon start \
     --listen 0.0.0.0:6780 --no-relay --no-mcp --no-web-ui
   ```

3. Make sure the project repo is checked out somewhere on this machine —
   you will need that absolute path for the binding.

**On the AO machine:**

4. **Store the credential as a secret ref** — a file, never a value inside an
   endpoint or command line:

   ```bash
   mkdir -p ~/.ao/secrets && chmod 700 ~/.ao/secrets
   printf '%s' '<the password>' > ~/.ao/secrets/office-mac-pw
   chmod 600 ~/.ao/secrets/office-mac-pw
   ```

   (The **Settings → Computers → Add computer** sheet does this for you when
   you paste the password there.)
5. **Register the computer** — in the app via *Settings → Computers → Add
   computer* (connection → details → review), or:

   ```bash
   ao remote register office-mac --name "Office Mac" \
     --transport tailscale --endpoint office-mac.tailnet.ts.net:6780 \
     --secret-ref office-mac-pw --trust-zone work --max-sessions 3
   ```

   The endpoint must contain a `host:port` colon. Registering an endpoint that
   resolves to the AO machine's own daemon is refused (`HOST_IS_SELF`).
6. **Test the connection** with the row's *Test connection* button (or
   `ao remote hosts` to see reachability). A computer that has never been
   probed shows a gray dot; online is green.
7. **Bind your project to the computer** — project settings → *Computers*
   section → *Bind computer*, or:

   ```bash
   ao remote bind my-repo office-mac \
     --host-path /Users/me/code/my-repo --base-branch main
   ```

   The path is the repo's location **on the host**. An unbound project has no
   candidate hosts and dispatch fails with `NO_ELIGIBLE_HOST`.
8. **Dispatch work**: create a work item (*Work items* view or
   `ao work-item add`), **approve it** (approval is the gate — nothing
   dispatches without it), then *Dispatch* from the app (pick provider, model,
   mode, and thinking level — all validated live against that computer) or
   `ao remote dispatch`. You can also create-and-run in one step from the New
   Task dialog's *Run on* section.
9. **Supervise**: the board card shows a *Monitor* badge; opening it gives the
   remote session pane — event timeline, message composer for follow-up
   prompts, and kill. Agent questions land in notifications and the inbox
   (`ao remote inbox` / `answer` / `allow` / `deny`). If the computer stops
   answering, the banner says so — unreachability never terminates a session.

### 3. Updating preferences, instructions, and skills

Click a computer's name (in *Settings → Computers*) to open its detail view.
Every write below is drift-checked: AO refuses to overwrite a file that
changed on the host since you last read it — refresh, re-read, retry.

- **Preferences tab** — edits the host's
  `~/.paseo/orchestration-preferences.json` (the provider map Paseo skills
  read). Role pickers are filled from the computer's **live** provider
  catalog, so you can only select models that actually exist there.
- **Instructions tab** — edits the host's machine-scope `~/.claude/CLAUDE.md`
  over the same channel, with the same drift discipline.
- **Skills tab** — inventories `~/.claude/skills` on every computer and
  pushes a skill from this machine to a host (staged, hash-verified,
  byte-identical, atomic). Skills that orchestrate through Paseo are marked
  *policy-gated*; inserting one at dispatch requires an explicit, audited
  override.
- **Schedules tab** — lists schedules found on the host's daemon. AO owns
  scheduling, so these are flagged as policy violations; delete them here.
- **Project instructions** (project settings) — the committed instruction
  files (`CLAUDE.md`, `AGENTS.md`, `.claude/skills/**`) are versioned code,
  shown read-only with *Edit via task*; each bound computer shows live drift
  against them with one-click *Sync* (fast-forward only — a diverged checkout
  is refused with git's own words and must be reconciled on that machine).
- **Auto-resume** (*Settings → Auto-resume*) — toggle *Resume after a usage
  limit* and edit the resume prompt sent to interrupted agents (empty field
  restores the default). Applies to local and remote sessions; resumes are
  capped per session and recorded in the timeline.

---

<div align="center">
  <img src="assets/ao-logo.svg" alt="Agent Orchestrator" width="160" height="160" />

# Agent Orchestrator

**The orchestration layer for parallel AI coding agents**

[![Stars](https://img.shields.io/github/stars/Untrivial-ai/agent-orchestrator)](https://github.com/Untrivial-ai/agent-orchestrator/stargazers)
[![Contributors](https://img.shields.io/github/contributors/Untrivial-ai/agent-orchestrator)](https://github.com/Untrivial-ai/agent-orchestrator/graphs/contributors)
[![Twitter](https://img.shields.io/badge/Twitter-1DA1F2?logo=twitter&logoColor=white)](https://x.com/aoagents)
[![Discord](https://img.shields.io/badge/Discord-join%20the%20community-5865F2?logo=discord&logoColor=white)](https://discord.com/invite/UZv7JjxbwG)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)

An Agentic IDE that supervises parallel AI coding agents in isolated workspaces, with complete control and automatic feedback loops from CI failures, review comments, and merge conflicts.

<img src="docs/assets/readme/dashboard.png" alt="Agent Orchestrator dashboard showing parallel coding agent sessions" width="100%" />
</div>

---

## What is Agent Orchestrator?

Agent Orchestrator is a meta-harness agent IDE for running AI coding agents in parallel. It gives terminal-based agents like Claude Code, Codex, Cursor, Kimi Code, opencode, and others a shared workspace where their sessions, terminals, branches, pull requests, and feedback loops can be supervised from one place.

The agents still do the coding. AO provides the harness around them: isolated workspaces, live terminal access, session state, PR awareness, and automatic loops that send CI failures, review comments, and merge conflicts back to the right agent. Instead of manually coordinating a pile of agent terminals, AO turns parallel agent work into a managed workflow.

## Why Agent Orchestrator?

AI coding agents become much more useful when they can work in parallel, but parallel work gets messy quickly. Branches overlap, terminals get lost, CI failures need follow-up, review comments need replies, and merge conflicts have to reach the right worker.

Agent Orchestrator is built to keep that loop visible and manageable. It helps you:

- Start multiple agents from the same project without mixing their work
- Keep every session in a separate git worktree
- See which agents are working, waiting, finished, or blocked
- Route CI failures, review comments, and merge conflicts back to the right session
- Use different agent CLIs through one common supervisor

## How it works

At a high level, Agent Orchestrator follows a simple loop:

1. Add a project you want agents to work on.
2. Start one or more sessions from the desktop app or CLI.
3. AO creates an isolated git worktree for each session.
4. AO launches the selected coding agent in that session's terminal runtime.
5. The local daemon watches session state, terminal activity, pull requests, CI, and review feedback.
6. The desktop app and CLI show the current state and let you send follow-up instructions to the right session.

The result is a local control layer for agentic coding: agents still do the coding, while Agent Orchestrator keeps their workspaces, status, terminals, and feedback loops organized.

## Features

The desktop app is the main control surface: projects on the left, active sessions in the center, and the selected session's terminal, pull request state, review runs, and browser preview in the inspector.

<table>
  <tr>
    <td width="36%">
      <h3>Parallel agent sessions</h3>
      <p>Start multiple coding agents from the same project without mixing files, branches, terminals, or pull request state.</p>
    </td>
    <td width="64%">
      <img src="docs/assets/readme/dashboard.png" alt="Agent Orchestrator board with multiple parallel sessions" />
    </td>
  </tr>
  <tr>
    <td width="36%">
      <h3>Live terminal control</h3>
      <p>Open any session and attach to the worker terminal while keeping session summary, PR state, and follow-up actions in view.</p>
    </td>
    <td width="64%">
      <img src="docs/assets/readme/session-terminal.png" alt="Session terminal inside Agent Orchestrator" />
    </td>
  </tr>
  <tr>
    <td width="36%">
      <h3>Review feedback loop</h3>
      <p>Run reviewer agents, inspect review status, and route requested changes back to the right worker session.</p>
    </td>
    <td width="64%">
      <img src="docs/assets/readme/reviews-tab.png" alt="Reviews tab showing reviewer runs and actions" />
    </td>
  </tr>
  <tr>
    <td width="36%">
      <h3>In-app browser preview</h3>
      <p>Preview a session's local app beside the terminal so UI work, browser state, and agent output stay together.</p>
    </td>
    <td width="64%">
      <img src="docs/assets/readme/browser-preview.png" alt="Browser preview tab showing a local app preview" />
    </td>
  </tr>
</table>

## Supported Agents

AO ships adapters for 23 worker agent harnesses:

<p>
  <a href="https://aoagents.dev/docs/plugins/agents/claude-code"><img src="frontend/src/renderer/assets/agents/claude-code.svg" alt="" width="16" height="16" valign="middle" /> <code>claude-code</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents/codex"><img src="frontend/src/renderer/assets/agents/codex.svg" alt="" width="16" height="16" valign="middle" /> <code>codex</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents/aider"><img src="frontend/src/renderer/assets/agents/aider.png" alt="" width="16" height="16" valign="middle" /> <code>aider</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents/opencode"><img src="frontend/src/renderer/assets/agents/opencode.svg" alt="" width="16" height="16" valign="middle" /> <code>opencode</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/grok.png" alt="" width="16" height="16" valign="middle" /> <code>grok</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/droid.png" alt="" width="16" height="16" valign="middle" /> <code>droid</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/amp.svg" alt="" width="16" height="16" valign="middle" /> <code>amp</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/agy.png" alt="" width="16" height="16" valign="middle" /> <code>agy</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/crush.png" alt="" width="16" height="16" valign="middle" /> <code>crush</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents/cursor"><img src="frontend/src/renderer/assets/agents/cursor.svg" alt="" width="16" height="16" valign="middle" /> <code>cursor</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/qwen.png" alt="" width="16" height="16" valign="middle" /> <code>qwen</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/copilot.svg" alt="" width="16" height="16" valign="middle" /> <code>copilot</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/goose.svg" alt="" width="16" height="16" valign="middle" /> <code>goose</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/auggie.svg" alt="" width="16" height="16" valign="middle" /> <code>auggie</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/continue.png" alt="" width="16" height="16" valign="middle" /> <code>continue</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/devin.png" alt="" width="16" height="16" valign="middle" /> <code>devin</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/cline.svg" alt="" width="16" height="16" valign="middle" /> <code>cline</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/kimi.png" alt="" width="16" height="16" valign="middle" /> <code>kimi</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/kiro.png" alt="" width="16" height="16" valign="middle" /> <code>kiro</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/kilocode.svg" alt="" width="16" height="16" valign="middle" /> <code>kilocode</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/vibe.png" alt="" width="16" height="16" valign="middle" /> <code>vibe</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/pi.png" alt="" width="16" height="16" valign="middle" /> <code>pi</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents"><img src="frontend/src/renderer/assets/agents/autohand.svg" alt="" width="16" height="16" valign="middle" /> <code>autohand</code></a>
</p>

Reviewer agents are configured separately. The current reviewer harnesses are:

<p>
  <a href="https://aoagents.dev/docs/plugins/agents/claude-code"><img src="frontend/src/renderer/assets/agents/claude-code.svg" alt="" width="16" height="16" valign="middle" /> <code>claude-code</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents/codex"><img src="frontend/src/renderer/assets/agents/codex.svg" alt="" width="16" height="16" valign="middle" /> <code>codex</code></a> ·
  <a href="https://aoagents.dev/docs/plugins/agents/opencode"><img src="frontend/src/renderer/assets/agents/opencode.svg" alt="" width="16" height="16" valign="middle" /> <code>opencode</code></a>
</p>

**If it runs in a terminal, it runs on Agent Orchestrator.**

## Install

Download the latest desktop build for your platform:

| Platform              | Download                                                                                                                      |
| --------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| macOS (Apple silicon) | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-arm64.zip)   |
| macOS (Intel)         | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-darwin-x64.zip)     |
| Windows               | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-win32-x64.exe)      |
| Linux (AppImage)      | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.AppImage) |
| Linux (Debian/Ubuntu) | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.deb)      |
| Linux (Fedora/RHEL)   | [Download](https://github.com/Untrivial-ai/agent-orchestrator/releases/latest/download/agent-orchestrator-linux-x64.rpm)      |

After installing, open Agent Orchestrator and point it at the repository you want AO to manage. The desktop app runs the daemon for you, so no CLI is required. Installed desktop builds check for updates on launch and periodically while the app is running. See the [installation guide](https://aoagents.dev/docs/installation) for agent CLI setup and troubleshooting.

<details>
<summary>Install via npm (legacy CLI, no longer recommended)</summary>

npm still works but is no longer recommended. `0.10.0` is the final version published to npm, and the `@aoagents/ao` package is frozen and will not receive further updates. It stays available for existing users who have the `ao` CLI on their PATH; `ao start` fetches and opens the same desktop build linked above. For any new setup, prefer the desktop download.

```bash
npm install -g @aoagents/ao
ao start
```

</details>

## Witness AO's Journey on X

<table>
  <tr>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2026329204405723180">
        <img src="assets/tweet2.png" height="330" alt="Agent Orchestrator journey screenshot one" />
      </a>
    </td>
    <td width="50%" align="center">
      <a href="https://x.com/agent_wrapper/status/2025986105485733945">
        <img src="assets/tweet1.png" height="330" alt="Agent Orchestrator journey screenshot two" />
      </a>
    </td>
  </tr>
</table>

## Documentation

| Document                                                         | Start here when you need                                                                     |
| ---------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| [docs/architecture.md](docs/architecture.md)                     | Backend mental model, lifecycle, persistence, CDC, status derivation, and daemon boundaries. |
| [docs/backend-code-structure.md](docs/backend-code-structure.md) | Package ownership and where each backend concern belongs.                                    |
| [docs/cli/README.md](docs/cli/README.md)                         | CLI behavior and daemon route mapping.                                                       |
| [docs/development.md](docs/development.md)                       | Prerequisites, build steps, running tests, and troubleshooting for local development.        |
| [docs/STATUS.md](docs/STATUS.md)                                 | What currently ships on `main` and what remains in flight.                                   |
| [docs/stack.md](docs/stack.md)                                   | Library, runtime, and dependency decisions.                                                  |

## Telemetry

Agent Orchestrator's Electron renderer sends anonymous usage events to PostHog for reliability and product understanding. PostHog session recording is disabled by default; if a time-boxed investigation enables it, local paths and local URLs are redacted before transmission. Set `VITE_AO_POSTHOG_KEY` to an empty string before building to disable transmission. See [docs/telemetry.md](docs/telemetry.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
