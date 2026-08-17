<p align="center">
  <img src="docs/assets/spacebar.png" alt="spacebar" width="480">
</p>

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

**Milestones 1 to 3 of 6 are done, and 4 and 5 are most of the way.** `spacebar send` works over an incoming
webhook, with no OAuth, no administrator approval, and no Cloud project: give a
profile a webhook URL and send. On a profile authorized as you, `spaces list`,
`spaces get`, `spaces members`, `messages list` and `messages get` work as
well, as do `tail`, `watch`, the `alias` group, and `messages edit`,
`messages delete` and `react`. `auth`, `version`, `licenses` and `completion`
work, as do `send --file` and `messages download`, and `watch --all` follows
every space at once on a request rate budgeted against the project's quota.
`spacebar mcp` serves the read paths to a model over MCP, and `send_message`
and `react_to_message` when `--allow-write` says so. Milestones 4 and 5 are
closed; milestone 6 is the local index and search.

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

Writing has now been run for real as well. A message was posted as the account
rather than as a bot, edited, reacted to, and deleted, against the live API on
2026-08-16, and that run is what settled who may do what: editing is limited to
the message's author and deleting is not. The tool had already been written with
the opposite assumption in its own help text, and the live check is the only
reason it did not ship that way.

The plan, in six milestones:

| #   | Deliverable                                               | State    |
| --- | --------------------------------------------------------- | -------- |
| 1   | Skeleton, licensing, CI gates                             | **done** |
| 2   | Webhook transport: `send` with no OAuth at all            | **done** |
| 3   | User OAuth: `auth`, `spaces`, `messages`                  | **done** |
| 4   | Full CLI: `tail`, `watch`, `react`, aliases, attachments  | **done** |
| 5   | MCP server                                                | **done** |
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

**A small dependency tree, on purpose.** Five direct dependencies, which is the
ceiling this project set itself and has now reached: cobra and pflag for the
command tree, go-keyring for the credential store, `x/oauth2` for the token
exchange, and the MCP SDK. A sixth needs an argument, and the answer is usually
no. Count what a dependency links rather than what it requires: the MCP SDK is
one line in `go.mod` and six modules in the binary, more than a third of
`NOTICE`. The Chat API client is hand-rolled because the generated one is 40k
lines and drags in a transport chain we would then not control.

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

**Some of this needs a Chat app configured on your Cloud project**, which is a
separate step from enabling the Chat API and lives under Chat API,
Configuration in the Cloud console. Measured, not guessed:

```
works without it   spaces list, spaces get, spaces members,
                   messages list, messages get, and resolving an address
needs it           send, and following a space with tail's successor
```

The line is not reading against writing, which is the obvious guess and is
wrong: following a space's events is a read and needs the app.

Without it those calls refuse with a 404 saying "Google Chat app not found",
which mentions neither the space nor the scope. `spacebar` explains it when it
happens, so you will not have to work that out from the API's wording.

**One warning you may see.** An OAuth client that has not been verified by
Google is in testing mode, where authorizations are revoked seven days after
consent. Nothing in the API says whether yours is, so the warning is worded as a
possibility, and it stops for good once a refresh proves the limit does not
apply to you. A client with an Internal user type is not subject to it at all.

[docs/ADMIN.md](docs/ADMIN.md) is the page to hand an administrator.
[docs/AGENTS.md](docs/AGENTS.md) is the one to hand a script or an agent driving
the command line, and [docs/SKILL.md](docs/SKILL.md) covers the MCP server.
Every example in both is held to the code by a test, because the reader of those
two cannot tell a stale example from a current one.

## Read a space

On a profile authorized as you, rather than a webhook:

```sh
spacebar spaces list                                  # name, type, name, bot, last active
spacebar spaces list --limit 0                        # every one
spacebar spaces get spaces/AAAAAAA
spacebar spaces get 'Ops'                             # or by display name
spacebar spaces members spaces/AAAAAAA                # who, kind, state, role, affiliation
spacebar spaces members spaces/AAAAAAA --show-invited # and anybody asked but not joined
spacebar spaces members spaces/AAAAAAA --show-groups   # and any Google Group with access

spacebar messages list spaces/AAAAAAA                 # newest 25
spacebar messages list spaces/AAAAAAA --limit 100
spacebar messages list spaces/AAAAAAA --order oldest
spacebar messages list spaces/AAAAAAA --since 2h      # the last two hours
spacebar messages list spaces/AAAAAAA --since 2026-08-16T09:00:00Z --until 2026-08-16T17:00:00Z
spacebar messages get spaces/AAAAAAA/messages/BBBBBBB
```

And the three that change what is already there:

```sh
spacebar messages edit spaces/AAAAAAA/messages/BBBBBBB 'the corrected text'
spacebar messages delete spaces/AAAAAAA/messages/BBBBBBB      # asks first
spacebar react spaces/AAAAAAA/messages/BBBBBBB 👍
```

