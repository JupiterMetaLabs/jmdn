# Changelog

All notable changes to JMDN are documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/),
adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.1.1] - 2026-04-XX

### Added
- CERT-IN security audit certificate (TERA/CERT-IN/03/2026/CR/16)
  with verification instructions ([VERIFICATION.md](./audits/2026-03-terasoft-certin-vapt/VERIFICATION.md))
- Trusted clients configuration for rate-limit bypass (#20)
- Security audit badges and section in README

### Fixed
- Initialize expectedChainID at startup, independent of BlockGen —
  fixes crash on non-sequencer nodes (#21)
- Alerts viper bindings and centralized config access (#17)
- Replace hardcoded web3_clientVersion with build-flag driven version (#26)

### Changed
- Lazy load alerts service and isolate configuration (#16)
- CI workflows now trigger on release branches (#15)

### Removed
- Internal pre-release analysis files containing sensitive findings
- Temporary rollout observability logs (#18)

## [1.1.0] - 2026-03-09

### Added
- Initial public release of JMDN
- Open source release baseline documentation
- SonarQube pipeline configuration
- Rate limiting and security hardening (#3)
- File/directory permission tightening and ReadHeaderTimeout (#4)
- Parameterization of systemd SERVICE_USER (#11)

### Fixed
- SQL injection findings in sqlops using pre-built statements (#12)
- Dynamic SQL execution warnings resolved (#9)
- Config viper override merging (#7)
- Staticcheck formatting and redundant types (#6)


## [1.0.0] - 2026-02-24

### Added
- Initial open source release

[Unreleased]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.1.1...HEAD
[1.1.1]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/JupiterMetaLabs/jmdn/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/JupiterMetaLabs/jmdn/releases/tag/v1.0.0