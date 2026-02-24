# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in the JMDT Decentralized Network, please report it responsibly. **Do not open a public GitHub issue.**

**Email:** [security@jupitermeta.io](mailto:security@jupitermeta.io)

Please include:

- Description of the vulnerability
- Steps to reproduce
- Affected component (consensus, networking, transaction processing, etc.)
- Potential impact assessment
- Any suggested fix (optional)

We request a **90-day disclosure window** from initial report to allow adequate time for fix development and deployment. We will work to resolve critical issues as fast as possible.

## Response Timeline

| Action | Timeframe |
|--------|-----------|
| Acknowledgment of report | Within 48 hours |
| Initial triage and severity assessment | Within 5 business days |
| Status update to reporter | Within 10 business days |
| Fix development and testing | Based on severity |
| Public disclosure (coordinated) | After fix is deployed |

## Scope

### In Scope

- **AVC Consensus Engine** — Byzantine fault tolerance logic, vote aggregation, round management
- **Ed25519 / BLS Signature Verification** — Message authentication and proof validation
- **Transaction Processing** — Mempool handling, sequencer logic, block production
- **Peer-to-Peer Networking** — libp2p gossip protocol, message propagation, rate limiting
- **Smart Contract Execution** — Contract deployment and state transitions
- **Cryptographic Implementations** — Key generation, signing, hashing
- **API Endpoints** — gRPC and HTTP API input validation and authentication
- **ImmuDB Integration** — Database access controls and state integrity

### Out of Scope

- Third-party dependencies (report these to the upstream project directly)
- Frontend applications or block explorer UI
- Social engineering or phishing attacks
- Network-level denial of service (application-layer DoS is in scope)
- Vulnerabilities present only in development or test environments
- Issues already documented in known limitations

## Severity Classification

| Severity | Description | Examples |
|----------|-------------|----------|
| **Critical** | Immediate threat to funds or consensus integrity | Signature bypass, double-spend, consensus manipulation |
| **High** | Significant security impact requiring prompt action | Authentication bypass, unauthorized state modification |
| **Medium** | Security concern with limited exploitability | Information disclosure, rate limit bypass |
| **Low** | Minor issue with minimal security impact | Non-sensitive log exposure, missing security headers |

## Coordinated Disclosure

1. Reporter submits vulnerability to [security@jupitermeta.io](mailto:security@jupitermeta.io)
2. Our team acknowledges and triages within 48 hours
3. Fix is developed and tested internally
4. Reporter is notified and given opportunity to verify the fix
5. Fix is deployed to production
6. Public disclosure is coordinated with the reporter

## Bug Bounty

We are launching a formal bug bounty program. Until it is live, we commit to:

- **Credit** — All valid reports will be acknowledged in our security hall of fame (with reporter consent)
- **Good Faith** — We will not pursue legal action against researchers who follow responsible disclosure
- **Coordination** — We will work with reporters on disclosure timing

Details and reward tiers will be published at [jmdt.io](https://jmdt.io) once the program launches.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release (`main` branch) | ✅ Yes |
| Previous minor release | Security fixes only |
| Older versions | ❌ No |

## Contact

- **Security vulnerabilities:** [security@jupitermeta.io](mailto:security@jupitermeta.io)
- **General inquiries:** [jmdt@jupitermeta.io](mailto:jmdt@jupitermeta.io)