**Editing is limited to messages you sent, and deleting is not.** Measured, not
assumed: editing your own message answers 200 and editing somebody else's
answers 403, a second apart on the same token, while a delete of somebody
else's message in a space you manage is allowed. So `delete` asks before it
acts, and with stdin not a terminal it exits 7 rather than prompting. `--yes`
answers in advance.

**`react` takes the emoji, not a shortcode.** `:thumbsup:` is refused by the
API at the type level, so this tool refuses it before the request and says to
paste the character rather than carrying a table of shortcode names that goes
stale.

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

It holds what `spaces list` returns, at mode 0600: resource names, types, and
display names, with no message text and no credential. `auth logout` and
`profile rm` delete it. That second one is not tidiness: a profile name is
reusable and the file is keyed by it, so a name configured again for a
different account would otherwise resolve display names against the previous
account's spaces for the rest of the day.

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
spacebar tail eng --since 30m          # everything since, then follow
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

### Attachments

```sh
spacebar send spaces/AAAAAAA 'the report' --file report.pdf
spacebar messages download spaces/AAAAAAA/messages/BBBBBBB --out ~/Downloads
```

Sending a file is two requests, because that is the API's shape: the bytes are
exchanged for a token, and the token is what a message can carry. The upload
goes first, so a file that fails to upload does not become a message with the
text and no file.

**A downloaded file lands inside `--out` and nowhere else.** The name comes
from whoever posted the message, so an attachment called
`../../.ssh/authorized_keys` is written as `.._.._.ssh_authorized_keys` in the
directory you asked for. Nothing is overwritten without `--force`, because the
name is not yours.

**The API's own download URL is never printed.** It carries an access token in
its query, which makes it a credential rather than a link, so `--json` gives you
the attachment's `resource_name` and this tool fetches the bytes with your own
credential.

A Drive attachment is listed and skipped: Chat returns a reference rather than
the bytes, and fetching it is Drive's API rather than this one.

### Watching, which is not tailing

`tail` polls for messages, so it cannot see an edit, a deletion, or a reaction:
a poll on `createTime` returns new messages and none of those makes one.
`watch` polls `spaceEvents` instead and reports them as events.

```sh
spacebar watch spaces/AAAAAAA
spacebar watch eng --events message,reaction,membership
spacebar watch eng --since 2h --json
spacebar watch --all
```

Columns are the time, the kind of event, the resource it happened to, and the
message text when the event carries one.

```
2026-08-16T22:04:01.102175Z  message created   spaces/A/messages/8xM   spacebar watch live check
2026-08-16T22:04:01.769711Z  reaction created  spaces/A/messages/8xM/reactions/115...
```

`--events` takes any of `message`, `reaction`, `membership` and `space`, and
defaults to `message,reaction`, which is the conversation. Membership and space
updates are administrative and arrive on a different rhythm, so they are opt-in
rather than noise.

**`--all` watches every space the profile can reach**, and the interesting part
is the pace it chooses. The spaces are polled one at a time, round robin, at a
rate this process holds to 10 requests a second however many there are. Google
allows 3000 a minute for the whole Cloud project, which is fifty a second, and
that quota belongs to the OAuth client rather than to you: an organization
following [docs/ADMIN.md](docs/ADMIN.md) shares one, so taking all of it would
mean the first person to start `--all` denies the second, with nothing in any
response saying why. A fifth of it leaves room for five.

Below twenty spaces the 2s floor decides and nothing is slowed at all. Above it
each space comes round less often, thirty spaces every 3s and a hundred every
10s, and the interval chosen is printed on stderr at startup unless `--quiet` or
`--json` says the reader is not a person.

The list of spaces is taken once. A space created while it runs is not picked
up, because re-listing spends the quota this is being careful with and a watch
whose subject changes underneath it is harder to reason about; restart to pick
up a new one. A space that goes away, or that turns out not to be readable, is
dropped with a line on stderr saying which and why, and the others carry on. If
every space is dropped the exit is non-zero, because a watch that is watching
nothing has not finished, it has been abandoned.

**`--json` carries the API's own event payload**, unaltered, under `payload`.
That is a departure from how the other shapes work here, and it is deliberate:
for a message that has since been deleted, the payload is the only place its
tombstone exists, and `messages get` on that name answers nothing.

**An event's payload is the subject as it is now, not as it was.** Measured: an
edit event for a message deleted ten minutes later carries the tombstone rather
than the text the edit set. Watching live gets the text; watching history gets
the current state.

**This endpoint needs the Chat app configured**, which is a separate step from
enabling the Chat API and is the same step every write needs. It is the reason
the line is not reading against writing.

**A time window is an RFC 3339 timestamp or how long ago**, as in `30m`, `2h`
or `36h`. Durations have no day unit, so a week is `168h`. Both `--since` and
`--until` are **strictly** outside the boundary: the API compares `createTime`
with `>` and `<` and refuses `>=`, so a message posted at exactly the moment
you name is not returned, and this tool would rather say that than shift your
timestamp by a nanosecond to hide it. On `messages list` they combine with
`--filter`, and your expression is parenthesized first so that an `OR` in it
keeps its meaning. `tail` takes `--since` and refuses it beside `--backfill`,
because the two disagree about where to start.

