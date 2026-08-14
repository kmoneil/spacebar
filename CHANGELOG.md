# Changelog

Written for somebody deciding whether to upgrade, which is why it is not a
commit list. A commit list is written for somebody who already has. The
release workflow reads the section for a tag out of this file and refuses to
publish a tag that has none.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/). Before 1.0,
the `--json` output shapes under `internal/cli/testdata/golden/` are treated as
the public contract: breaking one is a minor bump and is called out here by
name.

## [Unreleased]

### Added

- The repository skeleton: `cmd/spacebar`, `internal/meta`, `internal/output`,
  `internal/cli`. One direct dependency.
- `spacebar version`, with `--json`. The release gate refuses a tag whose binary
  does not report it.
- `spacebar licenses`, which prints every dependency's licence from inside the
  binary, with no checkout and no network, so the notices travel with a
  distribution that is one executable.
- `spacebar completion` for bash, zsh, fish, and powershell.
- The exit-code contract from SPEC.md §11.1, and the `--json` error envelope.
- Golden-file tests recording, for each invocation, the exit code and which
  stream every byte went to.
- `internal/lint`: tests that hold the repository to claims made in its own
  comments: go.mod and the workflows naming one Go patch version, tool
  versions declared once, `NOTICE` matching what is actually linked.
- The licence gate. `make license-check` runs `go-licenses` over all six
  release platforms against the SPEC.md §2.1 allowlist; `make licenses`
  regenerates `THIRD_PARTY_LICENSES` the same way. Cross-platform because
  `inconshreveable/mousetrap` is behind `//go:build windows` and a scan on one
  machine does not see it.

### Notes

Nothing talks to Google Chat yet. That starts with the webhook transport in
Milestone 2.
