<!--
The subject of the squashed commit comes from this PR's title, so write it as a
Conventional Commit: `<type>(<scope>): <subject>`, imperative, under 72
characters, no trailing period.

The body of the squashed commit comes from the commit messages on the branch,
not from this description, so write those with the same care: say what was
wrong and why the fix is shaped the way it is. This page is for a reviewer who
has the diff in front of them. The commit message is for somebody reading
`git log` in two years who does not.
-->

## What was wrong

## Why the fix is shaped this way

## Checks

<!--
Delete the rows that do not apply. The ones left should be true, and CI checks
most of them; this list is for the two or three it cannot.
-->

- [ ] `make ci` passes locally, or CI is green.
- [ ] **Output contract.** If a golden under `internal/cli/testdata/golden/`
      changed, every diff was read, and the change is deliberate. An agent
      parsing `--json` is a consumer we never hear from until we break it, so a
      breaking change is marked `!` with a `BREAKING CHANGE:` footer naming what
      moved.
- [ ] **Exit codes.** A new failure path returns one of the codes in SPEC.md
      §11.1 rather than a bare `1`, and the code means what the table says.
- [ ] **Dependencies.** If `go.mod` changed: the new module is one of the five
      SPEC.md §3.1 permits, or the description argues for the sixth.
      `make licenses` was run and `NOTICE` updated in the same commit.
- [ ] **Capability gating.** If this added a command, it fails with exit 5
      before making a network call on a profile whose transport cannot do it,
      and it works identically over MCP. A feature that works in the CLI but
      not over MCP is a bug (SPEC.md §4).
- [ ] **Secrets.** Nothing new can put a token, a refresh token, an
      `Authorization` header, or a webhook URL into output, including at
      `--verbose` and in `--dry-run`. A webhook URL is a bearer credential that
      does not look like one.
- [ ] `SECURITY.md` updated, if this changed what is treated as hostile, added
      a way for a credential or a request to leave the process, or moved a
      `(Mn)` marker to a named test.
- [ ] `make fuzz` run, if this touched anything that quotes, escapes, or parses,
      and any crasher Go wrote to `testdata/fuzz` is committed with the fix.
- [ ] **A new invariant has a test.** If this fixes a bug a gate should have
      caught, the gate is extended in the same change. That is the rule this
      project actually runs on. A claim that says "asserted by internal/lint"
      has a test in `internal/lint` that asserts it.
