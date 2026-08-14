# spacebar

A focused terminal client and MCP server for Google Chat.

```
spacebar send eng-alerts "deploy done"
```

That is the whole point. One line, from a script or a terminal, with no
ceremony after setup. The same capabilities are available to an agent with no
human in the loop, through `--json` and through a built-in MCP server.

> Google Chat and Google are trademarks of Google LLC. `spacebar` is an
> independent third-party client. It is not affiliated with, sponsored by, or
> endorsed by Google, and Google does not support it.

## Status

**Milestone 1 of 6.** The repository builds, the gates are green, and three
commands work: `version`, `licenses`, and `completion`. Nothing talks to Google
Chat yet.

The plan, in six milestones:

| #   | Deliverable                                               | State    |
| --- | --------------------------------------------------------- | -------- |
| 1   | Skeleton, licensing, CI gates                             | **done** |
| 2   | Webhook transport: `send` with no OAuth at all            | next     |
| 3   | User OAuth: `auth`, `spaces`, `messages`                  |          |
| 4   | Full CLI: `tail`, `watch`, `react`, aliases, attachments  |          |
| 5   | MCP server                                                |          |
| 6   | Local index and search                                    |          |

Milestone 2 is the real proof point. It has to be genuinely useful to somebody
whose org blocks all third-party API access, because that population is large,
and a tool that is useless to them has a much smaller reach than it looks.

## Why it is shaped this way

**Three auth models, degrading gracefully.** Many users are in locked-down
Workspace orgs where the full API is simply unavailable. An incoming webhook is
write-only and appears as a bot, but it needs no OAuth and no admin approval;
user OAuth does everything but requires a client the org will allow. A command
that needs a capability the current profile does not have fails _before_ the
network call, with an exit code that says so and a message naming the fix.

What each one can do:

|                      | webhook                | user OAuth                     |
| -------------------- | ---------------------- | ------------------------------ |
| Send text            | yes                    | yes                            |
| Send cards           | yes                    | **no**, a user send is text-only |
| Read, list, search   | no                     | yes                            |
| Edit, delete, react  | no                     | yes                            |
| Threading            | yes, by `threadKey`    | yes                            |
| Upload an attachment | no                     | yes                            |
| Appears as           | a bot                  | you                            |

The cards row is not a typo. A card requires app authentication, and a webhook
*is* an app; a user-authenticated send is you talking, and that is text only.
It is the one thing the write-only transport can do and the full one cannot.

**Narrow scopes, and bring-your-own OAuth client as a first-class path.**
Narrower scopes materially improve the odds of admin approval, so
`auth login --send-only` is a real mode. An Internal-type client in the org's
own Cloud project avoids both third-party app access controls and the seven-day
refresh-token expiry that testing-mode apps impose.

**Chat markup is not CommonMark.** Bold is `*one asterisk*`. Passing standard
Markdown through renders literal asterisks and tildes, which is a real and
common bug in tools like this. `--md` translates; without it, text is sent
verbatim.

**Built for a script and an agent, not only for a person.** Structured output
on stdout, everything else on stderr, NDJSON for lists so it streams. Exit codes
distinguish "your authorization expired" from "that space does not exist". The
tool never blocks on a prompt when stdin is not a terminal, because a hung agent
is strictly worse than a failed one.

**A small dependency tree, on purpose.** One direct dependency today, and at
most five ever; each one has to argue for its place. The Chat API client is
hand-rolled because the generated one is 40k lines and drags in a transport
chain we would then not control.

## Install

Nothing is released yet. From source:

```sh
git clone https://github.com/kmoneil/spacebar
cd spacebar
make build     # -> bin/spacebar
```

A build from source has no OAuth client baked in, which is deliberate (see
[SECURITY.md](SECURITY.md)), and `spacebar auth setup` will walk through
creating one.

## Configure a webhook

This is the path that needs no OAuth, no admin approval, and no Cloud project.
In the Chat space, open Apps & integrations, then Webhooks, and copy the URL.
Then, in one command:

```sh
pbpaste | spacebar profile set-webhook alerts     # macOS
xclip -o | spacebar profile set-webhook alerts    # Linux
spacebar profile set-webhook alerts < webhook.txt # from a file
```

```
profile     alerts
transport   webhook
credential  keyring:spacebar/alerts/webhook
```

The URL arrives on stdin, or in `SPACEBAR_WEBHOOK_URL`, and never as an
argument. It carries `key` and `token` query parameters that are the entire
authentication for that space, so it is a credential rather than an address,
and an argument lands in the shell history and in the process list. It goes to
the OS keyring; the configuration file gets the reference above and never the
value.

The first profile becomes the default, so nothing after this needs `--profile`.

```sh
spacebar profile list           # what is configured, without reading a credential
spacebar profile list --json    # one object per line
spacebar profile rm alerts      # the profile and the credential behind it
```

On a machine with no keyring, which is every container and most CI runners, the
credential goes to a mode `0600` file beside the configuration instead, and
says so every time it is used.

`send` lands with the rest of Milestone 2.

## Development

```sh
make hooks     # install the pre-commit and commit-msg hooks
make tools     # install every pinned tool the gate needs
make ci        # everything CI runs, in the order CI runs it
make help      # the rest
```

`make ci` is the gate. It runs what the workflow runs, with the same pinned
tool versions, so a green run locally means a green run there.

Some things that are not obvious:

- **The golden files under `internal/cli/testdata/golden/` are a public
  contract.** They record what went to stdout, what went to stderr, and the
  exit code. `make golden` regenerates them; a diff is a change every caller
  sees.
- **`internal/lint` holds the repository to its own rules.** It ships no code:
  every test there asserts something a comment elsewhere claims, like go.mod
  and the workflows naming the same toolchain patch version.
- **`make licenses` regenerates `THIRD_PARTY_LICENSES` across all six release
  platforms**, because the dependency graph is not the same on all of them.
  `NOTICE` is held to the result by a test.

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

`spacebar licenses` prints the licence of every dependency from inside the
binary, with no checkout and no network, which is how the notices travel with a
distribution that is one executable and nothing else.
