# spacebar

A focused terminal client and MCP server for Google Chat.

```
spacebar send "deploy done"
```

That is the whole point. One line, from a script or a terminal, with no
ceremony after setup: a webhook profile knows which space it posts to, so
there is nothing else to say. The same capabilities are available to an agent with no
human in the loop, through `--json` and through a built-in MCP server.

> Google Chat and Google are trademarks of Google LLC. `spacebar` is an
> independent third-party client. It is not affiliated with, sponsored by, or
> endorsed by Google, and Google does not support it.

## Status

**Milestones 1 to 3 of 6 are done.** `spacebar send` works over an incoming
webhook, with no OAuth, no administrator approval, and no Cloud project: give a
profile a webhook URL and send. On a profile authorized as you, `spaces list`,
`spaces get`, `spaces members`, `messages list` and `messages get` work as
well. `auth`, `version`, `licenses` and `completion` work. Still missing:
`tail`, `watch`, editing, reacting, attachments, aliases, and the MCP server.

One thing worth knowing before you rely on it. Every behaviour described below
is covered by tests, including against a server that answers the way the Chat
API does. Beyond that, the webhook path has been run end to end against a real
Google Chat space, and the Chat markup rules were measured there rather than
read from documentation. Authorizing with your own OAuth client has been run for
real too, through a browser, against a client in a Workspace organization.

Reading has been run against the real API too. `spaces list`, `spaces get`,
`spaces members`, `messages list` and `messages get` have each fetched from
Google rather than from a test server, which settled three things that had been
decoded from the API reference and never watched work: the response shapes,
that `orderBy` accepts `createTime DESC`, and that `pageSize` is honoured
exactly. The live run is also what found the two bugs in the set, which is
worth saying plainly because it is the argument for doing it. `spaces members`
asked for a scope no authorization requested, so it failed on every profile the
tool could create, and no test in the tree could have seen it. And every `auth`
command assumed a profile that could hold a token, so on a webhook one of them
stored an OAuth client against it and another reported a successful logout of
something that had never been logged in.

The plan, in six milestones:

| #   | Deliverable                                               | State    |
| --- | --------------------------------------------------------- | -------- |
| 1   | Skeleton, licensing, CI gates                             | **done** |
| 2   | Webhook transport: `send` with no OAuth at all            | **done** |
| 3   | User OAuth: `auth`, `spaces`, `messages`                  | **done** |
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

Add `--verify` to prove it works, which posts a message to the space:

```sh
pbpaste | spacebar profile set-webhook alerts --verify
pbpaste | spacebar profile set-webhook alerts --verify --verify-text 'setting up spacebar'
```

```
profile     alerts
transport   webhook
credential  keyring:spacebar/alerts/webhook
verified    spaces/AAAATestSpace
```

It is off by default because it puts a real message into a space other people
are reading. It is worth having because a webhook has no endpoint that reports
whether it works: posting is the only way to find out, so without it there is no
way to tell a mistyped URL from an organizational unit that has Chat apps
switched off. Those two produce different failures and this says which you have.

```sh
spacebar profile list           # what is configured, without reading a credential
spacebar profile list --json    # one object per line
spacebar profile rm alerts      # the profile and the credential behind it
```

On a machine with no keyring, which is every container and most CI runners, the
credential goes to a mode `0600` file beside the configuration instead, and
says so every time it is used.

## Send

```sh
spacebar send 'deploy done'                    # the profile knows its space
spacebar send spaces/AAAAAAA 'deploy done'     # name the space
spacebar send --md 'deploy **done**'           # translate CommonMark
echo 'deploy done' | spacebar send -           # body from stdin
spacebar send --thread-key deploys 'v1.2.3'    # group into a thread
spacebar send --dry-run 'deploy done'          # print the request, send nothing
```

```
message  spaces/AAAAAAA/messages/BBBB
space    spaces/AAAAAAA
profile  alerts
```

`--json` puts one object on stdout and nothing else, so it pipes into `jq`.
Warnings, logs and failures go to stderr, and the exit code says what happened:
`0` sent, `2` you asked for something impossible, `3` the API said no, `4`
authorize again, `5` this profile cannot do that, `6` rate limited.

**Text is sent exactly as typed.** Chat markup is not CommonMark: bold is one
asterisk, so `**bold**` arrives with the asterisks showing. `--md` translates,
and **the translation is one way**. `**bold**` becomes `*bold*`, which read back
as CommonMark is italic, so a body that has already been through `--md` must not
go through it again. The output of a dry run is not an input.

`--dry-run` prints the exact request and sends nothing. It works on every
command that reaches the network, reads included, and the credential is redacted
before it is printed:

