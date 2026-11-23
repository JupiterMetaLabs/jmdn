# Period cap

Every height starts at period 1, and each failed round moves the next proposal at that height to a higher period. The period cap (`config.MaxPeriod`) is the highest period any node accepts. It is a validity rule: the voter, the buddy signer, the linkage gate and the synced-period binding all enforce it.

The cap is 2^20 at every height. It used to be 1024 until a fleet-agreed activation height (`consensus.period_cap_raise_from_height`); that setting is removed. Every stored block already has a period of at most 1024, so the higher cap accepts the existing chain. A config that still sets the key loads unchanged; the key is ignored.

In practice the time bound (`consensus.vote_period_*`, one period per second by default) is what limits how high a period gets: reaching 2^20 at one height takes about 12 days.

## Upgrade requirement: coordinated fleet-wide upgrade

**Deploy the release that raises the cap to every node together (stop all nodes, upgrade, start them). A rolling upgrade is not supported.**

An upgraded sequencer may propose a block with a period above 1024, for example after period-poison recovery at 1024. A node still on the previous release refuses that block, so a mixed fleet can stall the height or disagree about it.

## Stuck-height alert

A height that keeps failing rounds is reported by period, not by share of the cap:

- **Consensus: Height Stuck**, warning, when one height reaches period 20;
- **Consensus: Height Stuck**, error, when it reaches period 100.

Each level fires once per height. A height that jumps past both levels (for example after a restart, when buddies report a higher period they already signed) sends only the error.
