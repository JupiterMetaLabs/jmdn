# JMDT Development Roadmap

> **Current status:** Phases 1–3 complete. Token live on CoinMarketCap, CoinGecko, and BitMart.
> Mainnet operational. ZK proofs, Decentralized Identity, and AVC consensus running in production.

---

## Phase 1 — Research & Foundation
**Q1–Q2 2024** ✅

Laying the cryptographic and architectural groundwork before a single line of node code ships.

- **ZK Proof Research** — deep-dive into ZK-SNARK/STARK primitives; select proof system suited for L2 throughput and on-chain verification cost
- **DID Architecture Design** — design W3C-compliant Decentralized Identity framework; define key schema, resolution protocol, and revocation model
- **AVC Consensus Design** — design Asynchronous Validation Consensus with BFT safety, BLS aggregate signatures, and VRF-based leader election
- **Immutable Ledger Selection** — evaluate and adopt ImmuDB as the append-only, tamper-proof backing store for the JMDN node
- **JMDN Node Prototype** — first internal build of the Go-based Layer 2 node with P2P messaging via libp2p and Yggdrasil
- **ZKP-Based Data Querying** — prototype DID-keyed queries where query proofs are generated and verified without exposing raw data

---

## Phase 2 — Testnet & Protocol Hardening
**Q3 2024–Q1 2025** ✅

Taking the protocol from internal prototype to a live public testnet under real network conditions.

- **JMDT Testnet Launch** — public testnet deployed on Ethereum Sepolia; open to external validators and developers
- **ZK-Rollup Aggregator** — batch transaction aggregator compresses L2 state transitions into a single ZK proof posted to Ethereum L1
- **JMDN FastSync Protocol** — eight-protocol P2P sync engine (Merkle bisection, header sync, data sync, PoTS, account sync) for new nodes joining the network
- **DID Registry on-chain** — live DID registration, resolution, and reference linking on the testnet; W3C DID Document standard compliant
- **Mempool & Transaction Pipeline** — production mempool with nonce management, duplicate detection, and fee prioritization
- **Beta Enterprise Testing** — closed beta with select enterprise partners validating DID-based data queries and ZK attestation flows
- **Security Audits (Round 1)** — independent audit of consensus logic, ZK circuit implementations, and P2P message handling

---

## Phase 3 — Mainnet Launch & Market Presence
**Q2–Q4 2025** ✅

Production launch, token distribution, and first exchange presence.

- **JMDT Mainnet Launch** — full production network live; JMDN nodes operating with AVC consensus and ZK proof submission to Ethereum L1
- **20M Verified User Capacity** — DID registry architected and tested to handle 20M+ verified identities
- **CoinMarketCap Listing** — JMDT token listed and tracked on CoinMarketCap ✅
- **CoinGecko Listing** — JMDT token listed and tracked on CoinGecko ✅
- **BitMart CEX Listing** — JMDT listed on BitMart exchange, 9 December 2025 ✅
- **JMDT Documentation Portal** — public developer docs at docs.jmdt.io covering node setup, RPC API, DID spec, and ZK proof integration
- **Block Explorer V1** — live explorer for blocks, transactions, accounts, and DID records

---

## Phase 4 — Ecosystem Foundation
**Q3–Q4 2026**

Turning a live chain into a developer platform with real liquidity and tooling.

- **Full EVM Compatibility** — deploy any Solidity/Vyper smart contract on JMDT L2 without modification; pass Ethereum's standard EVM test suite
- **JMDT ↔ Ethereum Mainnet Bridge** — two-way asset bridge with ZK-proof-based fraud proofs; no centralized relayer
- **Developer SDK** — Go, JavaScript, and Python SDKs; RPC-compatible with Hardhat, Foundry, and ethers.js
- **Block Explorer V2** — real-time explorer for blocks, transactions, accounts, DID records, ZK proofs, and smart contracts
- **CEX Expansion** — target 2–3 additional Tier-2 centralized exchange listings to improve liquidity and price discovery
- **Developer Grants Program** — first cohort of ecosystem grants for teams building on JMDT

---

## Phase 5 — DID & ZK Product Layer
**Q1–Q2 2027**

Turning the "Truth Layer" positioning into products enterprises and users actually ship with.

- **Verifiable Credential Marketplace** — issue, hold, and verify W3C-compliant credentials on-chain; revocation via ZK proofs without exposing PII
- **ZK-VM (Zero Knowledge Virtual Machine)** — execute private smart contracts where inputs stay hidden; on-device proving at the application layer
- **Verifiable Data Oracles** — on-chain attestation of real-world data (KYC outcomes, compliance checks, IoT readings) with ZK proof of source integrity
- **Enterprise KYC/AML Module** — prove a user passed compliance checks without exposing underlying identity data to the chain or counterparties
- **Mobile Wallet** — iOS and Android wallet with native DID management, credential storage, and ZK-proof generation on-device

---

## Phase 6 — Multi-Chain & Governance
**Q3–Q4 2027**

Expanding reach across the broader ecosystem and decentralising protocol control.

- **Cross-Chain Bridges** — LayerZero and/or IBC integration; JMDT assets and DID credentials portable to Arbitrum, Base, and Cosmos
- **Native DEX on JMDT** — AMM-based decentralised exchange; LP incentives funded from protocol fee revenue
- **L3 Application Chains** — framework for enterprises to deploy dedicated app-chains anchored to JMDT L2; shared security, isolated state
- **Ecosystem Fund** — grants, hackathons, and university research partnerships to grow the developer base

---

## Phase 7 — AI + Truth Layer Applications
**2028**

The long-term thesis: JMDT as the verification backbone for AI-generated content and autonomous agents.

- **AI Content Attestation** — on-chain provenance for AI-generated media; ZK proof that content was produced by a verified model or entity
- **Agent DID Framework** — autonomous AI agents receive DIDs; every action is signed and permanently auditable on-chain
- **Truth Score API** — public API for querying the authenticity score of any data anchored to JMDT; usable by media, finance, and legal sectors
- **JMDT Data Marketplace** — privacy-preserving marketplace where verified users monetise personal data with ZK consent proofs; enterprises pay in JMDT

---

## Full Timeline

| Phase | Period | Theme | Status |
|---|---|---|---|
| 1 | Q1–Q2 2024 | Research, ZK design, DID architecture, node prototype | ✅ Complete |
| 2 | Q3 2024–Q1 2025 | Testnet, ZK-rollup aggregator, FastSync, enterprise beta | ✅ Complete |
| 3 | Q2–Q4 2025 | Mainnet, 5K TPS, CMC/CoinGecko/BitMart listings | ✅ Complete |
| 4 | Q3–Q4 2026 | EVM, bridge, SDK, staking, CEX expansion | 🔨 In progress |
| 5 | Q1–Q2 2027 | Verifiable credentials, ZK-VM, Sign-In with JMDT | 🗓 Planned |
| 6 | Q3–Q4 2027 | DAO governance, multi-chain, DEX, L3 chains | 🗓 Planned |
| 7 | 2028 | AI attestation, agent DIDs, truth score API | 🗓 Planned |

---
