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

## Arguing about performance

`make bench` runs every benchmark. Nothing in CI runs it, deliberately: a
benchmark on a shared runner measures the runner, and a gate that fails for that
reason is one somebody turns off.

So a performance claim in a review is settled by whoever makes it, with a
measurement rather than by reading the code. Before taking one, read the header
of `internal/store/bench_test.go`. It records what this project has learned
about measuring on a machine it does not own: what the noise floor was, what a
p-value did and did not say once another tenant's test suite appeared half way
through a run, and the one technique that made a seven per cent difference
legible anyway.

The answer a measurement gives is allowed to be "leave it alone", and two of
them already are.

## Things that will fail CI

- **An unformatted file.** `make fmt`.
- **A `.go` file with no Apache-2.0 header.** `make license-headers`.
- **A dependency whose licence is not on the allowlist** in SPEC.md §2.1,
  including transitively, and including one that only appears on Windows.
  `make license-check` scans all six release platforms.
- **A new dependency without regenerated notices, or a version bump without
  them.** `make licenses`, and update `NOTICE` in the same commit.
  `make licenses-current` regenerates and fails if anything moved, and it is in
  `make ci` so that a green local run predicts a green CI run. It exists because
  `THIRD_PARTY_LICENSES` was once found naming `x/sys v0.41.0` against a
  `go.mod` that said `v0.44.0`: CI would have caught it, and `make ci` would
  not have, while claiming to run everything CI runs.
- **A changed golden file.** The files under `internal/cli/testdata/golden/`
  record what went to stdout, what went to stderr, and the exit code. They are
  a public contract with the scripts and agents that call this tool. `make
  golden` regenerates them; read every diff before committing it.
- **An example in any hand-written document that is no longer true**, or a
  command nobody wrote about. `internal/cli/docs_test.go` resolves every
  `spacebar` command line in the README, `CONTRIBUTING.md`, `SECURITY.md` and
  all three files under `docs/` against the real command tree, flags included,
  and fails when a command exists that no document mentions. For
  `docs/AGENTS.md` and `docs/SKILL.md` it also decodes every JSON block into the
  shape it claims to be with unknown fields refused, and compares every block
  that quotes a golden against it. Those two are read by agents, which cannot
  tell a stale example from a current one, so an example there is closer to an
  interface definition than to prose.
- **A command that neither has an MCP tool nor says why not.**
  `internal/cli/parity_test.go`. `internal/cli` and `internal/mcpsrv` are thin
  adapters over the same internal packages, so a job one can do and the other
  cannot is a bug rather than a feature of either. Every command is recorded as
  served by a named tool, deliberately not served with the reason, or owed one
  with the name it will have, and a command in none of the three fails the
  build. It runs the other way too: a tool no command claims fails, and an owed
  entry whose tool has since been written fails until it moves.

  A second check covers what walking commands cannot reach. `send --file` and
  `send --card` are flags on a command that already has a tool, and each
  carries its own `transport.Require`, so the capabilities `internal/cli` gates
  on are read out of the source and compared with the ones `internal/mcpsrv`
  registers behind. A flag earns an entry when it changes which requests are
  made rather than what is in one, which is the rule `dryrun_test.go` already
  follows for the same flag.

  This exists because four commands were absent from the tool list for every
  profile, including one that could run all four, while `docs/SKILL.md` told a
  model that a missing tool means the profile cannot do the thing and to say so
  to the person. A gap is recoverable; a gap that instructs a model to make a
  false statement about somebody's access is not.
- **An environment variable no document names.** `config.EnvVars` is the list of
  what this process reads. `internal/lint/env_test.go` fails on a read of
  anything not in it, and on a listed entry nothing reads;
  `internal/cli/docs_test.go` fails on an entry the README's environment table
  does not carry, and on a document naming a variable of this project's that
  nothing reads. It exists because `SPACEBAR_PROFILE` worked from the first
  milestone and its whole public existence was the parenthesis at the end of
  `--profile`'s help string, which is not where somebody wiring up a CI job
  looks.
- **A function over the cognitive complexity ceiling.** Split it. Raising the
  ceiling is not the fix.
- **`go.mod` and a workflow disagreeing about the Go patch version.** They move
  together. See `internal/lint/goversion_test.go` for what breaks otherwise,
  which includes the licence gate failing with an error that mentions none of
  this.
- **A workflow step pinned to a tag rather than to a commit.** A tag is a
  mutable ref, so `actions/checkout@v7` is whatever that name points at on the
  morning CI runs, and whoever can move it chooses what executes with this
  repository's token. `internal/lint/actionpin_test.go` requires the
  forty-character commit and the trailing `# vX.Y.Z` comment saying which
  release it was, which is also what Dependabot reads and rewrites when it
  moves one.

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
