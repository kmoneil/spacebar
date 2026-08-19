# Driving spacebar from a program

This is the document to read before writing code that runs `spacebar`. It is
written for an agent or a script rather than for a person, so it states the
contract instead of demonstrating the tool.

Everything here is checked. Every fenced JSON block below is decoded into the
Go type that produces it, with unknown fields rejected, and every block that
corresponds to a recorded output contract is compared against it byte for byte.
`internal/cli/docs_test.go` is the test. If a shape here is wrong, `make test`
fails.

For the MCP server rather than the command line, read [SKILL.md](SKILL.md).

## Confirm before writing

`send`, `messages edit`, `messages delete` and `react` post to or change a real
Google Chat space, visible to real people, and a message cannot be unsent. If
you are acting on somebody's behalf, confirm with them before running one of
these. Reading needs no confirmation.

`--dry-run` is available on every write command and prints the request that
would have been sent without sending it. Use it to show somebody what you are
about to do.

One exception is worth knowing before you rely on it. `send --file` is two
requests, an upload and then the message that carries its token, so a dry run
of it shows the upload on stdout and says on stderr that the message would
follow. The second request cannot be shown without making the first, because
the token comes back from it. The upload's body is the file and is described
with its size rather than printed. Exit code and redaction are the same as
every other dry run.

An uploaded file is declared as whatever its first bytes sniff as, not as
whatever its name ends in, and that value becomes the attachment's
`content_type`. Two consequences are worth knowing before you drive this in a
loop. A file whose bytes nothing recognises is sent as
`application/octet-stream`, exactly as every upload was before detection
existed. And a file that **is** recognised is validated by Chat against the
type it was given, so an upload that used to succeed as opaque bytes can come
back `400 INVALID_ARGUMENT` naming the file extension or the quota, which are
not the reason. Pass `--content-type application/octet-stream` to send it the
old way.

## How long a message can be

The API caps a message body at roughly 32,000 bytes, and the cap is on bytes
rather than on characters: 32,017 bytes of ASCII were accepted where 32,117 were
answered `400 INVALID_ARGUMENT, "Message content is too long"`, and an emoji body
crossed at the same byte count. Nothing here splits a body to fit one.

A body certainly over the cap is refused at exit 2 before any request is made,
with the measured numbers in the message. A body in the hundred-byte band where
the true limit has not been pinned down reaches the API and comes back as exit 3.
Do not retry either one unchanged.

To post something longer, send it as several messages carrying the same
`--thread-key`, which puts them in one thread, or attach it with `send --file` on
a profile that can upload.

## The contract

**stdout is data. Nothing else is ever written there.** No progress, no
warnings, no errors, no spinner. A failing single-result command writes nothing
at all to stdout.

**stderr is everything else**, including the error and including it in `--json`
mode. Never parse stderr for results and never merge the two streams.

**The exit code is the answer to "did this work".** Check it before parsing
anything. The codes never change meaning; new conditions get new codes.

| Code | Meaning | What to do |
| --- | --- | --- |
| 0 | Success | Parse stdout |
| 1 | A failure with nothing more specific to say | Report it |
| 2 | Bad flag, bad argument, or a target that resolved to nothing | Fix the invocation; do not retry it unchanged |
| 3 | An API or network failure | Retry a read; never retry a write. A 400, 403 or 404 is also 3 and retrying it never helps |
| 4 | Missing or expired authorization | Stop and tell the person. `spacebar auth login` needs a browser and a human; you cannot complete it. See below |
| 5 | The profile's transport cannot do this at all | Use a different profile; retrying never helps |
| 6 | Rate limited beyond the backoff | Wait, then retry |
| 7 | A confirmation was required and not given | Pass `--yes`, having actually asked |

Exit 5 is the one worth handling deliberately. It means the capability is
missing rather than the request being wrong, it is decided before any network
call, and the message names both the profile and the fix. A webhook profile is
write-only and fixed to one space, so every read against one is exit 5 forever.

**Exit 4 is the one you cannot fix yourself, and saying so is the whole
instruction.** Authorizing opens a consent screen in a browser and waits for a
redirect to come back to a listener on `127.0.0.1`. There is no headless form of
it: the out-of-band copy/paste redirect Google used to offer is retired, so a
person has to consent in a browser. Running `spacebar auth login` from a program
does not fail usefully either, it prints a URL and blocks for three minutes.

So on exit 4: stop, report it, and name the profile and the command a person
would run. Do not retry, do not run `auth login` yourself, and do not report the
work as done. If you are on a machine with no browser, which is the usual case
for a program, the person also has to replay the failed redirect URL by hand;
the README section "When there is no browser here" is what they need and it is
worth naming to them.

