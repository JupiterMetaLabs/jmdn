# Appendix: Tools and Methodology

## Methodology
The audit followed a structured Approach:

1.  **Phase A: Threat Modelling & Architecture Review**
    -   Analysis of `ARCHITECTURE.md` and `ENGINEERING_AUDIT.md`.
    -   Attack Surface Mapping.
    -   Trust Boundary Definition.

2.  **Phase B: Manual Code Audit**
    -   Line-by-line review of critical modules (`Security`, `Sequencer`, `Pubsub`, `CLI`).
    -   Cross-referencing implementation against the `01_threat_model_and_trust_assumptions.md`.
    -   Verification of cryptographic implementations (BLS, nonces).

## Tools Used
-   **Manual Analysis**: Expert review of Go source code.
-   **Static Analysis**: `grep` for pattern matching (secrets, "TODO", networking calls).
-   **Architecture Mapping**: Visualizing module interactions.

## Scope
-   **In Scope**: `main.go`, `CLI/`, `Sequencer/`, `Pubsub/`, `Security/`, `messaging/`, `config/`.
-   **Out of Scope**: `AVC/` (Buddy Nodes deep dive), `SmartContract/`, frontend components.
