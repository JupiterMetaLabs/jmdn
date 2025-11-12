# Context Lifecycle & Shutdown Docs

Use these guides when implementing the shutdown and context hardening work.

| Document | Purpose | When to read |
| --- | --- | --- |
| `CONTEXT_IMPACT.md` | Baseline assessment of current context usage and production risks. | Before kickoff, and after each phase to confirm risks are closed. |
| `CONTEXT_PHASE_AB.md` | Detailed blueprint for Phase A (bootstrap context propagation) and Phase B (DB context hygiene). | While implementing Phase 1/2 of the shutdown plan. |
| `SHUTDOWN_AUDIT.md` | Service-by-service list of missing shutdown registrations. | Use to scope each refactor PR; update as soon as items are fixed. |
| `SHUTDOWN_REVIEW.md` | Status table (✅/🟡/❌) summarising shutdown readiness. | Quick health check after each merge. |
| `SHUTDOWN_IMPL_PLAN.md` | End-to-end delivery roadmap, including lifecycle coordinator, server refactors, worker hygiene, DB context, and testing. | Primary execution plan for the team. |

### Execution Order
1. **Phase 1 – Lifecycle Foundations**: implement `signal.NotifyContext`, add the lifecycle coordinator, register pools/host/manager.
2. **Phase 2 – Bootstrap Context Propagation**: follow `CONTEXT_PHASE_AB.md` to thread contexts through `main`, CLI, node init, and PubSub.
3. **Phase 3 – Server Handle Refactors**: refactor HTTP/gRPC servers to return handles and register them with lifecycle adapters.
4. **Phase 4 – Background Worker Hygiene**: add context-aware loops, stop tickers, register services (block poller, FastSync, discovery, pubsub).
5. **Phase 5 – Data Access Context**: convert `DB_OPs` helpers to accept contexts and update call sites.
6. **Phase 6 – Testing & CI**: add goleak/port reuse tests and CI guards (`os.Exit`, stray `context.Background()`).

Update the audit/review tables and refreshed metrics in `CONTEXT_IMPACT.md` as each phase lands so future teams have a single source of truth.