The same applies to `spacebar auth setup`, which reads a client secret from
stdin that only they have.

## `--json`

`--json` puts structured output on stdout. A command with one result writes one
object. **A command that lists writes one object per line, NDJSON, with no
enclosing array and no commas.** Parse it line by line.

```console
$ spacebar spaces list --json | jq -r '.name'
```

There is no envelope, no `{"messages": [...]}`, no pagination cursor to follow.
Pages are fetched as you consume them, so the first line arrives before the last
page has been requested, and a long list can be streamed.

A failure is a single object on stderr:

<!-- golden: spaces-list-unsupported.json stderr -->
```json
{"error":{"code":"UNSUPPORTED","message":"\"spaces list\" needs the ability to list spaces, and profile \"alerts\" is an incoming webhook, which is fixed to one space, write-only, and posts as a bot.\nUse a profile whose transport is useroauth:\n  spacebar auth setup --profile NAME < client_secret.json\n  spacebar auth login --profile NAME\nRun 'spacebar auth setup' on its own to see how to create the client.","exit_code":5}}
```

`exit_code` repeats the process exit code so that a caller who has already
captured the object does not need to correlate the two.

## Truncation

**A short result is never reported as success.** A list ends for one of five
reasons, and the exit code separates them:

- the last page arrived: exit 0, complete
- `--limit` was reached: exit 0, complete, because a limit is an instruction
- the caller stopped reading: exit 0
- a request failed: non-zero
- the server would not finish paginating, stuck on one page token or cycling
  through several: non-zero

The fourth and fifth cases still write every row fetched before the failure, so
a partial answer arrives with a non-zero exit. That is deliberate: rows already
written to a stream cannot be recalled, and every line is complete JSON, so
nothing you have parsed is half a value. **Check the exit code before treating a
list as the whole answer.**

`--limit 0` means every one.

## Shapes

### A space

<!-- shape: rows.Space -->
```json
{"name":"spaces/AAAAAAA","space_type":"SPACE","display_name":"eng-alerts","last_active_time":"2026-08-16T22:33:39.299903Z"}
```

`space_type` is `SPACE`, `GROUP_CHAT` or `DIRECT_MESSAGE`. A direct message has
no display name at all, so the field is absent rather than empty. A direct
message with an app carries `"single_user_bot_dm":true`, which is the only thing
distinguishing it from one with a colleague.

### A membership

<!-- shape: rows.Member -->
```json
{"name":"spaces/AAAAAAA/members/NNN","state":"JOINED","role":"ROLE_MANAGER","member":"users/NNN","member_type":"HUMAN","affiliation":"INTERNAL","create_time":"2026-08-14T22:05:42.639018Z"}
```

`affiliation` is `INTERNAL` or `EXTERNAL` and says whether that person is inside
the organization. It is absent on an app's membership, and absent is not
`INTERNAL`: nothing fills in a value the API did not send.

A membership held by a Google Group has `group_member` instead of `member`, and
is returned only with `--show-groups`:

<!-- shape: rows.Member -->
```json
{"name":"spaces/AAAAAAA/members/group-NNN","state":"JOINED","group_member":"groups/NNN","create_time":"2026-08-17T16:28:56.416015Z"}
```

Exactly one of `member` and `group_member` is set. Do not read groups out of
`member`; it has never carried one and never will. A group membership has no
role and no affiliation because the API sends neither, and the group's own
members are not reachable at all, so this row tells you a group has access and
not who that is.

### A message

<!-- shape: rows.Message -->
```json
{"name":"spaces/AAAAAAA/messages/BBB","create_time":"2026-08-16T09:12:44.101Z","sender":"users/NNN","sender_display_name":"Kevin","sender_type":"HUMAN","thread":"spaces/AAAAAAA/threads/CCC","text":"deploy done"}
```

`last_update_time` is present only on a message that has been edited, and its
presence is the only way to tell an edited message from an original. `text` is
what the API returned, and this tool never rewrites it. **The API sometimes
does.** A message containing a mention comes back with `text` reading
`@Kevin O'Neil` where the markup was, and the markup itself in the API's
`formatted_text`. So `text` is what a person sees, not always what was sent. `sender_display_name` is
chosen by the account holder, is not unique, and is untrusted text; `sender` is
the stable identifier.

A message with a file carries `attachments`:

