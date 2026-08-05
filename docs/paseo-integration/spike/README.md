# Paseo compatibility spike

Proves — against a real installed Paseo — the capabilities the `ExecutionBackend` design depends on,
and captures JSON fixtures that become the adapter's **version contract**.

The fixtures matter more than usual: `paseo logs` has no `--json`, there is no OpenAPI document, and
the WebSocket protocol is private and explicitly unstable. Captured fixtures are the only
machine-readable record of how this CLI version actually behaves.

## Safety

Everything runs against a **throwaway daemon**, never the operator's:

- its own `PASEO_HOME` (`$TMPDIR/ao-paseo-spike/paseo-home`)
- its own port (`6799` by default; the script **refuses** to run on `6767`)
- its own password, generated per run and redacted from every fixture
- a disposable `git init` repo, removed on exit

Hard guards in `lib/common.sh`:

- **`--all` is banned** on `stop`, `delete`, and `archive`. On a shared daemon that flag has a
  whole-machine blast radius.
- **A colonless `--host` is rejected.** Paseo's `normalizeDaemonHost` returns `null` for any string
  without a colon and then **silently falls through to the local daemon** — which is the operator's.
- **Paseo's ambient env is scrubbed** from every invocation. A leaked `PASEO_AGENT_ID` cannot be
  overridden by `run --env` and silently reparents every new agent into the caller's workspace.
- `daemon stop` is addressed by `--home`, which cannot reach the operator's daemon.

The script fingerprints the operator's daemon at S0 and `cleanup.sh` re-checks its `serverId` at the
end, so "we didn't touch it" is verified rather than asserted.

## Usage

```bash
cd docs/paseo-integration/spike

./run-spike.sh                          # structural checks only, then clean up
SPIKE_PROVIDER=claude ./run-spike.sh    # also run the sentinel + label experiments
KEEP=1 ./run-spike.sh                   # leave the daemon up for inspection
./cleanup.sh                            # idempotent; safe any time
```

`SPIKE_PROVIDER` is opt-in because those steps launch a real agent and spend tokens. Without it the
sentinel and label experiments report `skip`, and the design falls back to rung 0 or rung 2 of the
transport ladder (see [`../PROTOCOL.md`](../PROTOCOL.md) §1).

Overridable: `PASEO_BIN`, `SPIKE_ROOT`, `SPIKE_PORT`, `SPIKE_PASSWORD`, `SPIKE_PROVIDER`, `KEEP`.

## What each step settles

| Step | Question | Why it matters |
|---|---|---|
| **S0** | versions; operator-daemon fingerprint | version pin, plus proof of non-interference |
| **S6a** | can a throwaway daemon come up with a non-interactive password? | makes every later step safe; enables restart testing |
| **S7a–c** | is auth enforced? do `host:port` and `tcp://…?password=` both work? | D16 transport; a *pass* on S7a means the password took |
| S7d | colonless `--host` | documented, deliberately **not** exercised — it would target the operator |
| **S3** | does `workspace create --title` round-trip as `ls .name`? | the only workspace-level recovery handle |
| **S4** | two-step create; exactly one worktree | `run --json` omits `workspaceId`, so AO must own it as an input |
| **S1f** | does `terminal capture --start/--end --json` give real line cursors? | **the decisive experiment** — the only cursored surface in the CLI, and it removes the LLM from the byte path |
| **S1a** | does a sentinel line survive `paseo logs` byte-exact? | rung 1 viability; a *fail* here is expected, not fatal |
| **S2a** | does `ls -ag --label` find the intended agent? | the only agent-level reconciliation handle |
| **S2b** | does a malformed label fail **open**? | expected `pass` confirms the bug the adapter must guard |
| **S2c** | does `inspect .Worktree` echo our `paseo.worktree` label? | label read-back; v0.2.5-only, re-check every version |
| **S9** | provider/mode discovery | modes are provider-specific; there is no global enum |
| **S10** | per-command latency | sets observer cadence — `paseo` is a shell shim around an Electron helper |

## Reading the results

`fixtures/capability-report.json` is the machine-readable summary
(`ao.paseo-capability-report.v1`): one verdict per step plus pass/fail/skip counts.

Two verdicts are **inverted** and easy to misread:

- **S2b `pass` is bad news about Paseo.** It confirms a malformed `--label` returns every agent on the
  daemon. The adapter must validate labels in Go before exec.
- **S1a `fail` is not a blocker.** It means the sentinel shredded at a provider delta boundary, which
  is the predicted behaviour. The design treats rung 1 as advisory and ships on rung 0 or 2.

Record conclusions in `FINDINGS.md`, then reconcile them against the claims in
[`../ARCHITECTURE.md`](../ARCHITECTURE.md) and [`../PROTOCOL.md`](../PROTOCOL.md).

## Related

`../spike/scan-invisible-unicode.py` is a standalone read-only scanner for invisible/bidi/TAG
codepoints used to smuggle instructions past human review. Re-run it on every Paseo version bump:

```bash
./scan-invisible-unicode.py /path/to/paseo-source /path/to/ao-source
```

Baseline results are in [`../VULNERABILITIES.md`](../VULNERABILITIES.md) §1.