```
$ spacebar send --dry-run 'deploy done'
POST https://chat.googleapis.com/v1/spaces/AAAAAAA/messages?key=REDACTED&token=REDACTED
Accept: application/json
Content-Type: application/json; charset=UTF-8
User-Agent: spacebar/1.0.0 (+https://github.com/kmoneil/spacebar)

{"text":"deploy done"}
```

The `Authorization` header, when there is one, prints as `REDACTED` rather than
being left out: an omitted line reads as "no credential was sent", which is a
different answer to the question a dry run is asked. `--json` gives the same
thing as an object with a `dry_run` field.

A flag this profile cannot honour fails before the network call, naming the
capability and the profile rather than pretending the flag does not exist:

```
$ spacebar send --file report.pdf 'here it is'
error: "send --file" needs attachment upload, and profile "alerts" is an
incoming webhook, which is fixed to one space, write-only, and posts as a bot.
Use a profile whose transport is useroauth:
  spacebar auth setup --profile NAME < client_secret.json
  spacebar auth login --profile NAME
Run 'spacebar auth setup' on its own to see how to create the client.
```

A webhook is still the only path that needs nothing from an administrator, which
is the point of it. Attachments are Milestone 4 even on a profile that can read.

## Authorize as yourself

Reading needs an account rather than a webhook, and an account needs an OAuth
client. If your organization blocks third-party applications, and many do, the
answer is a client you create in your own Cloud project, which is not
third-party to you. The tool prints the walkthrough with no browser and no
network:

```sh
spacebar auth setup                                        # what to click
spacebar auth setup --profile work < client_secret.json    # store what you downloaded
spacebar auth login --profile work                         # consent in a browser
spacebar auth login --profile work --send-only             # ask to post and nothing else
spacebar auth status                                       # what it may do, and for how long
spacebar auth logout                                       # forget it locally
```

The client secret is read from stdin, never from an argument. Only the
identifier and the secret are taken out of that file: its endpoints are ignored,
because a file that could redirect the consent screen would be a file that could
collect your authorization.

**Scopes are requested narrowly.** `--send-only` asks for
`chat.messages.create` and nothing else, because a narrower request is one an
administrator is more likely to approve, and that is often the difference
between the tool working and not.

**A command a scope does not cover fails at exit 5 before the request**, naming
the profile and the command that widens the grant, rather than coming back as a
`403` about your account. So an upgrade that adds a scope tells you to authorize
again on the profile you are already using:

```
$ spacebar spaces members spaces/AAAAAAA
error: "spaces members" needs the ability to read who is in a space, and profile "work" was not granted it.
Consent to the scope it needs by authorizing again:
  spacebar auth login --profile work
The scopes this build asks for have grown since that token was issued.
```

`spacebar auth status` prints the scopes a profile actually holds.

**Reading and writing need different things from your Cloud project.** Enabling
the Chat API is enough to read: spaces, messages, and members all work as soon
as you have consented. Writing also needs a Chat app configured on the project,
under Chat API, Configuration in the Cloud console. Without it, every write
refuses with a 404 saying "Google Chat app not found", which is not about the
space and which `spacebar` explains when it happens.

If you only need to read, you can skip that step. If you need to post as
yourself, do it before wondering why sending fails.

**One warning you may see.** An OAuth client that has not been verified by
Google is in testing mode, where authorizations are revoked seven days after
consent. Nothing in the API says whether yours is, so the warning is worded as a
possibility, and it stops for good once a refresh proves the limit does not
apply to you. A client with an Internal user type is not subject to it at all.

[docs/ADMIN.md](docs/ADMIN.md) is the page to hand an administrator.

## Read a space

On a profile authorized as you, rather than a webhook:

```sh
spacebar spaces list                                  # name, type, display name
spacebar spaces list --limit 0                        # every one
spacebar spaces get spaces/AAAAAAA
spacebar spaces get 'Ops'                             # or by display name
spacebar spaces members spaces/AAAAAAA                # who, state, role

spacebar messages list spaces/AAAAAAA                 # newest 25
spacebar messages list spaces/AAAAAAA --limit 100
spacebar messages list spaces/AAAAAAA --order oldest
spacebar messages get spaces/AAAAAAA/messages/BBBBBBB
```

**Newest first**, so that the default limit returns the latest messages rather
than the oldest ones in a space's history. Reading a conversation in the order
it happened is what `tail` will be for.

## Name a space without its ID

Every command that takes a `SPACE` also takes a display name or an address, and
resolves it before making the request:

