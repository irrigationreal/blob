# Releasing

Blob uses GitHub Actions to build and publish releases. Pushing a `v*` tag to `main` triggers the release workflow, which cross-compiles both binaries for four platforms and creates a GitHub Release with the artifacts.

## How a release works

The workflow (`.github/workflows/release.yml`) does the following on any `v*` tag push:

1. **Matrix build** — compiles `blobctl` and `blobd` for linux/darwin × amd64/arm64 (4 jobs, 8 binaries total, static `CGO_ENABLED=0`).
2. **Artifact collection** — uploads all 8 binaries.
3. **GitHub Release** — creates a release at the tag with auto-generated release notes and all binaries attached.

The install script (`scripts/install.sh`) fetches `blobctl-{os}-{arch}` from the latest GitHub Release, so a new release makes the updated CLI available to `curl | sh` installs immediately.

## Cutting a release

### 1. Bump the version

The version string is hardcoded in two files — both must match:

```
cmd/blobctl/main.go    var version = "X.Y.Z"
cmd/blobd/main.go      var version = "X.Y.Z"
```

### 2. Commit and merge to main

If you're working on a branch (recommended for non-trivial changes):

```bash
git add cmd/blobctl/main.go cmd/blobd/main.go
git commit -m "bump version to X.Y.Z"
git push origin <branch>
# Open PR, squash merge to main
```

For a simple version bump on main:

```bash
git add cmd/blobctl/main.go cmd/blobd/main.go
git commit -m "vX.Y.Z: <summary>"
git push origin main
```

### 3. Tag and push

```bash
git checkout main
git pull origin main
git tag vX.Y.Z
git push origin vX.Y.Z
```

The release workflow triggers automatically. Check progress at **Actions → release** on GitHub.

### 4. Verify

Once the workflow completes:

- The release appears at `https://github.com/irrigationreal/blob/releases/tag/vX.Y.Z`
- The install script picks up the new version: `curl -fsSL https://raw.githubusercontent.com/irrigationreal/blob/main/scripts/install.sh | sh`

## Binary naming convention

| Binary | Pattern |
|--------|---------|
| CLI    | `blobctl-{linux,darwin}-{amd64,arm64}` |
| Server | `blobd-{linux,darwin}-{amd64,arm64}` |

The install script detects the local OS and architecture automatically and fetches the matching `blobctl-*` binary.

## Version scheme

Versions are plain semver (`MAJOR.MINOR.PATCH`). Tags are prefixed with `v` (e.g. `v0.44.0`). There is no ldflags injection — the version is the literal string in the source files.
