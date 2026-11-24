# Releasing JMDN

## Versioning

JMDN uses [semantic versioning](https://semver.org/).

```
vMAJOR.MINOR.PATCH
```

- **MAJOR** — incompatible changes
- **MINOR** — new features or improvements
- **PATCH** — bug fixes or stability improvements

Pre-releases add a suffix: `vMAJOR.MINOR.PATCH-beta.N` while the release is still
taking features, then `vMAJOR.MINOR.PATCH-rc.N` once it is frozen.

## Branch Model

```
main             active development, all features merge here; betas are tagged here
release/3.0.x    next release line, branched from main at the first release candidate
release/2.0.x    previous release line, receives cherry-picked patches only
```

- `main` is always the source of truth for new development
- Release branches are never merged back into `main`
- Patches go to `main` first, then cherry-pick to release branches
- Betas (`-beta.N`) are tagged on `main`; release candidates and final releases are
  tagged on their release branch

## Creating a Beta

Example: releasing `v3.0.0-beta.2`. CI must be green on the `main` commit being tagged.

```bash
git checkout main
git pull --ff-only origin main

git tag -a v3.0.0-beta.2 -m "v3.0.0-beta.2"
git push origin v3.0.0-beta.2
```

No release branch is needed for a beta. A GitHub Release is optional; if one is created,
mark it as a pre-release.

## Cutting a Release Branch

Example: freezing `v3.0.0` at its first release candidate.

```bash
git checkout main
git pull --ff-only origin main

git checkout -b release/3.0.x
git push -u origin release/3.0.x

git tag -a v3.0.0-rc.1 -m "v3.0.0-rc.1"
git push origin v3.0.0-rc.1
```

Further release candidates and the final `v3.0.0` are tagged on `release/3.0.x`.

## Creating a Minor Release

Example: releasing `v2.0.0`.

```bash
git checkout main
git pull --ff-only origin main

git checkout -b release/2.0.x
git push -u origin release/2.0.x

git tag -a v2.0.0 -m "v2.0.0"
git push origin v2.0.0
```

Create the GitHub Release from the tag with release notes.

## Creating a Patch Release

Example: releasing `v2.0.1`. The fix must already be merged into `main`.

```bash
git checkout release/2.0.x
git pull --ff-only origin release/2.0.x

git cherry-pick <commit-sha>

git tag -a v2.0.1 -m "v2.0.1"
git push origin release/2.0.x
git push origin v2.0.1
```

Create the GitHub Release from the tag.

## Tags Are Immutable

Release tags (`v*`) cannot be moved, re-pushed or deleted once pushed: rulesets block it.
Check the commit and the message before pushing:

```bash
git for-each-ref refs/tags/v3.0.0-beta.2 --format='%(contents:subject) -> %(*objectname)'
```

A wrong tag is fixed by tagging the next version, not by replacing the tag.

## Container Images

Pushing a `v*` tag runs `.github/workflows/docker.yml`, which publishes
`ghcr.io/jupitermetalabs/jmdn:<tag>`. Only a final release (`vX.Y.Z`) moves `:latest`;
betas and release candidates never do.

## Release Artifacts

For releases that require verifiable archives (e.g., external audits or certification):

```bash
git archive v2.0.0 --format=zip -o jmdn-v2.0.0-source.zip
md5sum jmdn-v2.0.0-source.zip > jmdn-v2.0.0-source.zip.md5
sha256sum jmdn-v2.0.0-source.zip > jmdn-v2.0.0-source.zip.sha256
```

Upload the archive and checksum files to the GitHub Release. Not required for every release — only when external parties need to verify the source against a known hash.

## Verification

```bash
# Verify archive integrity
sha256sum -c jmdn-v2.0.0-source.zip.sha256

# Or verify exact code from tag
git checkout v2.0.0
```

## Checklist

Before tagging a release:

- [ ] All CI checks pass on the commit being tagged
- [ ] SonarQube quality gate passes
- [ ] SECURITY.md version table is updated
