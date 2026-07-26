# Releasing JMDN

## Versioning

JMDN uses [semantic versioning](https://semver.org/).

```
vMAJOR.MINOR.PATCH
```

- **MAJOR** — incompatible changes
- **MINOR** — new features or improvements
- **PATCH** — bug fixes or stability improvements

## Branch Model

```
main             active development, all features merge here
release/1.1.x    maintained release line, receives cherry-picked patches only
release/1.2.x    next release line, branched from main when ready
```

- `main` is always the source of truth for new development
- Release branches are never merged back into `main`
- Patches go to `main` first, then cherry-pick to release branches
- Tags are only created on release branches

## Creating a Minor Release

Example: releasing `v2.0.0`.

```bash
git checkout main
git pull --ff-only origin main

git checkout -b release/2.0.x
git push -u origin release/2.0.x

git tag -a v2.0.0 -m "Release v2.0.0"
git push origin v2.0.0
```

Create the GitHub Release from the tag with release notes.

## Creating a Patch Release

Example: releasing `v2.0.1`. The fix must already be merged into `main`.

```bash
git checkout release/2.0.x
git pull --ff-only origin release/2.0.x

git cherry-pick <commit-sha>

git tag -a v2.0.1 -m "Release v2.0.1"
git push origin release/2.0.x
git push origin v2.0.1
```

Create the GitHub Release from the tag.

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

- [ ] All CI checks pass on the release branch
- [ ] SonarQube quality gate passes
- [ ] SECURITY.md version table is updated
