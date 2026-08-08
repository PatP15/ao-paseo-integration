# End-to-end verification

The first run of the whole integration against a real Paseo daemon: AO
registered a host, dispatched a work item, an agent did the work in a remote
worktree, and AO observed it.

Everything below was executed on 2026-08-07 against Paseo **0.2.5**.

---

## Result

| Step | Evidence |
|---|---|
| Register host | `e2e-worker` at `127.0.0.1:6803` |
| Probe online | `serverId=srv_j3qArrm6vwrk`, `v=0.2.5`, via `GET /api/status` |
| Bind project | `e2e` → `/tmp/e2e-worker/repo` |
| Dispatch | `Queued e2e-1 … command 6a86c618 pending` |
| Drain outbox | `start_agent state=acknowledged attempts=1` |
| Create worktree | `wks_f2d737ec762bb7be`, title `ao:e2e-1:1`, `isolation=worktree` |
| Launch agent | `8f66016a`, `claude/claude-opus-5` |
| **Agent did the work** | `HELLO.txt` (6 bytes) in the remote worktree |
| **AO observed it** | `agent_running` → `agent_idle`, durable `execution_events` |
| Session state | `activity=idle`, `terminated=0` |

That last row matters: AO recorded idleness and did **not** terminate the
session, because `idle` conflates finished, never-started, and awaiting-prompt.
Only AO's own completion evidence may end a work item.

---

## Reproducing it

```bash
# 1. Worker: a throwaway Paseo daemon in the posture SECURITY.md requires
mkdir -p /tmp/e2e-worker/{home,repo} && git init -q -b main /tmp/e2e-worker/repo
git -C /tmp/e2e-worker/repo commit -q --allow-empty -m base
env -u PASEO_AGENT_ID -u PASEO_WORKSPACE_ID -u PASEO_HOST \
  PASEO_HOME=/tmp/e2e-worker/home PASEO_PASSWORD=e2epw \
  paseo daemon start --home /tmp/e2e-worker/home --listen 127.0.0.1:6803 \
    --no-relay --no-mcp --no-web-ui &

# 2. AO: its own data dir and port, so nothing touches a real install
export AO_DATA_DIR=/tmp/e2e-ao/data AO_PORT=3099 AO_RUN_FILE=/tmp/e2e-ao/running.json
mkdir -p "$AO_DATA_DIR/secrets" && chmod 700 "$AO_DATA_DIR/secrets"
printf 'e2epw' > "$AO_DATA_DIR/secrets/e2e-worker-password"   # the --secret-ref target
chmod 600 "$AO_DATA_DIR/secrets/e2e-worker-password"
go build -o /tmp/ao ./backend/cmd/ao && /tmp/ao daemon &

# 3. Register, bind, dispatch
/tmp/ao remote register e2e-worker --name "e2e worker" --transport lan \
  --endpoint 127.0.0.1:6803 --secret-ref e2e-worker-password \
  --trust-zone hobby --max-sessions 2
/tmp/ao project add --path /tmp/e2e-worker/repo --name e2e --id e2e
/tmp/ao remote bind e2e e2e-worker --host-path /tmp/e2e-worker/repo --base-branch main
/tmp/ao remote dispatch --project e2e --work-item wi-e2e-1 --trust-zone hobby \
  --provider claude --harness claude-code --branch ao/e2e-1 \
  --prompt "Create a file called HELLO.txt containing the single word: hello. Then stop."
```

**A work item must already exist and be approved.** There is no surface to
create one — see Gap 1 below — so this run inserted the row with SQL.

---

## What this found that unit tests could not

Four defects, all of them living *between* components that had only ever met a
fake of each other. Each component's own tests passed honestly.

1. **The happy path was unreachable.** `Provision` computed `fresh := !found`,
   meaning "safe to create only when no binding row exists" — but the dispatch
   service commits the binding *before* enqueuing the command, precisely so a
   crash between the two replays rather than vanishes. A binding therefore
   always existed by the time `Provision` ran, so every dispatch refused with
   "workspace create outcome is unknown" and no workspace was ever created on
   any path.
2. **A nil dereference killed the daemon.** `guardHost` did
   `if *status.DesktopManaged` unconditionally. Only `paseo status --json`
   reports that field and it cannot target a remote host; `GET /api/status`,
   the surface that can, omits it. One nil field took down the whole process
   mid-delivery, including local sessions unrelated to remote execution.
3. **Nothing drained the outbox.** `dispatch.Worker` existed and was never
   constructed: dispatch returned 201 and the command sat `pending` at attempt
   0 forever.
4. **Nothing could create a project→host binding.** The router iterates
   bindings, so an unbound project has zero candidates however many hosts are
   online. Table, store method and router all existed; no API or CLI wrote a row.

Plus `ErrNoEligibleHost` mapping to HTTP 500, which reported an unbound project
as an internal server error and buried the actual cause.

### The guards all held

Worth recording alongside the defects, because it is the reason the failures
were recoverable rather than destructive. Every safety check fired correctly:

- refused to create a second worktree when a create outcome was ambiguous
- refused a binding whose attempt number did not match the command
- refused a host whose identity could not be confirmed
- `"host probe failed; sessions left untouched"` on every failed probe
- did not treat `idle` as completion

---

## Gaps still open

1. **No way to create a work item.** `UpsertWorkItem` exists in the store; no
   HTTP controller, no CLI. Dispatch requires an approved work item, so today
   dispatch is unusable without direct SQL. This is the planner's surface and it
   was deferred, but dispatch depends on it.
2. **No automatic escalation on an ambiguous create.** `RECOVERY.md` §2.2 says
   escalate to `attempt+1` with a fresh title. Nothing does: the command retries
   the same attempt and stalls permanently. Recovering the run above needed
   manual intervention.
3. ~~**Dispatch does not verify approval.**~~ **Not a gap — retracted.**
   `CreateExecutionDispatch` (`execution_dispatch_store.go:24-36`) checks, inside
   the dispatch transaction, that the work item is `approved`, that its lifecycle
   is dispatchable (`open`/`in_progress`), and that it belongs to the requesting
   project. An earlier version of this doc claimed the check was absent; that was
   wrong.
4. **The report channel is unexercised.** Observation here came entirely from
   `inspect` (rung 2, the floor). Neither terminal capture nor the sentinel was
   involved, and the rung-0 emitter does not exist yet.
5. **Desktop-managed detection is unavailable remotely.** `GET /api/status`
   omits it, so the guard can only fire on a local probe. `ServerID` comparison
   carries the guarantee instead.
