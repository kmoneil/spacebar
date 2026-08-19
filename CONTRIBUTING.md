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

## Fuzzing

`make fuzz` runs every target for `FUZZTIME` each, discovered by asking the
toolchain rather than from a list, so a target added to a package nobody
thought about is still swept. `.github/workflows/fuzz-nightly.yml` runs the same
thing for ten minutes a target every night, against a corpus carried over in a
cache: discovery scales with cumulative CPU time against a corpus that persists,
not with the length of any one run, so a twenty-second leg on every pull request
would mostly re-derive what last night already explored.

It is scheduled rather than required, because a find is usually about code the
last change never touched, and a nondeterministic red on somebody's unrelated
merge teaches people to press re-run. What runs on the pull request is the
deterministic half, inside `go test`: every seed, and every crasher ever
committed under `testdata/fuzz`.

**A find is committed twice.** The file Go wrote goes under the target's
`testdata/fuzz` directory, which is what replays it, and the same value goes in
as an `f.Add` seed in the same change, which is what makes the regression
legible to somebody reading the test rather than only to the toolchain.

**Write the property, not the examples.** A target that re-states what the code
does passes whatever either of them gets wrong. Two habits keep that from
happening. Plant the value you are looking for and fuzz what surrounds it,
rather than fuzzing the whole input and then writing a rule for how to
recognise a failure, because that rule is usually the guess under test. And
assert the property in the test's own terms rather than by calling the function
that implements it: `FuzzARecordOnlyAnswersForItsOwnSpace` called `belongs` in
its first draft, and deleting half of `belongs` did not fail it.

**Find the second implementation.** The strongest property is one checked
against something somebody else wrote, because a program compared with itself
passes when it is wrong the same way twice. That is worth a name: an **oracle**.

This tree has several and did not always use them.

| For | The oracle |
| --- | --- |
| redaction | `net/url`, parsed permissively rather than through the same `url.Values` the code uses |
| the configuration round trip | `encoding/json`, rather than `reflect.DeepEqual` over Go values |
| `chat.ParseWhen` | not `time.Parse`, which it calls. The inverse: `wireTime` formats, the test parses |
| everything a command does | **the live API** |

The last row is the point of the section below on verifying against a real
space. Live verification is an oracle comparison, and calling it that explains
why it catches things a full test run does not: the test suite is the same
program, and Google's API is not.

**Equivalence means agreement, not acceptance.** Compare only where both accept.
Where this tool refuses and the oracle does not, there is nothing to compare:
`ParseWhen` deliberately takes less than `time.Parse` does, refusing a bare date
because honouring one means choosing a timezone on somebody's behalf. Do not try
to enumerate what the other implementation is lenient about; that enumeration
grows one crasher at a time. This wording is taken from `kmoneil/dateparsa`,
which learned it the expensive way.

**A property the code cannot violate is not a property.** The failure has a
shape: the test asserts P of what a function returned, and the function already
refuses to return anything that is not P. It passes forever and says nothing.
`FuzzTheSpaceOfAMessageIsAlwaysASpaceName` was this until 2026-08-19, and it was
not caught by reading: deleting the check it existed to hold and fuzzing for 45
seconds over two million executions found nothing. The property was really about
two regexps, so it is stated between them now, where widening either one fails
it.

**Then break the code and check that the target notices.** Every target added
in the sweep that wrote this section was run against a deliberately broken copy
of what it guards before it was kept, and that found three that could not fail.
One was the circularity above. Another compared a value against itself: it
snapshotted a configuration *after* calling `SaveTo` rather than before, so a
`SaveTo` that dropped a field from the value it was handed still passed. A
property test that has never failed is a hypothesis.

Break it in the direction that should still pass, too. The gate in
`internal/lint/fuzz_test.go` derived a package from a corpus path two levels up
instead of three, so it reported every corpus as orphaned, including the ones
that were not. Only planting a corpus that should have been *accepted* found
it, and a gate that fails on everything is as useless as one that fails on
nothing.

**Say which mutation you checked.** When a test is added for a defect, its
comment names the change that made it fail. Not a machine gate: it is legible,
it is reviewable, and a test with no such line is one somebody can ask about.
The habit is worth the line because it keeps paying. Three tests that could not
fail at all were found this way in a single session, and each had been written
carefully by somebody who believed it worked:

- one asserted its property by calling the function that implements it;
- one tested both halves of a change and neither the wire between them, so
  reverting the fix to the line that shipped passed everything;
