# Release

Steps to cut a new release of the exporter.

## Preparation

- Sync `main`: `git checkout main && git pull origin main`
- Run validation locally: `make lint test test-integration`
- Run e2e: `make test-e2e`
- Confirm every PR for the milestone is merged

## Versioning and changelog

- Pick a version following SemVer (`MAJOR.MINOR.PATCH`). Pre-releases use `-rc.N` (e.g. `v1.4.0-rc.1`).
- In `CHANGELOG.md`:
  - Rename the `## Unreleased` heading to `## vX.Y.Z — YYYY-MM-DD`
  - Update the milestone URL link
  - Confirm every significant change is captured
- Commit: `git add CHANGELOG.md && git commit -m "chore: release vX.Y.Z"`

## Tag and push

```sh
git tag -a vX.Y.Z -m "release: vX.Y.Z"
git push origin main      # commit first
git push origin vX.Y.Z    # tag triggers the release workflow
```

## Verify

The release job (`.github/workflows/ci.yml::release`) builds a multi-arch image, pushes it to GHCR, builds linux/amd64 and linux/arm64 binaries, and creates a GitHub Release with the binaries attached. Confirm each of:

- Workflow run succeeded — `gh run list --workflow=ci.yml --limit 3`
- Image is on GHCR — `ghcr.io/colinedwardwood/network-topology-exporter:X.Y.Z` (and `:latest` for non-prerelease tags)
- GitHub Release exists with both `topology-exporter-linux-amd64` and `topology-exporter-linux-arm64` attached

If the docker push step failed (it has, on `v1.3.0`), the binaries and the GitHub Release may still have shipped. Rerun just the docker push manually:

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  -t ghcr.io/colinedwardwood/network-topology-exporter:X.Y.Z \
  -t ghcr.io/colinedwardwood/network-topology-exporter:X.Y \
  -t ghcr.io/colinedwardwood/network-topology-exporter:latest \
  --push .
```

## Post-release

- Re-open `## Unreleased` at the top of `CHANGELOG.md`
- Close the milestone on GitHub
- Bump the pinned image tag in `deploy/test-harness/docker-compose.yml` if testers should move to this release