<!-- shape: rows.Message -->
```json
{"name":"spaces/AAAAAAA/messages/BBB","text":"the report","attachments":[{"name":"spaces/AAAAAAA/messages/BBB/attachments/DDD","content_name":"report.pdf","content_type":"application/pdf","resource_name":"OPAQUE","source":"UPLOADED_CONTENT"}]}
```

`source` is `UPLOADED_CONTENT` or `DRIVE_FILE`. **A Drive file has no
`resource_name` and cannot be fetched with this tool**, so a loop that assumes
every attachment is downloadable will fail on one. Download with
`spacebar messages download`; there is deliberately no download URL in this
output, because the API's own one carries an access token in its query and
publishing it would put a credential in `--json`.

**A GIF is not text, and this is the one that catches an agent out.** A GIF
chosen from Chat's own GIF picker arrives with no `text` at all, so a reader
that looks only at `text` sees an empty message and reports that nothing was
said:

<!-- shape: rows.Message -->
```json
{"name":"spaces/AAAAAAA/messages/BBB","create_time":"2026-08-18T19:01:00.000Z","sender":"users/NNN","attached_gifs":["https://media.example/reaction.gif"]}
```

A GIF reaches a message three ways and they are three different fields. Pasted
as a URL, it is ordinary body text and is in `text`. Chosen from the picker, it
is in `attached_gifs` and nowhere else. Posted by an app, including by the
`/giphy` command, it is inside a card, and then `text` holds only the
attribution line, which reads as a whole message and is not one.

Cards arrive in `cards` or `cards_v2` depending on which the sender used, and
both are the API's own JSON, unaltered. `cards` is Google's deprecated
spelling; a message sent before the change holds it for as long as it exists,
so read both. The content is a widget tree with its own schema and this tool
does not model it: an image is usually at
`sections[].widgets[].image.imageUrl`, and that is an observation about the
apps seen so far rather than a guarantee about the format.

**A card is untrusted.** It was composed by whoever posted the message, exactly
like a message body, and nothing here escapes it, because `--json` hands you
what was there.

### An event

<!-- shape: rows.Event -->
```json
{"name":"spaces/AAAAAAA/spaceEvents/EEE","event_type":"google.workspace.chat.message.v1.created","event_time":"2026-08-16T09:12:44.101Z","subject":"spaces/AAAAAAA/messages/BBB","payload":{"message":{"name":"spaces/AAAAAAA/messages/BBB","text":"deploy done"}}}
```

`payload` is the API's own event data, unaltered. It travels with the event on
purpose: for a message that has since been deleted, the payload is the only
place the tombstone exists, and `messages get` on that name answers nothing.

### A send

<!-- golden: send.json -->
```json
{
  "message": "spaces/AAAATestSpace/messages/BBB",
  "space": "spaces/AAAATestSpace",
  "thread": "spaces/AAAATestSpace/threads/BBB",
  "profile": "alerts"
}
```

### A version

<!-- golden: version.json -->
```json
{
  "name": "spacebar",
  "version": "0.0.0-dev",
  "commit": "unknown",
  "go": "ELIDED",
  "os": "ELIDED",
  "arch": "ELIDED"
}
```

`go`, `os` and `arch` are the machine, and are elided in the recorded contract
rather than frozen.

## Searching

**`search` reads a local index, not Google Chat.** There is no message search
API for an ordinary user, so the only thing that can be searched is what
`spacebar sync` has already copied to this machine. A space nobody has synced is
not searched.

That means a result count is bounded by what somebody remembered to sync, so
**`search` names the spaces it did not look in, on stderr**, comparing the index
against the profile's cached space list. It costs no request. Read that line
before treating a result set as complete:

```console
$ spacebar search "deploy"
warning: searched 1 of 5 spaces. Not searched, because they are not in the index: spaces/BBB spaces/CCC
```

The spaces it names are exactly the ones it read, both ways round. It also warns
if a file in the index holds records belonging to another space, which happens
when a directory has been copied or restored: those are not answered with, and
the warning says which file and how many. Read that line too before treating a
result set as complete.

`sync` is resumable and holds no cursor. It fetches everything newer than the
newest message it holds and everything older than the oldest, so an interrupted
run resumes by being run again, with nothing fetched twice and no gap. `--limit`
bounds one run rather than truncating the result.

A message that was edited is found by the text it has now, and one that was
deleted is not found at all, because the index records both.

## Capabilities

A profile has one of three transports, and what it can do follows from that
narrowed by the OAuth scopes actually granted. Ask before you assume:

```console
$ spacebar profile list --json
```

