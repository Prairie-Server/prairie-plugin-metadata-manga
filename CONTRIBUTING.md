# Contributing to the Manga Metadata Plugin

The [Prairie contribution guide](https://github.com/Prairie-Server/prairie-server/blob/main/CONTRIBUTING.md)
covers project-wide coordination, focused changes, evidence, AI disclosure, and
pull request expectations. Those requirements apply here; this guide adds the
plugin-specific workflow.

## Before you start

Open an [issue](https://github.com/Prairie-Server/prairie-plugin-metadata-manga/issues)
before adding a source or changing matching, source fallback order, dump
storage, configuration, or the advertised capability. This repository owns
manga provider behavior; plugin contracts belong in
[`prairie-plugin-sdk`](https://github.com/Prairie-Server/prairie-plugin-sdk), while host
metadata orchestration belongs in
[`prairie-server`](https://github.com/Prairie-Server/prairie-server).

## Development setup

Use the Go version declared in `go.mod`. A local `go.work` may point at a sibling
SDK checkout while developing both repositories, but committed code and CI must
resolve the SDK version pinned in `go.mod` (a release tag or a pseudo-version
of the SDK's `main` branch) with `GOWORK=off`. Never commit local dump
data, cache paths, or a local filesystem `replace` directive.

## Validate your change

```sh
GOWORK=off go test ./...
GOWORK=off go vet ./...
GOWORK=off go build ./...
gofmt -l .
golangci-lint run ./...
GOWORK=off go test ./... -count=1 -covermode=atomic -coverprofile=coverage.out
./scripts/check-coverage.sh coverage.out
```

`gofmt -l .` should print nothing. If it reports unrelated pre-existing drift,
none of the Go files touched by your change may appear in the output; do not add
to the output, and report what remains. Add focused coverage for title
normalization, ambiguity handling, source fallback, dump refreshes, and failure
isolation when those behaviors change.
CI runs golangci-lint v2.14.0 and enforces a 95% statement coverage floor
(`scripts/check-coverage.sh`); the lint and coverage commands above reproduce
those checks locally.

The normal suite is hermetic. `TestLiveMangaBakaIntegration` is skipped unless
`MANGABAKA_LIVE=1`; run it separately for live API or banner-enrichment changes
and report its result separately. For banner-enrichment changes, confirm that
the verbose output reports a non-empty banner URL; the live test does not assert
that condition today.

```sh
MANGABAKA_LIVE=1 GOWORK=off go test ./provider -run TestLiveMangaBakaIntegration -v
```

## Open the pull request

Use a Conventional Commit title, explain any matching, storage, or upstream
service risk, and paste the actual validation results. Read the
[AI-assisted contribution policy](https://github.com/Prairie-Server/prairie-server/blob/main/docs/ai-contributions.md)
and include its disclosure block.