Two things it does not do. It does not replay what was already there unless
`--backfill` asks. And it never corrects itself: a message edited or deleted
while you are watching is not shown again, because a poll sees new messages and
an edit does not make one. Seeing mutations needs `spaceEvents`, which is a
different command still to come.

**`spaces members` identifies people by resource name, not by display name.**
The Chat API returns a member as `users/NNN` and a type, with no name attached,
and the sender of a message comes back the same way. That is how a
user-authorized read answers rather than a gap here, and it is worth knowing
before you build something that expects a name. `users/NNN` is the stable
identifier in any case; a display name is chosen by the account holder and is
not unique. `--json` carries a `display_name` field regardless, so a caller gets
one for free if the API ever starts sending it.

**The affiliation column says who is outside your organization.** It is
`INTERNAL` or `EXTERNAL`, and it is the column to read before posting something
that should not leave the company. An app's membership carries no affiliation at
all, so that column is blank on those rows, and nothing here fills in a value the
API did not send.

**`spaces members` lists people who have joined.** Somebody who was invited and
has not accepted is not returned at all unless `--show-invited` asks for them,
and a membership held by a Google Group is not returned unless `--show-groups`
does. Both are the API's own defaults rather than choices made here.

**`--show-groups` is the one to reach for before posting something sensitive.** A
space can grant access to a group, and then everybody in that group is in the
space without a membership of their own, so the default list is not the whole
answer to "who can see what I post here". A group row carries `groups/NNN` in the
first column and `GROUP` in the second, and `--json` gives it its own
`group_member` key rather than putting it in `member`, so nothing that selects
`.member` starts receiving groups:

```console
$ spacebar spaces members spaces/AAAAAAA --show-groups
users/NNN	HUMAN	JOINED	ROLE_MANAGER	INTERNAL
groups/NNN	GROUP	JOINED
```

Read that second row for what it does not say. There is no role and no
affiliation, because the API sends neither, so the column that tells you who is
outside your organization is blank on exactly the row that can let in the most
people. There is no group name or address either, only `groups/NNN`, the same
way a person is only `users/NNN`. And the group's own members are not listed
anywhere and are not reachable from a Chat scope at all. What this flag tells
you is that a group has access, which is a different and smaller thing than
knowing who does.

**A direct message with an app is marked.** Every direct message has a blank
display name, so without the fourth column a conversation with a colleague and a
conversation with a bot are the same row. `--json` carries it as
`single_user_bot_dm`, present only when true.

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

## Serve it to a model

`spacebar mcp` speaks MCP on stdin and stdout. It is not a command to run by
hand: an MCP client starts it, and in that client's configuration it is the
command with the profile as an argument.

```json
{"command": "spacebar", "args": ["mcp", "--profile", "work"]}
```

Five read tools: `list_spaces`, `get_space`, `list_members`, `list_messages`,
`get_message`. They return the same shapes `--json` does, because both come
from one place.

**A tool this profile cannot serve is not registered at all.** A model that
cannot see a tool cannot argue itself into calling it; one that can see a broken
tool will call it, be refused, try again differently, and tell you the tool is
broken. So a profile whose token lacks `chat.memberships.readonly` offers four
tools rather than five, and a webhook profile, which is write-only, is refused
before the session starts rather than connected with nothing to offer. This is
deliberately the opposite of how flags work in the CLI, where a person reading
`--help` is served by knowing that `--file` exists.

**Writes are off unless you say otherwise.**

```json
{"command": "spacebar", "args": ["mcp", "--profile", "work", "--allow-write",
                                 "--allow-space", "spaces/AAAAAAA"]}
```

Without `--allow-write`, `send_message` is not registered at all, so there is no
tool for a model to talk itself into calling. With it, `--allow-space` narrows
where it may post, checked against the space a call resolves to rather than
against the string the model sent, and refused before the request rather than
after it.

Every write tool's description ends with "This posts a visible message to a real
Google Chat space. Confirm with the user before calling.", which is what the
model reads before deciding.

**Every tool call is one JSON line on stderr**, and neither `--quiet` nor
`--json` turns it off. It says which tool, which profile, what the arguments
were with long strings truncated, and whether it worked. An audit line a flag
can silence is missing exactly when somebody has a reason to silence it.

A webhook profile is worth a word here: it can post and can do nothing else, so
`spacebar mcp --allow-write` on one registers exactly one tool, and without the
flag it is refused before the session starts because there would be nothing to
offer.

**Message text is untrusted input.** It reaches a model as data, and a message
that asks the model to do something is still a message. That is why writing over
MCP will arrive with a confirmation requirement rather than as another tool.

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
