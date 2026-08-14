# Security

`spacebar` holds a credential to somebody's Google Chat and posts to real
spaces that real colleagues read. Three things follow from that, and this
document is about all three: the credential must not leak, a message must never
be sent that the operator did not ask for, and a result must never claim more
than it can back up. A script that acts on a partial answer it believes is
complete is a security problem wearing a correctness costume.

Google Chat and Google are trademarks of Google LLC. This project is not
affiliated with, sponsored by, or endorsed by Google.

## Status of this document

Pre-1.0, and the six milestones this is planned in are still landing.

Every claim below is either **held by a test**, in which case the test is
named, or a **requirement on the milestone that implements it**, in which case
it carries that milestone: `(M3)`, `(M5)`. A claim with neither is a bug in
this document, and should be reported as one.

The distinction matters. A security document that describes an intention in the
present tense is how a gap survives review: everybody reads it, everybody
believes it, and nobody checks.

A `SPEC.md §N` citation, here and in the source comments, points at the design
spec, a maintainer document that is not in this repository. Nothing here
depends on reading it: every rule is stated in full where it is claimed, and
the citation is provenance rather than a pointer to the real answer.

## Reporting a vulnerability

Email **kevin@oneil.xyz**. Please do not open a public issue for a
vulnerability.

You will get an acknowledgement within 3 working days. Fixed issues are
disclosed publicly within **90 days** of the report, sooner if a fix ships
earlier, later only by agreement with the reporter.

Include the invocation and, if you can, the exchange that triggers it. A
recorded request and response is worth more than a description.

## Supported versions

Pre-1.0. Until the v1.0 tag, only the tip of `main` receives fixes.

## Threat model

**Assumed hostile: everything the Chat API sends.** A response is parsed, never
trusted. It cannot redirect a request to another host, name a file outside the
working directory, put an escape sequence in a data column, or turn a bounded
run unbounded.

