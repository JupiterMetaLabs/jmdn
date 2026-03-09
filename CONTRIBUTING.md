# Contributing to JMDN

Thank you for your interest in contributing to the JMDT Decentralized Node.

## Getting Started

```bash
git clone https://github.com/JupiterMetaLabs/jmdn.git
cd jmdn
sudo ./Scripts/setup_dependencies.sh
make build
```

For detailed setup instructions, see [GETTING_STARTED.md](./GETTING_STARTED.md).

## Development Workflow

All development targets the `main` branch.

1. Create a feature branch: `git checkout -b feat/your-change`
2. Make your changes
3. Run quality checks locally before pushing:
   ```bash
   make fmt-check    # formatting
   make lint         # linting
   make build        # compilation
   ```
4. Push and open a pull request against `main`

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(component): add new capability
fix(component): resolve specific issue
chore(ci): update pipeline configuration
docs: update architecture documentation
style: formatting changes only
```

## Pull Requests

All PRs must pass CI checks before merging:

- **CI / Quality Gate** — build, lint, format check (`ci.yml`)
- **SonarQube / SonarQube Analysis** — static analysis and quality gate (`sonarqube.yml`)

PRs require at least one approving review from a maintainer. See `.golangci.yml` for active linter rules.

## Releases

Branching model and release process are documented in [RELEASING.md](./RELEASING.md).

## Security

If you discover a security vulnerability, **do not open a public issue**. Follow the process in [SECURITY.md](./SECURITY.md).

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](./LICENSE).