```sh
spacebar spaces get 'Ops'                     # a display name, matched loosely
spacebar messages list 'ops' --limit 20       # case does not matter
spacebar send 'Ops' 'deploy done'
spacebar spaces get someone@example.com       # the direct message with them
```

**Four steps, in order, and the last one never guesses.** A literal
`spaces/XXXX` passes straight through and costs nothing. Then a profile alias.
Then anything containing `@`, which is a person and becomes a direct message
lookup. Then the display names of the spaces you can reach: an exact match
wins, otherwise a substring match, both ignoring case.

**Two matches is a question, not a tie.** If more than one space matches, it
lists them and exits 2 rather than picking one. The cost of picking wrong is a
message in front of people who were not meant to see it.

```
$ spacebar send 'ops' 'deploy done'
error: 2 spaces match "ops", and this tool does not guess which one you meant:
  spaces/AAAAAAA	Ops
  spaces/BBBBBBB	Ops Escalation
Name the one you want directly, as in: spacebar spaces/AAAAAAA
```

An exact match beats a substring, so with those two spaces `'Ops'` reaches the
one actually called Ops rather than being ambiguous forever.

**The space list is cached for 24 hours**, per profile, under
`$XDG_CACHE_HOME/spacebar`. Listing spaces costs quota shared with every other
app in those spaces, so a resolver that listed on every command would degrade
the space for everybody. `--refresh` fetches it again. A resource name or an
alias never touches the cache at all.

**Give a space a name you will remember:**

```sh
spacebar alias set eng spaces/AAAAAAA
spacebar alias set eng 'Engineering'      # resolved now, stored as the space
spacebar alias set bob bob@example.com    # the direct message with them
spacebar alias list
spacebar alias rm eng
```

The target is resolved when the alias is set, and the space it resolved to is
what gets stored. A display name is a label somebody else controls, so storing
one would mean an alias that quietly points somewhere new the day a room is
renamed.

An alias belongs to one profile, so a work profile and a personal one cannot
see each other's names. It cannot contain a slash or an `@`, because resolution
reads a name of either shape as something other than an alias, and one that
could never be consulted is worse than one that is refused.

A webhook profile can use an alias, because an alias is a local map and needs no
permission. The last two steps need to read and are refused on one.

## Follow a space

```sh
spacebar tail spaces/AAAAAAA
spacebar tail eng --backfill 20        # the last 20, then follow
spacebar tail eng --json | jq -r .text
```

**Oldest first**, because this is a conversation read in the order it happened.
That is the opposite of `messages list`, which is newest first so that a limit
cuts from the recent end.

Google Chat offers no socket, so this polls. **The interval floor is 2s and a
smaller one is refused rather than rounded up**: per-space quota is shared with
every other app acting in that space, so a tight loop degrades the space for
everybody in it. After five polls with nothing new the interval doubles, up to
a minute, and any message resets it.

**Ctrl-C exits 0.** It is how the command is meant to end, so it is not a
failure, and a script wrapping it does not have to special-case the code.

Two things it does not do. It does not replay what was already there unless
`--backfill` asks. And it never corrects itself: a message edited or deleted
while you are watching is not shown again, because a poll sees new messages and
an edit does not make one. Seeing mutations needs `spaceEvents`, which is a
different command still to come.

**`spaces members` identifies people by resource name, not by display name.**
The Chat API returns a member as `users/NNN` and a type, with no name attached,
so the display-name column is blank. That is the API rather than a gap here,
and it is worth knowing before you build something that expects a name in it.
`users/NNN` is the stable identifier in any case; a display name is chosen by
the account holder and is not unique.

**A list streams.** Pages are fetched as you consume them, so `--json` is NDJSON
and the first object arrives before the last page has been requested:

```sh
spacebar spaces list --json | jq -r '.name + "\t" + .display_name'
spacebar messages list spaces/AAAAAAA --limit 5 --json | jq -r .text
```

`--limit` is honoured exactly. Asking for five fetches five, rather than
fetching a page of a thousand and discarding the rest, which matters because the
per-space quota is shared with every other app acting in that space.

**A list that fails part way through keeps the rows it already wrote** and exits
non-zero. Every line is a complete object, so a caller that checks the exit code
is never handed a truncated answer that looks whole. This is the one place the
"a failing command writes nothing to stdout" rule is narrower than it sounds,
and it is the price of streaming rather than an oversight.

`--dry-run` works on a read too, printing the request without spending a call
against the quota:

```
$ spacebar spaces list --dry-run
GET https://chat.googleapis.com/v1/spaces?pageSize=25
Accept: application/json
Authorization: REDACTED
User-Agent: spacebar/1.0.0 (+https://github.com/kmoneil/spacebar)
```

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
