# ADR 0002: Remote execution backends are outbound-only

- **Status:** accepted
- **Date:** 2026-08-04
- **Context:** AO × Paseo integration, PR 0
- **Supersedes / relates to:** [ADR 0001 — LAN listener for mobile](0001-lan-listener-for-mobile.md)

## Context

AO is becoming a control plane for coding agents that run on **other machines** via stock Paseo
daemons. That requires a bidirectional-feeling relationship: AO must command remote hosts and must
learn what happened on them.

AO's binding rule (`AGENTS.md`, `docs/architecture.md` rule 5) is that the daemon binds
`127.0.0.1:3001` and adds no other network-facing bind. ADR 0001 carved out exactly one exception: an
opt-in, bearer-password LAN listener on `0.0.0.0:3011` for a paired phone.

So the question is whether remote execution justifies a second exception.

The concrete pressure comes from observation. Paseo's CLI gives AO no good event channel:
`paseo logs` has no `--json`, no event IDs, no cursor, and `--since` is a **dead flag** (declared at
`logs.ts:20`, referenced zero times). The obvious fix is to have worker hosts **POST** structured
events to an authenticated AO ingest endpoint. That is mechanically easy — reuse `authMiddleware`,
add a second `authState`, gate routes with an allowlist — and it would give sub-second latency.

## Decision

**AO makes only outbound connections. No new inbound bind.**

AO commands Paseo by executing the installed `paseo` binary with `--host`, and observes by polling
`paseo inspect --json`, `paseo terminal capture --json`, and `GET http://<host>/api/status`.

Rejected: worker→AO push (an inbound ingest endpoint).

Deferred, with a named trigger: a `@getpaseo/client` sidecar speaking Paseo's `/ws` protocol, which
has real cursors (`fetch_agent_timeline_request` with `seqStart`/`seqEnd`/`hasNewer`). Revisit only if
polling proves insufficient, and only in a separate AGPL repo.

## Rationale

**1. Polling has strictly better gap properties than the alternatives.**

The apparent weakness — no cursor — turns out to be the strength. `paseo logs` fetches the *entire*
timeline every call (`fetchAgentTimeline(…, {limit: 0})` → `selectAll`). Full replay plus an
emitter-minted event ID gives at-least-once delivery with idempotent apply and **zero cursor state**.
AO cannot miss an event that is still on the timeline, and an AO restart needs no recovery logic. A
push channel, by contrast, loses every event emitted while AO is down unless the worker also
implements a durable spool — which is a queue, on every worker, that we would then have to operate.

**2. Push inverts the trust direction into the wrong shape.**

An ingest endpoint authenticates *a server*, not a paired phone, and carries transcript content across
a possibly-untrusted network. ADR 0001's accepted posture was narrow on all three counts —
on-demand, one phone, home LAN — and AO's LAN password is bare hex SHA-256
(`mobilebridge.HashPassword`), not bcrypt. Reusing that machinery would silently widen ADR 0001 rather
than extend it deliberately.

**3. The credential has nowhere safe to live.**

A push token must reach the worker, and the only channels are the run brief (which
[`PROTOCOL.md`](../paseo-integration/PROTOCOL.md) forbids carrying secrets) or the agent's
environment. The environment is not safe: `paseo-env.ts:4-10` strips only five keys, so everything
else is readable by the agent with `printenv` and lands in its transcript — and therefore at its model
vendor. Worse, `get_agent_activity` is **cross-agent and unscoped**, so a co-resident agent can read
another agent's transcript. A bearer token that reaches an AO *write* endpoint cannot live in that
environment.

**4. Outbound-only keeps the blast radius on the side we control.**

If a worker is compromised, an outbound design means it can lie to AO. AO validates, rate-limits, and
treats every event as advisory ([`PROTOCOL.md`](../paseo-integration/PROTOCOL.md) §5.1). With push, a
compromised worker gets an authenticated write path into the control plane instead.

**5. The sidecar's only advantage does not exist.**

Real cursors are the sidecar's differentiator, and full replay already dominates them on gap
recovery. Paying an explicitly *"not a stable public SDK"* dependency, a second runtime to supervise,
and AGPL adjacency buys nothing we lack.

## Consequences

**Accepted:**

- Observation latency is a poll tick (5s hot, 30s cold) rather than sub-second. Acceptable: humans are
  the consumers of "needs input", and PR/CI facts already arrive on a 30s SCM poll.
- Polling costs a process spawn per call, and `paseo` is a shell shim around an Electron helper. Spike
  step S10 measures this and sets the cadence; it is a real budget, not a rounding error.
- AO cannot observe a host it cannot reach. This is correct behavior, not a gap — a failed probe is an
  observation, never proof of death (`RECOVERY.md` §3).

**Required by this decision:**

- `ExecutionRuntime.Alive` must return a **non-nil error** when a host is unreachable. Returning
  `(false, nil)` is read as death and, combined with `lifecycle.Manager`'s same-state no-op, would
  terminate live remote sessions (`ARCHITECTURE.md` §5.2).
- The event channel must survive being lossy. Hence the transport ladder and the rung-2 floor: ~13 of
  14 display statuses derive with **no** event channel at all
  ([`PROTOCOL.md`](../paseo-integration/PROTOCOL.md) §1).
- Cross-machine transport is Tailscale, not a LAN-bound daemon. Paseo's daemon is `http`-only
  (`bootstrap.ts:2`), so there is no TLS to turn on.

**Revisit if:** polling latency becomes a product problem, or Paseo ships a stable, cursored,
JSON-emitting CLI surface, or Paseo Hub leaves private beta (it already provides idempotent execution
IDs across reconnects — the thing this integration most wants and cannot have today).

## Alternatives considered

| Option | Verdict |
|---|---|
| Poll the CLI (chosen) | Outbound-only, no new bind, gap-free by full replay, zero worker install |
| Worker→AO HTTP push | **Rejected** — second bind violates a load-bearing rule; token has nowhere safe to live; loses events while AO is down |
| `@getpaseo/client` sidecar | **Deferred** — unstable AGPL dependency and a second runtime, for a gap-recovery advantage that does not exist |
| Git side-ref as event bus | **Contingency only** — durable and ordered, but 30s+ latency, needs push credentials, and races the agent's own branch pushes |
| Paseo Hub | **Watch** — private beta, self-host, Postgres, one hub per daemon, undocumented publicly |
