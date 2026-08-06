# Spike findings

Results of [`run-spike.sh`](run-spike.sh) against a real installed Paseo.

**Status: EXECUTED — 12 pass, 0 fail, 1 skip.** Machine-readable summary in
[`fixtures/capability-report.json`](fixtures/capability-report.json); every claim below cites a
fixture in the same directory.

---

## Environment

| Field | Value |
|---|---|
| Paseo version | **0.2.5** (CLI and daemon) |
| Throwaway daemon `serverId` | `srv_klFjP9mdVeyy` (port 6799, own `PASEO_HOME`, own password) |
| Operator `serverId` before / after | `srv_Gjw4q7ibuUVT` / `srv_Gjw4q7ibuUVT` — **unchanged**, pid 1989 |
| Host OS | macOS (darwin/arm64) |
| Provider exercised | `claude` (`SPIKE_PROVIDER=claude`) |
| Date | 2026-08-05 |

---

## Verdicts

| Step | Prediction | Observed | Verdict |
|---|---|---|---|
| S0 | versions readable | 0.2.5 both | **pass** |
| S6a | `PASEO_PASSWORD` hashes in at startup, so non-interactive works | daemon up, password took | **pass** |
| S7a | unauthenticated rejected | rejected **with an auth error** | **pass** |
| S7b | `host:port` + `PASEO_PASSWORD` works | works | **pass** |
| S7c | `tcp://host:port?password=` works | works, with no `PASEO_PASSWORD` in env | **pass** |
| S7d | colonless host silently targets the local daemon | not exercised — would hit the operator | `skip` |
| S3 | `--title` round-trips as `ls .name` | `name = "ao:spike-44494:1"` exactly | **pass** |
| S4 | two-step create; exactly one worktree | `wks_1da4800fd5bd41e0`, `isolation=worktree` | **pass** |
| **S1f** | `capture` returns `{terminalId, lines[], totalLines}` with a monotonic cursor | **exactly that**; `totalLines=30` | **pass** |
| S1a | sentinel may be line-split at a delta boundary | survived intact — **but see caveat** | **pass\*** |
| S2a | `ls -ag --label` returns exactly one agent | exactly one | **pass** |
| S2b | malformed label returns **everything** | **confirmed fail-open** | **pass** |
| S2c | `inspect .Worktree` echoes `labels["paseo.worktree"]` | echoes it | **pass** |
| S10 | ~0.3–1.5 s per command | **0.88–0.96 s** | **pass** |

---

## The four results that decide the design

### 1. Rung 0 is real — and needs the wrapping mitigation

`terminal capture --start N --end M --json` returns exactly the predicted shape, and `totalLines`
is a genuine monotonic cursor. This is the only cursored surface in the CLI and it takes the model
out of the byte path entirely. **`PROTOCOL.md` rung 0 is confirmed as the primary transport.**

**But the PTY hard-wraps at exactly 80 columns.** A ~200-byte payload came back as three screen
lines:

```
line[ 9] len=80: AO_EVENT_SPIKE {"seq":1,"pad":"xxxxxxxx…
line[10] len=80: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx…
line[11] len=73: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx…
```

`lines[]` are **screen** lines, not logical lines. So a reader must reassemble, and the
"line is exactly COLS wide ⇒ it continues" heuristic is ambiguous (a payload could legitimately end
at 80). This is precisely why `PROTOCOL.md` specifies **76-char base64 chunks with a `k/n` header** —
the spike confirms that is required, not belt-and-braces.

Also note the sentinel appears **twice per emission** (shell echo of the command, then its output),
so the reader must dedupe. The emitter-minted `eventId` already covers this.

### 2. S1a passed, but only exercises the easy case

The sentinel survived byte-exact through Claude's streaming. That is **not** evidence the
delta-boundary hazard is imaginary.

The test emits **one short reply with no interleaved reasoning or tool-call rows** — exactly the
case where `mergeAssistantChunks` succeeds, because seq adjacency holds. The hazard needs an
intervening row to break adjacency, at which point `appendText` joins fragments with `\n` at an
arbitrary byte offset. A real agent emitting a checkpoint mid-work is the hard case, and it is
**untested**.

**Conclusion: rung 1 remains advisory.** S1a is recorded as "survives under favourable conditions",
not "rung 1 is safe". See follow-up F1 below.

### 3. The fail-open label bug is confirmed on a real daemon

