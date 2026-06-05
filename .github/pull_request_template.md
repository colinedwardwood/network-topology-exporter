<!--
Thanks for contributing! Keep the description tight — reviewers read this first.
See CONTRIBUTING.md for the clean-room development rules and conventions.
-->

## What this PR does

<!-- One or two sentences. What changes, and why. -->

## Which issue(s) this closes

<!-- e.g. "Closes #123". Use one "Closes #N" per issue — GitHub only links the
     issue immediately after each keyword. -->
Closes #

## Special notes for reviewers

<!-- Anything non-obvious: a design trade-off, a follow-up left open, a config
     migration, a breaking change. Delete if none. -->

## Checklist

- [ ] Tests added/updated and `go test ./... -race` passes
- [ ] `golangci-lint run ./...` is clean (gofmt + gosec — not covered by `go vet`)
- [ ] `govulncheck ./...` is clean
- [ ] Docs updated (`README.md`, `docs/`, `config/example.yaml` if config changed)
- [ ] `CHANGELOG.md` updated under the right section
- [ ] Any new metric/config key/flag is documented and uses a stable, low-cardinality name
- [ ] Breaking changes to config or the metrics contract are called out above and in `CHANGELOG.md`