- one built the object under test by hand rather than with the constructor the
  production code uses, so changing that constructor changed nothing.

The last two are the same mistake wearing different clothes: **the test
exercised something the shipped code does not do.** That is the thing to look
for when a mutation does not fail.

**Expect the first finds to be yours.** Five of the six inputs this sweep
produced were the test being wrong and not the code: a comparison that could not
represent what the code correctly preserved, a sentinel that landed somewhere a
message is supposed to quote it, a harness that crashed on input the real server
would never pass through, a substring match on a value too short to be
distinctive. Each one is worth a paragraph where it lives, because the wrong
conclusion is available and cheap every time, and it is usually "change the code
so the test passes".

## Verify it against a real space before you open the pull request

A change to what a command *does* is run against a real Google Chat space before
it is proposed, not after it is merged. What was run and what came back goes in
the pull request. If some part cannot be run live, say which part and why, so
that a reader knows the difference between checked and assumed.

`_plans/live-testing.md` records which space has a Google Group in it, which
identifiers are real, and what the placeholders in the tree stand for. It is
gitignored, like everything under `_plans`.

**This is not belt and braces on top of the tests. It is the step that catches a
different class of thing.** On the day this section was written, four separate
defects were found this way and every one of them had passed a full test run:

- A URL fragment reached a verbose log, because the two redaction layers each
  looked only at the query.
- `auth login` said "Opened a browser to authorize" on a machine with no
  display, because `exec.Cmd.Start` answers whether a process spawned and the
  caller was asking whether a browser opened.
- A warning about an incomplete member list used `Renderer.Note`, which is
  suppressed under `--json`, so the fix carried the defect it was fixing for the
  reader that cannot check.
- A member list omitted a Google Group in `--json` as well as on the terminal,
  which the card that carded it had ruled out of scope.

They share a shape. **The code was right about the API and wrong about the
reader.** A test is not a reader: it does not have a terminal, or a pipe, or a
program parsing its stdout, and it never notices that the thing it asserted went
to a stream nobody is listening on.

## Undoing a change while you work

Two rules, both from losing something.

**Never `git checkout` or `git restore` a file to undo an edit** when that file
has uncommitted work in it. It reverts the whole file, not the edit. Copy the
file somewhere first and copy it back, which is what to do when temporarily
breaking code to check that a test notices. Two tests were destroyed this way in
one session, and a grep found them missing later by luck.

**Never hand somebody a command you have not run.** A one-line replacement for a
two-shell procedure was reasoned about carefully and never executed. It printed
no prompt, so a stray Enter satisfied its `read`, and the authorization code it
existed to protect was echoed to the screen instead. Running it once, even
against a deliberately wrong input, would have shown that.

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
- **A fuzz target that has gone missing, or one nobody wrote down.**
  `internal/lint/fuzz_test.go`. `requiredFuzzTargets` names every target and
  the property it holds, and is checked both ways: a listed target that is gone
  fails, and a target that exists and is not listed fails too, so the list
  cannot rot into a subset of what is there. The nightly sweep discovers what
  to run by asking `go test -list`, which is the right way to run it and the
  wrong way to guard it, because a workflow that greps for what exists cannot
  tell "never had one" from "somebody deleted it": it would go green with a
  shorter matrix and no message.

  A target with no `f.Add` fails too. The nightly sweep is scheduled and does
  not block a merge, deliberately; what blocks a merge is `go test`, which runs
  each target against its seed corpus and every crasher ever committed under
  `testdata/fuzz`, with no fuzzing budget at all. A target with no seeds
  executes nothing on the gate.

  And a corpus directory that names no target fails. An input Go wrote under
  `testdata/fuzz` is a bug that happened, replayed on every run by matching the
  directory name against a function name, so renaming the target orphans it: it
  stays committed, still reads as coverage in a diff, and is executed by
  nothing.
- **A commit message the hook cannot vouch for.** `.githooks/commit-msg`, held
  by `internal/lint/hook_test.go`, which runs the hook itself rather than
  reimplementing its rules in Go.

  It reads the subject the way git does: everything up to the first blank line,
  joined with spaces. It read `head -1` for four milestones, so a subject
  wrapped over two lines was measured at the width of its first line and passed
  a gate written to refuse it. An eighty-character subject landed that way, and
  nobody has to make a mistake for it to happen, because an editor that
  soft-wraps does it for them. The subject is now required to be one line with
  a blank one after it.
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