`ls -a -g --json --label ao-malformed` (no `=`) returned the **full unfiltered agent list**, while
the well-formed query returned exactly one. A `pass` here is bad news about Paseo: it means a typo
turns "list my agents" into "list everything on the daemon".

`SECURITY.md` §10's rule — validate exactly one `=`, non-empty key **and** value, in Go before exec —
is confirmed as load-bearing rather than defensive.

### 4. Orphan adoption is verifiable

`inspect --json .Worktree` echoed back `labels["paseo.worktree"]`. Combined with S2a, `RECOVERY.md`
§2.2's adoption rule is executable: find by `ao.intent`, then **verify** via `.Worktree`, `.Cwd`,
`.Archived`, `.CreatedAt` before binding. Without S2c this collapses to "escalate to `attempt+1`"
in every ambiguous case.

Pinned to v0.2.5 — `paseo.worktree` is a dead key the server never writes, so a future version could
reclaim it. Re-check on every bump.

---

## Observer cadence, from measured latency

| Command | Seconds |
|---|---|
| `ls -a -g --json` | 0.962 |
| `workspace ls --json` | 0.878 |
| `provider ls --json` | 0.899 |

**~0.9 s per invocation**, because `paseo` is a shell shim that execs an Electron helper. A 5 s hot
tick therefore supports roughly **5 concurrent sessions per host** before polling saturates the
interval, and that is before `logs` — which re-renders the *entire* transcript every call.

Consequences for PR 9: prefer one `inspect` per session over any `ls`-then-`inspect` fan-out; use a
30 s cold tick for idle sessions; and treat per-host polling as a budget, not a constant.

---

## Deviations from the design docs

| Doc | Claim | Observed | Action |
|---|---|---|---|
| `PROTOCOL.md` §1 | sentinel likely shreds at a delta boundary | survived the easy case | **No change.** The claim is about interleaved messages, which S1a does not exercise. Caveat recorded above. |
| `PROTOCOL.md` §1 | rung 0 is PTY-wrapped, needs chunking | wraps at exactly 80 cols | **Confirmed.** Chunking is mandatory. |
| `ARCHITECTURE.md` §7 | `--host` accepts several forms | true, **but it is per-subcommand, not global** | Documented in `lib/common.sh`; adapter must not emit `paseo --host X <cmd>`. |

Nothing in the design docs was contradicted.

---

## Follow-up work

- **F1 (highest value)** — re-run S1a with a **hard** case: a long agent reply with interleaved tool
  calls and reasoning, at 200 B / 2 KB / 8 KB, ten times, measuring the shred rate. This is the only
  remaining unknown behind the transport decision.
- **F2** — run the whole spike with `SPIKE_PROVIDER=codex`. Codex emits one timeline row per
  `agent_message_delta` where Claude emits arbitrary byte slices, so its shred behaviour should
  differ and it is the likely workhorse provider.
- **F3** — S5 (permission flow) and S6 (daemon-restart recovery) are still unimplemented in the
  harness. S6 matters because it would confirm the transcript is non-durable, which `DECISIONS.md`
  D9 currently rests on source reading alone.
- **F4** — measure `logs` latency specifically, since it is the full-transcript re-render and is
  absent from the S10 table.

---

## Harness bugs found while running this

Recorded because three of them produced **false findings about Paseo**, and the pattern matters more
than the individual bugs — every one failed *open*, reporting success or a confident wrong answer:

1. **`--host` placed before the subcommand.** `--host` is per-subcommand; `paseo --host X ls` dies
   with `unknown option '--host'`. Every authenticated call failed, which read as an auth failure and
   sent me debugging Paseo's password handling — which was correct all along. Fixed by using
   `PASEO_HOST` env, which has no positional ambiguity.
2. **S7a asserted only a non-zero exit.** It "passed" while every command was dying on the flag error
   above. An expected-failure test must assert *why* it failed; it now fails explicitly on
   `unknown option`.
3. **`FIXTURES`/`VERDICTS` were `readonly` but never `export`ed**, so the inline Python could not see
   them. Reported *"S3 --title did NOT round-trip"* and *"S2a label lookup did not return exactly one
   agent"* — two damning, entirely fictional findings. Fixed by exporting, and by adding `jcheck`,
   which distinguishes pass / real-negative / **harness error** instead of conflating the last two.
4. **Cleanup's operator-daemon check wrote its snapshot inside the directory it had just deleted**,
   so it reported *"operator daemon not responding"* on all three runs while the daemon was provably
   healthy — a false alarm on the one check whose entire job is proving no harm was done.
