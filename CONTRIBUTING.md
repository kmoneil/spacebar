# Contributing

## Sign off your commits (DCO). There is no CLA.

Every commit needs a `Signed-off-by` line:

```sh
git commit -s -m "fix(chat): name the space in a 404 rather than the URL"
```

That line is the [Developer Certificate of Origin](https://developercertificate.org/):
you are stating you wrote the change, or have the right to submit it under the
project's licence.

**There is deliberately no CLA.** Apache-2.0 §5 already licenses inbound
contributions under the same terms as the project, so a CLA would add paperwork
and take rights without giving anything back. The DCO is an assertion about
provenance; a CLA is an assignment. Only the first one is needed here.

## `SPEC.md §N` in a comment

The source, the Makefile, and `SECURITY.md` cite a design spec by section. That
spec is a maintainer document and is deliberately not in this repository.

You are not missing anything you need. Every comment that cites it states its
reasoning in full. The citation is provenance, so that a rule which looks
arbitrary can be traced to where it was decided. If you hit one whose reasoning
is *not* stated locally, that is a bug in the comment; say so in your pull
request and it will be fixed rather than left pointing somewhere you cannot go.

## Getting set up

```sh
make hooks     # pre-commit and commit-msg, installed via core.hooksPath
make tools     # every pinned tool the gate needs
make ci        # everything CI runs, in the order it runs it
```

Never bypass the hooks with `--no-verify`. If a check is wrong, fix the check in
the same change, so the next person does not hit it either.

## Commit messages

Conventional Commits, checked by `.githooks/commit-msg`:

```
<type>(<scope>): <subject>

Body.

Signed-off-by: Your Name <you@example.com>
```

Types: `feat fix docs test refactor perf chore build ci`. The subject is
imperative, under 72 characters including the type and scope, and has no
trailing period.

**The body is plain text.** Git renders nothing and GitHub shows it
preformatted, so a heading is the characters `## ` and a table is a row of
pipes on every surface that will ever display it. Hyphen bullets, indented
blocks for code and log output, backticks around identifiers, bare URLs, and
trailers all survive; markdown headings, tables, fences, `**emphasis**`, and
`[links](url)` do not, and the hook refuses them.

Say what was wrong and why the fix is shaped the way it is. A reviewer has the
diff; somebody reading `git log` in two years does not.

## The rule this project actually runs on

**A new invariant gets a test.** If a bug got in, a gate should have caught it,
and extending that gate is part of the fix rather than a follow-up. A follow-up
is a thing that does not happen.

This is also why `internal/lint` exists. It ships no code: every test in it
asserts something a comment somewhere else claims. If you write "asserted by
internal/lint" in a comment, write the assertion too. A comment describing a
gate nobody wrote is worse than no comment, because it stops the next person
from looking.

## Things that will fail CI

- **An unformatted file.** `make fmt`.
- **A `.go` file with no Apache-2.0 header.** `make license-headers`.
- **A dependency whose licence is not on the allowlist** in SPEC.md §2.1,
  including transitively, and including one that only appears on Windows.
  `make license-check` scans all six release platforms.
- **A new dependency without regenerated notices.** `make licenses`, and update
  `NOTICE` in the same commit.
- **A changed golden file.** The files under `internal/cli/testdata/golden/`
  record what went to stdout, what went to stderr, and the exit code. They are
  a public contract with the scripts and agents that call this tool. `make
  golden` regenerates them; read every diff before committing it.
- **A function over the cognitive complexity ceiling.** Split it. Raising the
  ceiling is not the fix.
- **`go.mod` and a workflow disagreeing about the Go patch version.** They move
  together. See `internal/lint/goversion_test.go` for what breaks otherwise,
  which includes the licence gate failing with an error that mentions none of
  this.

## Adding a dependency

SPEC.md §3.1 names five permitted direct dependencies and the reason each earns
its place. All five are now here, so the next one is a sixth and needs an
argument in the pull request description. The answer is usually no: the standard
library is large, and a dependency is forever.

Count what it links rather than what it requires. `go list -deps` is the
measurement, and the MCP SDK is why the sentence is here: it is one line in
`go.mod` and six modules in the binary, which is more than a third of NOTICE.
That was worth knowing before the import was written rather than when the notice
gate failed.

Licences: `Apache-2.0`, `MIT`, `BSD-2-Clause`, `BSD-3-Clause`, `ISC`, `0BSD`,
`Unlicense`, `CC0-1.0`. Nothing else: no `GPL`/`LGPL`/`AGPL`, no `MPL`, no
`BSD-4-Clause` with its advertising clause, no `BUSL` or `SSPL`. When in doubt,
ask before adding it rather than after.

## Adding a command

Two rules that are easy to miss.

**Business logic does not go in `internal/cli`.** It and `internal/mcpsrv` are
both thin adapters over the same internal packages. A feature that works in the
CLI but not over MCP is a bug, and the only way that stays true is for neither
adapter to be where a decision gets made.

**A capability the profile does not have fails before the network call.** Exit
code 5, with a message naming both the profile and the fix. A write-only
webhook cannot read, and finding that out from a 403 is worse than finding it
out from us.

## Reporting a vulnerability

Not here. See [SECURITY.md](SECURITY.md).
