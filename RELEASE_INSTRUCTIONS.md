# Release Instructions

In order to release a new version of the exporter the following steps will need to be completed.

### Preparation

- Ensure local `main` branch is synchronized: `git checkout main && git pull origin main`
- Run local validation suite: `make lint test test-integration`
- Verify e2e consistency: `make test-e2e`
- Confirm all PRs for the milestone are merged

### Versioning & Changelog

- Determine version number following Semantic Versioning (Major.Minor.Patch)
- Update `CHANGELOG.md`:
  - Rename `## Unreleased` section to `## vX.Y.Z — YYYY-MM-DD`
  - Update the milestone URL link
  - Ensure all significant changes (D-numbers) are captured
- Commit the update: `git add CHANGELOG.md && git commit -m "chore: release vX.Y.Z"`

### Execution (Tagging)

- Create an annotated git tag: `git tag -a vX.Y.Z -m "release: vX.Y.Z"`
- Push the release commit: `git push origin main`
- Push the tag to trigger automation: `git push origin vX.Y.Z`

### Verification (CI/CD)

- Monitor GitHub Actions `release` job progress
- Verify Docker images are published to `ghcr.io/grafana/network-topology-exporter`
- Confirm GitHub Release is created automatically
- Verify release assets: `topology-exporter-linux-amd64` and `topology-exporter-linux-arm64` are attached

### Post-Release

- Add a new `## Unreleased` placeholder to the top of `CHANGELOG.md`
- Close the completed milestone in GitHub Issues
- Notify the engineering/tester team of the new version