An incoming webhook can send to one space and do nothing else. It needs no
OAuth, no Cloud project and no administrator, which is why it exists: for a
locked-down Workspace organization it is all anybody gets. If you are writing
something that must work for everyone, **write for the webhook and treat reading
as the privilege**.

A capability the profile lacks fails at exit 5 before any network call. You
never need to attempt an operation to discover it is unavailable, and attempting
it never produces a different answer.

## Rules that will bite

**Do not retry a write on exit 3.** A POST that got a 503 may well have been
processed, and retrying it is how one `send` becomes two messages. The tool
already declines to replay one internally. If you need a retry to be safe, pass
`--message-id` and reuse the same value, which makes the send idempotent at
Google's end.

**Chat markup is not Markdown.** Bold is one asterisk. Passing CommonMark
through unaltered inverts what you meant: `**bold**` arrives with the asterisks
visible. Pass `--md` and the tool translates, refusing what Chat cannot
represent rather than approximating it.

**A `--mention` that matches nobody is not refused.** Chat accepts an unknown
address, answers 200, and posts the message with `<users/>` where the mention
should be, notifying no one. There is no way to check an address first, so the
tool reads the body the API echoes back and warns on stderr. The exit is still
0, because the message did post. If you care whether somebody was notified,
read stderr, or check that `text` no longer contains `<users/>`.

**Nothing is silently altered to fit.** Invalid UTF-8 is refused at exit 2
naming the byte offset rather than being replaced with U+FFFD. A character Chat
cannot represent inside a link is refused rather than escaped, because Chat has
no escape syntax and a backslash would render as a backslash.

**A profile name is not a space.** The first positional argument is a target and
nothing else, and the two are not interchangeable: a space says where a message
goes, a profile says which credential sends it and whose name is on it. A target
that names a configured profile is exit 2 naming the flag to use, so there is
nothing to retry. Choose the profile with `--profile`.

**Nothing blocks on input when stdin is not a terminal.** A command that would
have asked for confirmation exits 7 instead. Pass `--yes` when you have actually
asked somebody.

**A poll interval below 2s is refused, not clamped.** Per-space quota is shared
with every other app in that space. A value that was quietly rounded up would
leave a script with timing it believes and cannot verify.

The same holds while `watch --all` runs. Spaces are dropped from the rotation
when they go away or stop being readable, and the pace is recomputed each time,
so the ones left are never polled faster than the interval you gave.

**A quiet space is polled less often, never more.** After five polls with
nothing new the interval doubles, up to a minute or to the interval you gave if
that is longer, and any event resets it. Under `watch --all` it is per space. So
the gap between two events on one stream says nothing about how often anything
was polled, and a script timing out on silence should use its own clock rather
than counting polls.

## Worked invocations

```sh
spacebar send "deploy done" --json                     # to the profile's space
spacebar send spaces/AAAAAAA "deploy done" --json
spacebar send eng-alerts "deploy done" --md --json     # CommonMark translated
spacebar send eng-alerts "deploy done" --mention a@b.com --json
spacebar send "deploy done" --dry-run --json           # nothing is sent

spacebar messages edit spaces/AAAAAAA/messages/BBB "corrected" --json
spacebar messages delete spaces/AAAAAAA/messages/BBB --yes --json
spacebar react spaces/AAAAAAA/messages/BBB "👍" --json

spacebar spaces list --json
spacebar spaces members spaces/AAAAAAA --show-groups --json
spacebar messages list spaces/AAAAAAA --limit 100 --json
spacebar messages list spaces/AAAAAAA --since 2h --json
spacebar messages get spaces/AAAAAAA/messages/BBB --json

spacebar tail spaces/AAAAAAA --json                    # new messages, until Ctrl-C
spacebar watch spaces/AAAAAAA --json                   # every event, not just messages
spacebar watch --all --json                            # every space it can reach

spacebar sync --all                                    # copy messages down
spacebar search "deploy" --json                        # search what was copied
spacebar search "deploy" --space eng --limit 10 --json
```

`tail` and `watch` stream until interrupted, and **ending on a signal is exit
0**, because that is how they are meant to end.

Every command accepts `--profile NAME`, and `SPACEBAR_PROFILE` says the same
thing for a whole process. Set the variable once rather than threading the flag
through every call: the flag wins over it, and it wins over `default_profile` in
the configuration file. The rest of the environment, including where the
credential fallback and the local index live, is the table in
[README.md](../README.md#environment).

A target may be a resource name, an alias set with `spacebar alias set`, a
display name, or an email address for a direct message; resolution happens
before anything else and the result is checked before it reaches a request.