**Assumed hostile: message content, more than anywhere else in a tool like
this.** A Jira ticket is written by a colleague with an account. A Chat space
can contain anyone the space contains (an external guest, a compromised
account, an app posting on someone's behalf), and every one of them can put
arbitrary bytes in a message body that this tool will read, render, index, and
hand to a model. Message text is treated as data at every point it is touched.

**Assumed hostile: values that flow into a request path.** A space ID, a
message ID, a resolved alias, and an email that came back from a directory
lookup are all strings from somewhere else. None of them may change which URL
is requested. A space is refused unless it matches `^spaces/[A-Za-z0-9_-]+$`
(SPEC.md §15.8), by `chat.CheckSpaceName`, which is the one place that rule
lives. Held today on the two paths that exist: the space a webhook URL names,
and a target given on the command line.
`TestABadURLIsRefusedAtConstruction`, `TestARubbishTargetIsRefusedAsOne`.

That is a check on the value and not the only defence. Anything reaching a
request path also goes through the relative-path rule below, which refuses a
path that would leave the base at all, and `FuzzAPathStaysOnTheBase` states it
as a property rather than as a list of cases. What Milestone 3 owes is the
third path: a resolver turning an alias or an email into a space, whose output
has to go through the same function **(M3)**.

**Assumed trusted: the caller.** Flags, arguments, the config file, and the
profile are the operator's own instructions. `spacebar send "shipping now"`
does what it says. The tool defends the operator from mistakes with a
confirmation gate and a capability check; it does not defend the machine from
its own user.

**Assumed trusted: the credential store, once written.** File modes are
checked; the filesystem is not otherwise second-guessed.

**Out of scope:** side channels, an attacker who already has your token, an
attacker with write access to your config or your `$PATH`, and the contents of
the Chat spaces themselves. If someone can edit `config.json`, they can point
you at their webhook.

## What is not possible, by construction

Absences, not options. There is no flag that enables these and no hook to wire
one to.

- **No cgo.** Pure Go, and the dependency list is chosen to keep it that way:
  SPEC.md §3.1 admits `modernc.org/sqlite` and not any other, for this reason
  alone. `CGO_ENABLED=0` is set on every build and on the licence scan, and
  `make build-all` cross-compiles all six platforms in CI, so a dependency that
  broke it would fail the build rather than the release.
- **No telemetry, no phone-home, no update check.** Ever. The only hosts this
  binary contacts are the Chat API, Google's OAuth endpoints, and a webhook URL
  the operator supplied.
- **Only `internal/chat` speaks HTTP.** Nothing else may import `net/http`, so
  header redaction cannot be bypassed by a package that builds its own client.
  `TestOnlyChatImportsNetHTTP`, which reads the source rather than asking the go
  command, so that a file behind a build tag for another platform is still held
  to it. What it does not cover is a dependency that speaks HTTP on our behalf:
  `golang.org/x/oauth2` will build the token request itself, and that request
  carries a client secret and a refresh token.
- **Only `internal/output` writes to stdout or stderr**, so an escaping rule
  holds everywhere or nowhere. `TestOnlyOutputWritesToTheProcessStreams`, which
  covers `fmt.Println` and the `println` builtin as well as the streams named
  directly, because a stray debugging line is the likely way stdout stops being
  data. Cobra reaches stdout on its own behalf, which is how `internal/cli` is
  meant to write; the golden files are what hold what travels through it.
- **No shell is involved in building a request.** Nothing is interpolated into
  a command line, because there is no command line.

## Processes this tool does start

Stated plainly, because it is the sharpest difference between this tool and a
pure API client, and because a document that only lists absences is not one.

Three child processes exist, and only the first is limited to authentication:

- **A browser, once, during an authorization flow.** The OAuth
  authorization code flow requires a user agent, and out-of-band copy/paste
  redirect is no longer supported by Google (SPEC.md §6.3), so there is no
  flow that avoids this. It is `open`, `xdg-open`, or `rundll32
  url.dll,FileProtocolHandler`, invoked through `os/exec` with the URL as a
  separate argument and never through a shell, so it cannot become a command.
  The argument is refused unless it is an `http` or `https` URL, because the set
  of schemes a desktop will act on includes ones that run things, and refused if
  it starts with a dash, which would be read as a flag.

  If the launch fails the flow does not:
  `TestABrowserThatWillNotLaunchDoesNotFailTheFlow`. That matters on exactly the
  machines this tool is built for. On the development box this was written on
  there is no `xdg-open` at all, so the real failure is `exec.ErrNotFound`
  rather than a non-zero exit, and the URL is printed for the operator to open
  themselves.
- **The macOS keyring helper.** `zalando/go-keyring` calls `/usr/bin/security`
  on darwin, once per credential read or write. On Windows it uses the
  credential API in-process and starts nothing. This happens from Milestone 2,
  not Milestone 3: the webhook URL is a credential and lives in the keyring
  like any other.

  The secret is passed to that helper on stdin rather than in an argument, so
  it does not appear in the process list. It is base64-encoded on the way, and
  `internal/auth` scrubs both the raw and the encoded form out of any keyring
  error it reports, rather than trusting that an upstream change keeps the two
  apart.
- **`dbus-launch`, on Linux, when there is no session bus.** `go-keyring`
  speaks D-Bus in-process when `DBUS_SESSION_BUS_ADDRESS` is set, and when it
  is not, the D-Bus library tries to start a bus by running `dbus-launch`. A
  container, a CI runner, and a headless server all reach that path, and on
  most of them the binary is absent and the call fails in a few milliseconds,
  which is what sends this tool to the fallback credentials file. It fails
  rather than hanging, which matters: a keyring call that blocked would hang
  every invocation on a headless box.

All three inherit the environment, which is where a `SPACEBAR_CLIENT_SECRET`
may live. That is accepted rather than mitigated, because a browser that cannot
see the environment is not a browser the user is already logged into.

## Credentials

A credential reaches `spacebar` from the keyring, from the environment, or from
a fallback file, and never as a flag value: an argument lands in the shell
history, where it outlives the session, and in the process list, where every
other user on the machine can read it while the command runs. Neither is
something the operator can undo afterwards.

`TestNoFlagTakesASecret` walks the whole command tree and fails on any flag
whose name suggests it takes one. The check is on the name rather than on where
the value goes, because the name is what a user types and what documentation
tells them to type: a flag called `--webhook-url` invites a credential on the
command line whatever the code behind it does.

`spacebar profile set-webhook` is the one command that is handed a credential,
and it reads it from stdin or from `SPACEBAR_WEBHOOK_URL`. It has no flag at
all for the URL, which is also why the verb carries the word: a boolean
`--webhook` would have tripped the check above, and the shortest way past a
gate like that is to name it in the verb rather than to add an exception.
`TestTheCredentialReachesNoStreamAndNoFile` asserts that the URL appears in
neither stream and not in the configuration file;
`TestNoURLAnywhereIsAUsageFailureThatSaysWhatToDo` holds the message that says
why there is no flag.

A URL that is refused is never quoted back, including by `url.Parse`, whose own
error repeats what it was given. `TestARejectedURLIsNotQuotedBack`. The refusal
is deliberately eager: a truncated paste is the common way a webhook URL goes
wrong, and it produces a credential that looks complete, so it is caught when
it is pasted rather than as a `400` about an API key days later.

- **`config.json` holds a reference, never a secret** (SPEC.md §5.3). The
  config is meant to be hand-edited and kept in a dotfiles repository. A
  webhook URL is stored as `keyring:spacebar/<profile>/webhook`, and the secret
  lives in the OS keyring. `TestASecretIsRefusedInTheConfigFile` and
  `TestSaveRefusesToWriteASecret`: a field that holds a reference is refused
  both on read and on write when it holds anything else, and the refusal never
  quotes the value back. An unknown key is refused too, because
  `webhook_url` written where `webhook_url_ref` belongs would otherwise be a
  credential in a file, silently ignored by the tool that put it there.

  This is why `client_secret` from SPEC.md §5.1 is written as
  `client_secret_ref`. RFC 8252 is right that a native-app secret is not
  confidential and none of this tool's security rests on it, but a user who
  made a client in their own Cloud project did not agree to keep the secret in
  the file they paste into an issue.
- **Where there is no keyring**, the secret falls back to `credentials.json` at
  mode `0600`, refused on read if it is wider, **warning on stderr every
  invocation** that it did so. `TestAWideFallbackFileIsRefused` and
  `TestFallbackWarnsAndRoundTrips`. Refused rather than warned about, because a
  warning leaves the file exactly as readable as it was and becomes a line the
  operator learns to scroll past. The warning is deduplicated within one run
  and not across runs: a warning printed once is a warning nobody sees.

  A container, a CI runner, and a headless server all lack a keyring, and all
  three are where a script that posts to Chat actually runs. Refusing to work
  without one would exclude the population this tool is built for, so the
  fallback is a supported path rather than a degraded one, and it says plainly
  that the credential is on disk in plain text.
- **A webhook URL is a bearer credential, not a URL.** It carries `key` and
  `token` query parameters that are the entire authentication for posting to
  that space. It is redacted, stored, and refused on the command line exactly
  as a token is. Treating it as an ordinary URL (logging it, printing it in a
  dry run, putting it in an error message) is the most likely way this tool
  leaks a credential, because it does not look like one.
  `TestVerboseOutputCarriesNoCredential`,
  `TestATransportFailureDoesNotQuoteTheURL`,
  `TestAnErrorMessageFromTheServerIsScrubbed`,
  `TestAMalformedBaseIsNotQuotedBack`, and, for `--dry-run`,
  `TestNoDryRunOutputAnywhereCarriesACredential`, which runs every write command
  in the tree in all three renderings and asserts both that the values never
  appear and that the placeholders do. The second half matters: a test that only
  grepped for `key=` would pass on output that leaked, because a correctly
  redacted URL still reads `key=REDACTED`.

  The failure this is arranged around is not hypothetical.
  `net/http` wraps every failure in a `*url.Error` whose `Error` method quotes
  the URL it failed on, query string included, so the default rendering of
  "the server is down" publishes the credential for the space. It is redacted
  where the failure is built, and struck out again by a second pass that knows
  the profile's own secret values by sight, because the first pass can only
  redact what somebody anticipated the shape of.
- **Redaction happens where the request is built, not where it is printed**
  (SPEC.md §15.1). No present or future formatter is ever handed a token,
  including at `--verbose`, where the `Authorization` header prints as
  `REDACTED` rather than being omitted: a missing line says a credential was
  not sent, which is a different answer to the question somebody reading a log
  is asking. `TestVerboseOutputCarriesNoCredential` and
  `TestADryRunShowsTheHeaderAsRedactedRatherThanOmittingIt`.
- **A credential never travels in the clear.** A base URL that is not `https`
  is refused when the client is built, before anything is sent. The one
  exception is a loopback IP literal, which is what a test server is; the name
  `localhost` is not accepted for it, because a name is resolved by whatever
  the machine's resolver says. `TestAPlaintextBaseIsRefused`.
- **A redirect is never followed.** `net/http` strips the `Authorization`
  header across a cross-origin redirect, which protects a bearer token and does
  nothing at all for a webhook credential, because that one is in the query
  string and travels with the URL. So no 3xx is followed anywhere: the Chat API
  does not use them, and one that arrives is reported as the failure it is.
  `TestARedirectIsNotFollowed`.
- **A request path is relative, always**, and is joined onto the base rather
  than substituted for it. An absolute path, a scheme-relative one, or a walk
  up through `..` in a value the far end chose would move the request to
  another host and take the credential with it. Refused by a check on the path
  and again by a check on the built URL, because a parser that accepts what it
  should not is a bug with a long history. `TestAPathCannotLeaveTheBase`,
  `TestSameOriginIsCheckedAfterTheJoin`, `TestASpaceCannotRedirectTheRequest`,
  and `FuzzAPathStaysOnTheBase`, which states it as a property rather than as
  the list of cases somebody thought of.
- **Nothing ever blocks on input.** A command that would read from a terminal
  when stdin is not one refuses and exits `7` instead. A CLI that blocks on a
  prompt inside a pipeline hangs whatever is driving it, and a hung agent is
  strictly worse than a failed one: a failure gets reported, a hang gets a
  timeout somebody has to go and find.
  `TestConfirmRefusesWhenThereIsNobodyToAsk` also asserts that stdin is not
  read at all in that case, because reading it is the bug: even a stream with
  an answer in it is a pipeline the command stole a line from.
  `TestRemovingAProfileNeedsAnAnswer` holds it through the command tree, and
  `TestConfirmDefaultsToNo` holds the other half, that anything which is not an
  explicit yes is a no.

  The check is applied to the stream the command actually reads from rather
  than to `os.Stdin`, which is the same file in production and not under test:
  a prompt rule verified against a stream nothing reads is not verified.
  One known imprecision, documented at `output.Interactive` rather than fixed:
  `os.ModeCharDevice`, which SPEC.md §11.3 specifies, is set for `/dev/null` as
  much as for a terminal, so `command < /dev/null` is treated as interactive.
  Telling them apart needs a termios call and therefore a dependency, and the
  outcome is the same either way: the question is asked, nothing answers it,
  and the command exits `7`.

## Authentication

- **Authorization code with PKCE, loopback redirect, and nothing else.** The
  verifier is 64 bytes from `crypto/rand`; the challenge is `S256`. Out-of-band
  redirect is not implemented, because Google no longer supports it and a
  fallback nobody can use is a fallback nobody tests.
  `TestTheChallengeIsTheHashOfTheVerifier` computes the challenge from RFC 7636
  §4.2 rather than by calling the function under test, because a round trip
  through one implementation agrees with itself whatever it does.
  `TestTheAuthorizationURLCarriesWhatTheFlowDependsOn` checks that the challenge
  on the consent URL and the verifier in the exchange are the same pair, which
  is the whole of PKCE, and
  `TestTheStateAndVerifierAreDifferentEveryTime` that neither repeats.
- **The listener binds `127.0.0.1` explicitly**, never `0.0.0.0` and never
  `localhost`. `localhost` resolves through DNS and is therefore hijackable;
  the loopback literal is not (SPEC.md §6.3, §15.4).
  `TestTheListenerIsOnLoopbackAndNowhereElse` parses the bound address and
  fails on a name as well as on a routable one.
- **`state` is 32 random bytes and is checked**, in constant time, before
  anything else about the callback is looked at. A callback whose state does
  not match is answered with a 404 that explains nothing, because whoever sent
  it is not the person who started the flow.
  `TestAMismatchedStateIsNotAnswered` and
  `TestACallbackWithTheWrongStateIsRejected`, which also asserts that no code
  from such a callback is exchanged.
- **The whole flow times out at 180 seconds**, and the listener shuts down
  within 2 seconds of the callback. A loopback listener left open is a loopback
  listener something else can talk to.
  `TestTheListenerIsClosedAfterTheCallback` dials the port afterwards, and
  `TestAFlowThatNobodyAnswersTimesOutRatherThanHanging` covers the tab somebody
  closed.

  The flow timeout is not `--timeout` and neither bounds the other. One is the
  budget for an HTTP attempt, measured in seconds because a request that takes
  longer has failed; the other is the budget for somebody to read a consent
  screen and decide.
- **The token request goes out through a client this repository configured.**
  `golang.org/x/oauth2` builds that request itself, carrying the client secret
  and either an authorization code or a refresh token, and the gate that holds
  every other request to one package cannot see it. What is done about that is
  a construction rather than a gate: `chat.TokenHTTPClient` follows no
  redirect, because a 3xx on a token request resends the POST and the POST body
  is the credential.
- **An OAuth error is read for its code and then dropped.**
  `oauth2.RetrieveError` carries the whole response body and the
  `http.Response` beside it. Its `Error` method is careful, printing only the
  error code when there is one, but anything formatting the value with `%+v`
  would print the body, and on a token endpoint that body is an access token
  and a refresh token. `TestATokenResponseNeverReachesTheError` sends a
  response that names an error and carries tokens, and asserts neither reaches
  the failure message.
- **The OAuth client is not in the source tree.** `internal/meta.DefaultClientID`
  and `DefaultClientSecret` are empty, and release builds inject them from CI
  secrets via `EXTRA_LDFLAGS`. **This is not a security measure**, and should
  not be read as one: RFC 8252 is explicit that a native-app client secret is
  not confidential. It is a quota and reputation measure. A client committed to
  an Apache-2.0 repository is a client every fork uses, which means forks spend
  our quota, their users see our consent screen, and one abusive fork can get
  the client suspended for everybody (SPEC.md §6.1).
- **Bring-your-own client is a first-class path, not a fallback.** An
  Internal-type OAuth client in the org's own Cloud project avoids third-party
  app access controls *and* the seven-day refresh-token expiry that External +
  Testing imposes (SPEC.md §6.2). A refusal carrying `admin_policy_enforced` is
  the exact case it exists for, and says so:
  `TestAnAdminPolicyRefusalNamesTheWayOut`.
- **An expired authorization is not an error message, it is exit code 4.**
  `invalid_grant` during refresh is reported as "your authorization has expired
  (this is normal for apps in testing mode)", never as a raw OAuth error, so
  that a script can tell it apart from a failure worth investigating.
  `TestInvalidGrantIsExplainedRatherThanQuoted`, which also asserts the raw
  library error is not what gets printed. Held on the authorization exchange
  today; the same mapping on a token refresh belongs to the card that adds
  refreshing **(M3)**.

## Input this tool does not trust

- **Message text is data, everywhere it goes.** It is not interpreted as
  markup, not evaluated, and not re-escaped into a context it did not come
  from.
- **A data column never carries an escape sequence.** Message bodies come from
  people this operator may not know, and a terminal is a program that
  interprets bytes: an OSC sequence in a message body could set a window title,
  move a cursor, or in some terminals prime the input buffer. Nothing this tool
  prints to a data column can do any of those. `output.Sanitize` and
  `output.Cell`, held by `TestSanitize`, `TestADataColumnCannotDriveTheTerminal`,
  and `FuzzSanitize`.

  Beyond the control characters: the C1 range at U+0080 to U+009F, because
  U+009B is a second encoding of the control sequence introducer that a
  terminal in eight-bit mode acts on; and the bidirectional overrides and
  isolates, because they let a string display as something other than what it
  contains, which in a tool that prints message bodies beside space names is a
  way to make one look like another.

  `Cell` escapes tab and newline as well. A list is one record per line with
  tab between columns, so a body containing either could otherwise forge a
  column or a whole row in something that is parsing the output.

  Colour is the only escape sequence written on purpose, from constants in
  `internal/output`, applied to labels and never to a value. `TestColourIsNeverDataDerived`.

  **`--json` output is deliberately not sanitized.** `encoding/json` already
  writes every character below U+0020 as an escape, so no control sequence
  survives into the bytes, and rewriting anything else would break the rule
  that a value is never silently altered. `--json` is read by programs, and it
  hands them what was actually there. `TestJSONDoesNotAlterAValue`.
- **Chat markup is generated, never concatenated** (SPEC.md §9).
  `internal/format` owns the only place a link or a mention is built.
  `TestALinkCannotBeClosedByItsOwnText`, `TestMentionOnlyAcceptsAResolvedName`,
  and `FuzzTranslate`. The package is at 100% statement coverage, which the
  spec asks for because this is where a bug would actually appear.

  **A wrapper character is refused, not escaped**, which is a correction to
  what this document used to say. Chat has no escape syntax: there is no way to
  write a pipe inside the display half of `<url|text>` such that the far end
  reads it as a pipe. So a link whose text contains `<`, `>`, or `|` cannot be
  represented, and the whole message is refused at exit 2 naming the character
  and its offset. The alternative was altering the text to fit, and a reader at
  the other end cannot tell an altered message from an intended one.

  A user resource name reaching a mention is checked against the same standard
  SPEC.md §15.8 sets for a space name, rather than escaped: anything a parser
  accepts is safe inside a wrapper unescaped. Escaping is the second layer and
  never the only one.
- **Nothing inside a fenced or inline code span is transformed.**
  `TestNothingInsideACodeSpanIsTransformed`, fifteen cases. Somebody pasting a
  shell command or a diff is what code spans are for, and a translator that
  worked by substitution would rewrite the inside of one.
- **A value is never altered to make it representable.** Invalid UTF-8 is
  refused, not replaced with U+FFFD. A message that is not what was sent is a
  wrong answer that looks like a right one. `TestInvalidUTF8IsRefused`, exit 2,
  naming the byte offset.
- **Cache and state paths stay under their roots.** A space ID that reached a
  filename could otherwise name a file anywhere **(M4)**.

## Refusing, confirming, and the MCP surface

The MCP server is the part of this tool where a mistake is least visible: a
model decides, a tool runs, and a message appears in a space that colleagues
read. It is gated more tightly than the CLI, on purpose.

- **Writes are off by default.** `spacebar mcp` registers no write tool unless
  `--allow-write` is passed **(M5)**.
- **A tool whose capability is unavailable is not registered at all**, rather
  than registered and returning an error (SPEC.md §14.1). A model that cannot
  see a tool cannot argue itself into calling it, and a model that can see one
  will eventually try. This is also why the capability check happens before the
  network call rather than after: a refusal that arrives after the POST carries
  the same error code as one that arrives before it **(M5)**.
- **`--allow-space` restricts writes to an allowlist** of spaces, so that an
  agent with write access to a scratch space does not have write access to the
  company-wide announcements space **(M5)**.
- **Every write tool description ends with the confirmation requirement**, and
  every tool call is logged to stderr as one JSON line for auditability
  **(M5)**.
- **A webhook posts to one space and there is no version of it that posts
  anywhere else.** The space is derived from the URL rather than configured
  beside it, so the two cannot disagree about where a message goes, and a target
  naming a different space is exit 2 before any request.
  `TestAnotherSpaceIsRefusedBeforeTheRequest`. Sending anyway would mean
  somebody who typed the wrong target watched their message arrive in a space
  full of people, with a success code saying it went where they asked.
- **`--dry-run` cannot send, and that is enforced where the request would be
  sent rather than at each command.** The client stops on the line before the
  send, after the URL has been resolved and checked, the body encoded and the
  credential attached, and returns that request instead of making it. So what a
  dry run prints is the request that would have gone rather than a second
  description of one, which can drift from it silently.

  A command still has to render what comes back, and forgetting to is the one
  thing that could go wrong. `TestEveryCommandIsClassifiedAsWritingOrNot` walks
  the command tree and fails when a command is in neither the writing nor the
  read-only list, so a command added in a later milestone cannot be merged
  without somebody deciding in writing whether it can put something into a
  space; `TestEveryWriteCommandHonoursDryRun` then runs each writing one against
  a server that fails the test if it is ever reached. Verified by planting a new
  command and watching the first fail.

  A dry run of a command that writes locally means the whole command.
  `spacebar profile set-webhook --dry-run` stores nothing, because the other
  reading, where it saves the credential and only declines to send, is a
  `--dry-run` that wrote to disk. `TestADryRunOfSetupStoresNothing`.
- **A capability the profile does not have is exit code 5, before any request.**
  A write-only webhook profile cannot read, and `spacebar tail` on one fails
  naming both the profile and the fix rather than sending anything (SPEC.md
  §8.2). `TestARefusalMakesNoNetworkCall` counts requests rather than reading
  the error, because a refusal that arrives after the POST returns the same
  exit code and the same message as one that arrives before it, and the
  difference between them is a message somebody's colleagues can see.

  The check is on the path to the network rather than beside it. Only
  `internal/chat` may build a request, only a transport may build a client, and
  a transport refuses before it reaches its own client, so a command cannot
  bypass the check by forgetting to ask for it.
  `TestOnlyATransportBuildsAChatClient` holds the middle step, which is the one
  that is otherwise only a convention: `internal/cli` may import
  `internal/chat`, and nothing else would stop it sending directly.
- **Confirmation that cannot be asked for is refused, not skipped.** Exit code
  7, `output.ExitRefused`.
- **A send is never replayed after an upstream error.** A `POST` that received
  a 503 may well have been carried out, with only the acknowledgement lost, and
  nothing in the response distinguishes that from a message that never arrived.
  Retrying it is how one `send` becomes two messages in a space full of people,
  which is a message the operator did not ask for. Three cases permit a replay
  and no others: a 429, which is a refusal issued before the request was
  processed; a 401, likewise; and a failure at the dial stage, where no request
  byte reached the server. A caller who supplies a message ID opts back in,
  because the API will then refuse the duplicate itself.
  `TestAPostIsNotReplayedAfterAnUpstreamError` asserts it by counting requests
  rather than by reading the error, because a refusal that arrives after the
  POST returns the same error as one that arrives before it.
  `TestAPostWithAMessageIDIsReplayed`, `TestA429IsRetriedEvenOnAPost`,
  `TestA403IsNotRetried`, and `TestSafeToReplay` hold the rest of the table.

## Doing what was asked, and only that

- **A thread key that would be silently ignored is not sent silently.** The
  Chat API's default `messageReplyOption` is documented as "Starts a new thread.
  Using this option ignores any thread ID or threadKey that's included", so a
  caller who asks to group a message into a thread and says nothing else gets a
  new thread every time, with a `200` and no indication that what they asked for
  did not happen. Supplying a thread key is read as a request to thread, and the
  option that threads is what gets sent.
  `TestAThreadKeyAloneStillThreads`. This is in the security document rather
  than only in the release notes for the same reason truncation is: the failure
  is silent by nature, and a job that believes it is appending to one thread is
  making a different decision from one that knows it is not.
- **A verification message is never sent without being asked for.**
  `spacebar profile set-webhook --verify` posts to a real space that other
  people read, so it is off by default and its text is the caller's to choose.
  `TestWithoutVerifyNothingIsPosted`. The credential is stored before the check
  rather than after, so a space with Chat apps switched off costs somebody a
  failed verification and not their configuration:
  `TestAFailedVerifyStillLeavesTheProfileConfigured`.

## Truncation is a security property

A result set cut short is never reported as complete. In `--json` mode a list
is NDJSON and a truncated one carries an explicit marker plus a token to resume
from; in text mode it is a structured stderr warning and a non-zero exit. The
reason this is in the security document rather than only in the output contract
is that the failure is silent by nature: a nightly job that reads fifty
messages as the whole conversation makes decisions on a subset and reports
success, and nothing downstream can tell **(M4)**.

`stdout` is data and nothing else. A failing command writes nothing at all to
it, so a partially written document can never be parsed as a whole one. Held
today by `internal/cli.TestFailureWritesNothingToStdout`, and by the golden
files under `internal/cli/testdata/golden/`, which record which stream every
byte went to.

A response body is bounded, and one that exceeds the bound fails rather than
being parsed short. A document decoded from a truncated body is the same
failure as a truncated list: an answer that looks complete and is not. It is
also how a hostile server makes a send expensive, so the failure is permanent
rather than retried. `TestAnOversizeResponseIsRefusedRatherThanTruncated`.

## Supply chain

- **One direct dependency today**, four more permitted by SPEC.md §3.1 and no
  others, listed with their licences in [NOTICE](NOTICE). Nothing is vendored;
  `go.sum` pins every module.
- **The licence allowlist is a build gate.** `make license-check` runs
  `go-licenses` over all six release platforms and fails on anything outside
  the allowlist, transitively. It runs across platforms because
  `inconshreveable/mousetrap` is behind `//go:build windows` and a scan on one
  machine does not see it, which is also how a forbidden licence would enter
  unnoticed. `internal/lint/notice_test.go` holds NOTICE to the generated list.
- **`make vuln` runs govulncheck and fails closed.** It is part of `make ci`.
- **Tool versions are pinned in one place.** A floating linter can fail a pull
  request that changed nothing, and can fail a release, because the release
  gate re-runs `make ci` on a tag that cannot be moved.
  `internal/lint/toolversion_test.go` holds the one version that has to be
  written twice to the one that does not.
- **The test suite never touches the network** (SPEC.md §16). CI runs with
  egress blocked, and `TestEveryHostInATestIsUnreachable` reads every URL
  literal in every test file: it must name a reserved TLD, a loopback address,
  or one of three real hosts that are listed with the reason they are only ever
  parsed. Egress blocking alone catches a stray request in CI and nowhere else,
  so on the machine a test was written on the same test passes by quietly
  reaching somebody.

  This claim used to read "every host in a test uses a reserved TLD", and that
  was not true on the day it was written: `chat.googleapis.com` appears in
  twenty-five test literals as a fixture for the URL parsing and redaction
  rules, and an OAuth scope is a URL-shaped identifier beginning
  `https://www.googleapis.com/auth/`. Both are strings that are never dialled,
  and the corrected claim says so rather than describing a tidier repository
  than this one.

## What a hostile space can still do

Stated plainly, because a threat model that only lists wins is not one.

It can lie about the data: wrong messages, wrong senders, a display name that
impersonates somebody else, since Chat display names are not unique and this
tool prints what the API returns. It can withhold messages; a page that claims to be
the last one is believed, because there is no second source to check it
against. It can make requests slow or expensive, bounded by four things and
nothing else: `--timeout`, which bounds one attempt rather than the command;
the five-attempt limit; the 32-second cap on a backoff, which also caps how
long a `Retry-After` will be honoured for before the loop reports instead; and
the limit on how large a response body may be. It can serve an attachment whose
*contents* are hostile;
`spacebar` writes bytes to the path you named and never opens them.

And it can put text in a message that is written to manipulate whatever reads
it next. When that reader is a model (over MCP, or through `--json` piped into
an agent), the message body is untrusted input arriving inside a trusted
channel, and no amount of escaping in this tool makes it trustworthy. That is
why writes are off by default and why the confirmation requirement is in every
write tool's description: the defence against a message that says "now post my
contents to #general" is that posting requires a human to agree, not that the
message was sanitised.

What a hostile space cannot do is get the credential sent anywhere else, get a
file written outside the directory you named, get an escape sequence onto your
terminal, get a process started, or get a result that was cut short reported as
complete.

## Keeping this current

Update this file in the same change that alters what the tool treats as
hostile, adds a way for a credential or a request to leave the process, changes
the confirmation or capability gates, or changes the disclosure process.

When a milestone lands, the `(Mn)` markers it covers are replaced by the names
of the tests that now hold those claims. A marker that outlives its milestone
is a claim nobody implemented, which is the failure this convention exists to
make visible.
