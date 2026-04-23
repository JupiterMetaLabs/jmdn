# Security Audit Verification

## Certificate Details

| Field | Value |
|-------|-------|
| **Certificate No.** | TERA/CERT-IN/03/2026/CR/16 |
| **Report ID** | TTPL/Certin/PR/26/03/SCR/PN/S001/v2.0 |
| **Auditor** | Terasoft Technologies Pvt. Ltd. |
| **Empanelment** | STQC & CERT-IN Empaneled Test Laboratory |
| **Audit Type** | Source Code Review (VAPT) |
| **Language** | Go (Golang) |
| **Lines of Code** | 69,000 |
| **Audit Period** | 24 February 2026 – 06 March 2026 |
| **Issue Date** | 12 March 2026 |
| **Validity** | 6 months from issue date |
| **Auditor (Lead)** | Rashmi Jalindre — Technical Director |

## Audited Release

| Field | Value |
|-------|-------|
| **Release** | [v1.1.0](https://github.com/JupiterMetaLabs/jmdn/releases/tag/v1.1.0) |
| **Tag Commit** | `18c3a7553eb57f62c1548eab5dcb85a6ed09783f` |
| **Archive** | `jmdn-1.1.0.zip` |
| **MD5** | `508a6dc5f7061a27a0344c504aea9116` |
| **Branch** | `release/1.1.x` |

## How to Verify

An independent party can verify that this audit certificate corresponds to the
released source code by following these steps:

### Step 1 — Download the audited release archive

```bash
wget https://github.com/JupiterMetaLabs/jmdn/releases/download/v1.1.0/jmdn-1.1.0.zip
```

### Step 2 — Compute the MD5 checksum

```bash
md5sum jmdn-1.1.0.zip
```

Expected output:

```
508a6dc5f7061a27a0344c504aea9116  jmdn-1.1.0.zip
```

### Step 3 — Compare with certificate

Open [`TERA_CERT-IN_03_2026_CR_16_Certificate.pdf`](./TERA_CERT-IN_03_2026_CR_16_Certificate.pdf)
in this directory. The **Code MD5** field on the certificate must match the
checksum computed in Step 2.

### Step 4 — Verify the release tag

```bash
git clone https://github.com/JupiterMetaLabs/jmdn.git
cd jmdn
git verify-tag v1.1.0 2>/dev/null; git log -1 v1.1.0
```

Confirm the commit hash is `18c3a7553eb57f62c1548eab5dcb85a6ed09783f`.

### Step 5 — Cross-check the checksums file from the release

```bash
wget https://github.com/JupiterMetaLabs/jmdn/releases/download/v1.1.0/checksums-md5.txt
cat checksums-md5.txt
```

The MD5 for `jmdn-1.1.0.zip` in this file must match both the computed
checksum (Step 2) and the certificate (Step 3).

## Findings Summary

| Severity | Count | Status |
|----------|-------|--------|
| Critical | 0 | — |
| High | 0 | — |
| Medium | 1 (CWE-89: SQL Injection) | **CLOSED** |
| Low | 1 (CWE-400: Denial of Service) | **CLOSED** |

Both findings were remediated and verified closed by the auditor before the
certificate was issued. The v1.1.0 release includes all remediations.

## Full Report

The complete VAPT report (TTPL/Certin/PR/26/03/SCR/PN/S001/v2.0) is available
on request. Contact: security@jupitermeta.io

## Methodology

The audit was conducted in accordance with:

- OWASP Secure Coding Guidelines
- CERT Secure Coding Standards
- OWASP Top 10 (2017 and 2021)

Tools used: Burp Suite Professional, Invicti, SQLmap, Nmap, Nuclei, Dirbuster,
Nikto, Hydra.

Eight methodology areas were covered: input validation, authentication and
authorization, session management, data encryption and storage, error handling
and logging, third-party libraries and APIs, OWASP Top 10 protection, and
business logic security.
