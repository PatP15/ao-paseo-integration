# Spike findings

Results of running [`run-spike.sh`](run-spike.sh) against a real installed Paseo.

**Status: NOT YET EXECUTED.** Everything below is a template with the source-derived prediction for
each step. Fill in the observed column after a run, and reconcile any disagreement against
[`../ARCHITECTURE.md`](../ARCHITECTURE.md) and [`../PROTOCOL.md`](../PROTOCOL.md) before writing
production code.

A prediction that turns out **wrong** is the most valuable output here — it means a design claim
rests on a misread of the source.

---

## Environment

| Field | Value |
|---|---|
| Paseo version | *(from `paseo --version`; expected `0.2.5`)* |
| `@getpaseo/cli` version | *(from `paseo status --json` → `cliVersion`)* |
| Daemon version | *(→ `daemonVersion`; must match `cliVersion`)* |
| Throwaway `serverId` | |
| Operator `serverId` before / after | *(must be identical — see `cleanup.sh` step 4)* |
| Host OS | |
| Providers available | *(from `provider ls --json`)* |
| Date | |
| `SPIKE_PROVIDER` set? | *(if no, S1a/S2a–c are `skip`)* |

---

## Verdicts

| Step | Prediction (from source) | Observed | Verdict |
|---|---|---|---|
| **S0** | versions readable; operator daemon reports `desktopManaged: true` | | |
| **S6a** | daemon starts; `PASEO_PASSWORD` is hashed into config at startup, so non-interactive works | | |
| **S7a** | unauthenticated request **rejected** | | |
| **S7b** | `host:port` + `PASEO_PASSWORD` works | | |
| **S7c** | `tcp://host:port?password=` works; URI password takes precedence over env | | |
| S7d | colonless host silently targets the local daemon — **not exercised** | n/a | `skip` |
| **S3** | `--title` round-trips as `ls .name`; `resolveWorkspaceName` is *"the title always wins"* | | |
| **S4** | exactly one worktree; path is `<worktreesRoot>/<hash(cwd)>/<slug>`, so `--worktree-slug` alone does not determine it | | |
| **S1f** | `capture --start/--end --json` returns `{terminalId, lines[], totalLines}`; `totalLines` monotonic. **PTY line-wraps at `COLS`** | | |
| **S1a** | sentinel found, but **may be line-split** at a provider delta boundary — `appendText` joins fragments with `\n` at an arbitrary byte offset | | |
| **S2a** | `ls -ag --label` returns exactly one agent; filtering is server-side *and* client-side | | |
| **S2b** | malformed label (no `=`) returns **everything** — `if (eqIndex !== -1)` with no `else` | | |
| **S2c** | `inspect .Worktree` echoes `labels["paseo.worktree"]`, a dead key in v0.2.5 | | |
| **S9** | provider modes are per-provider strings; Claude and Codex expose different sets | | |
| **S10** | each command costs a process spawn through a shell shim → an Electron helper; expect ~0.3–1.5s | | |

---

## Questions this spike must answer for the design

### 1. Which transport rung ships? *(decides PR 10)*

- **S1f passes** → rung 0 (`terminal capture`) is primary. Deterministic writer, cursored reader, no
  model in the byte path.
- **S1f fails, S1a intact 10/10** → rung 1, advisory only, pointer payloads.
- **Both fail** → rung 2. ~13 of 14 display statuses still derive from `inspect` + AO's existing
  `observe/scm`. **This is not a blocker.**

Record the observed sentinel loss rate at 200 B / 2 KB / 8 KB. Any loss at all disqualifies rung 1 as
primary, because the failure is silent and indistinguishable from "hasn't emitted yet."

### 2. Is label reconciliation trustworthy enough to adopt an orphan? *(decides PR 5)*

Needs **S2a pass and S2c pass**. Without S2c there is no way to verify an adoption candidate, and
`RECOVERY.md` §2.2's rule collapses to "escalate to `attempt+1`" in every ambiguous case.

Also measure **visibility latency**: how long after `run` returns does `ls --label` find the agent?
The provider session is spawned *before* `registerSession` makes it visible, so this window is the R1
TOCTOU. Record the observed width.

### 3. What polling cadence can AO afford? *(decides PR 9)*

From S10. If a single `inspect` costs ~1s, a 5s tick across N sessions on one host serializes badly.
Consider per-host batching or a longer cold-path interval.

### 4. Does the transcript survive a daemon restart?

Predicted **no** — `durableTimelineStore` is never constructed in v0.2.5. Confirm by restarting the
throwaway daemon (safe; it is not the operator's) and re-reading `logs`. If confirmed, `DECISIONS.md`
D9 is forced rather than chosen, and nothing in `RECOVERY.md` may depend on Paseo's transcript.

---

## Deviations from the design docs

*Record anything the spike contradicts, with the fixture that proves it. Then fix the doc — the
fixture wins.*

| Doc | Claim | Observed | Action |
|---|---|---|---|
| | | | |

---

## Not covered by this spike

Deliberate gaps, carried forward as risks:

- **A real second host.** `--host 127.0.0.1:<port>` proves flag parsing, the `tcp://` auth path, and
  the two-step create. It does **not** prove Tailscale, the relay offer-URL flow, real cross-machine
  latency, or Windows path handling. The Windows/Unity box is half the target topology and is
  entirely unexercised.
- **Concurrency.** No test of two AO instances racing the same work item, or N agents on one host.
- **Long-running behaviour.** No multi-hour run, so compaction, timeline growth, and scrollback
  eviction are unmeasured — all three affect rungs 0 and 1.
- **Permission flow (S5).** Forcing a real permission request needs a provider and a tool call the
  agent will actually attempt; deferred until `SPIKE_PROVIDER` runs are routine.
- **Failure injection.** No test of a mid-`run` kill, which is the R3 zombie path.
