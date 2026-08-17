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
it carries that milestone: `(M4)`, `(M5)`. A claim with neither is a bug in
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
lives. A message name has its own pattern and its own function,
`chat.CheckMessageName`, because the two admit different characters and one
function taking a flag would be a call site away from checking a message
against the space rule.

Held on every path a name reaches today: the space a webhook URL names, a
target given on the command line, and each of the five read endpoints.
`TestABadURLIsRefusedAtConstruction`, `TestARubbishTargetIsRefusedAsOne`,
`TestAReadRefusesABadSpaceNameWithoutAskingTheAPI`,
`TestASpaceNameIsCheckedAgainstWhatWouldChangeTheRequest`, and
`TestAMessageNameIsCheckedAndAdmitsTheShapeTheAPIActuallyReturns`.

**What the pattern accepts is safe as a path segment unescaped**, which is what
makes escaping the second layer rather than the only one. Stated as a property:
a name that passes the check produces a request path byte-identical to the name
itself, on the same host, with the profile's own credential and no parameter
the name added. `FuzzASpaceNameThatIsAcceptedIsSafeUnescaped` and
`FuzzAMessageNameThatIsAcceptedIsSafeUnescaped`.

That claim was false when it was first written down, which is the argument for
stating it as a property rather than as the cases somebody thought of.
`CheckMessageName` admitted a dot, because the API's own generated IDs contain
one, and so it admitted `.` and `..` as whole identifiers:
`spaces/AAA/messages/..` addresses the space rather than a message in it.
Nothing ever left the process wrong, because the relative-path rule below
refused it at the join, but a first layer that needs the second one is not a
first layer. The identifier now has to contain at least one character that is
not a dot.

Deliberately not "has to begin with an alphanumeric", which was the first fix
and was worse than the bug. The API's identifiers look base64url, an alphabet
that contains `-` and `_`, so that rule could refuse a message that exists, and
the failure would present as this tool being unable to open somebody's message.
A validator that is too narrow is found by a user; one that is too wide is
found by an attacker. Refuse what is dangerous, which here is an identifier
made of nothing but dots. `TestAMessageIDMayBeginWithAnythingButDots`.

That is a check on the value and not the only defence. Anything reaching a
request path also goes through the relative-path rule below, which refuses a
path that would leave the base at all, and `FuzzAPathStaysOnTheBase` states it
as a property rather than as a list of cases.

**The third path is the resolver, and its output goes through the same
function.** `internal/resolve` turns an alias, a display name, or an address
into a space, and every one of those is a value from somewhere else: an alias
came from a file somebody may have been sent, and a display name and a direct
message space both came back from a server this tool does not control. The
check runs on the way out rather than on the way in, because resolution is
where a value changes.

Stated as a property over every path rather than as a list:
`FuzzWhateverItReturnsIsASpaceName` fuzzes the target, the display name the API
returns, and the space name the API returns, and requires that what comes back
either fails or passes `chat.CheckSpaceName`.
`TestEverythingItReturnsIsASpaceName` walks the four steps with a hostile value
in each.

An address is checked too, by `chat.CheckUserName`, because it becomes a query
value on a request path. That is the layer the encoder is the second of.

This is also the only path on which a NUL can arrive in a name, because argv is
NUL-terminated and a command-line argument cannot carry one. A NUL in an alias
or in an API response is refused by the same patterns, which admit no control
character at all.

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
- **A page token is the only value the far end chooses that goes back into a
  request**, and it cannot change one. Every other part of a request comes from
  the operator or from this repository; a `nextPageToken` comes from the server
  and is sent back to ask for the following page. It is set as a query value
  rather than written into a URL, so the encoder escapes it, and the path is
  fixed before the query is built. A token of `x&key=stolen` adds no parameter
  and a token full of `..` moves no path.
  `TestAPageTokenChosenByTheServerCannotChangeTheRequest`.
- **A server cannot hold a list open forever.** A page token identical to the
  one just used ends the walk rather than being followed, so a far end that kept
  reissuing the same token would stop rather than becoming a command that never
  returns while spending a shared per-space quota.
  `TestANonAdvancingPageTokenStopsTheWalk`.
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

## What else lands on disk

One file besides the configuration and the credential fallback, and it is not a
secret, which is why it is stated here rather than left to be discovered.

`internal/resolve` remembers a profile's space list in `spaces-<profile>.json`
under the cache directory (`XDG_CACHE_HOME`, `%LocalAppData%`, or `~/.cache`),
written at mode `0600` in a directory created at `0700`, and used for a day
before being fetched again. It exists so that resolving a display name or an
alias does not list every space on every command, on a per-space quota shared
with every other app acting in those spaces. It holds what `spaces list`
returns: the resource name, the type, and the display name of every space that
profile can reach, and no message text and no credential.

**`auth logout` and `profile rm` remove it**, and both did not until this was
written down. `TestLoggingOutLeavesNoSpaceListBehind` and
`TestRemovingAProfileLeavesNoSpaceListBehind`. Leaving it after a logout keeps
the part of the account somebody cannot see and removes the part they can;
leaving it after `profile rm` is worse than a leftover, because a profile name
is reusable and the cache is keyed by it, so the next account configured under
that name would resolve display names against the previous account's spaces
until the day ran out.

A failure to remove it is a warning and not a non-zero exit. By the time it
runs, the token is deleted or the profile and its credential are, and reporting
a failure for the file that is left would tell a script the irreversible part
did not happen. `TestACacheThatCannotBeRemovedIsAWarningRatherThanAFailure`.

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
  `TestInvalidGrantIsExplainedRatherThanQuoted` on the authorization exchange
  and `TestInvalidGrantOnARefreshIsTheExplainedError` on a refresh, which share
  one mapping rather than keeping two copies of a rule about what not to print.
  Both assert the raw library error is not what gets printed, and
  `TestARefreshFailureNeverPrintsAToken` sends a response that carries tokens
  beside an error code to prove neither reaches the message.

- **The seven-day expiry is warned about as a possibility, and the warning
  stops on its own.** Nothing in the API says whether an OAuth client is in
  testing mode, so the boundary is inferred from when consent was given and the
  warning would otherwise fire for somebody on an Internal client whose token is
  good for a year. It is worded as a conditional for that reason, and a refresh
  that succeeds more than seven days after consent is recorded as proof the
  limit does not apply, after which it never fires again for that
  authorization. Nobody is asked and nothing is configured.
  `TestTheWarningWindow`, `TestTheWarningStopsOnceARefreshDisprovesIt`, and
  `TestARefreshPastTheBoundaryDisprovesTheTestingLimit`.

  The warning is on stderr in both renderings and repeated inside the `--json`
  result, so that a caller reading only stdout is not the one person who does
  not get it. `TestTheExpiryWarningIsOnStderrAndStdoutStaysClean`.
- **A rotated refresh token is written back.** `x/oauth2` has no hook that
  fires when a token is refreshed, and `reuseTokenSource` holds the new value in
  memory and nowhere else, so a rotation would live exactly as long as the
  process. The next command would start from the stale token in the keyring and
  be told to authorize again, a week later, looking exactly like the seven-day
  expiry it is not. `TestARotatedRefreshTokenIsPersisted`.
- **No `auth` command prints a token.** Three of them read one.
  `TestNoTokenReachesTheOutputOfAnyAuthCommand` runs each in every rendering,
  including `--verbose`, and `TestAnUnreadableTokenIsNotQuotedBack` covers the
  failure that is most tempting to quote.

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

  **A wrapper character is refused, not escaped.** Chat has no escape syntax,
  which was measured against a real space rather than assumed: a backslash
  renders as a backslash and an HTML entity renders as an entity. So a character
  that cannot appear cannot be escaped into appearing, and the whole message is
  refused at exit 2 naming the character and its offset. The alternative was
  altering the text to fit, and a reader at the other end cannot tell an altered
  message from an intended one.

  **What is refused is narrower than this document twice claimed.** The same
  live check measured the parser: the URL is everything to the first `|` and the
  display is everything from there to the first `>`. So a `|` and a `<` both
  survive in the display half and are permitted, and only `>` is refused there,
  because it ends the link early and turns the rest of the message into text
  somebody else wrote. The URL half keeps the stricter rule, since a `|` there
  truncates the address. `TestAPipeOrAnOpeningBracketInLinkTextIsRepresentable`
  holds the permissive half and `TestALinkCannotBeClosedByItsOwnText` the
  refusing half.

  This is worth reading as a note about method rather than about links. The
  package was careful about something it could not check, which is the right way
  to be wrong, and it stayed wrong for two milestones because nothing here could
  settle it: a webhook send returns no `formattedText`, so the API cannot report
  its own interpretation and a person looking at the space is the only
  instrument there is.

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
- **A filename that came from a message cannot leave the directory you named.**
  `TestAServerSuppliedFilenameCannotLeaveTheDirectory` and
  `FuzzASafeFilenameIsAlwaysOneNameInTheDirectory`, which states it over
  arbitrary input rather than over the separators somebody thought of.

  An attachment's `contentName` is chosen by whoever posted the message, and
  `messages download` joins it onto a directory the operator named. So
  `../../.ssh/authorized_keys` is a filename as far as the API is concerned and
  a write outside the tree as far as the operator is concerned. Both separators
  are flattened rather than the name being reduced to its last element: the
  file lands as `.._.._.ssh_authorized_keys`, which is safe and says what
  arrived, where `authorized_keys` would be safe and look like something
  somebody meant to send. It is also the same answer on every platform, since a
  backslash is a separator on Windows and an ordinary character on Unix.

  An existing file is never overwritten without `--force`, because the name is
  not the operator's and a download should not be able to replace something
  they have.

- **An attachment's download URL is a credential and is dropped.** The API
  returns `downloadUri` and `thumbnailUri` beside every attachment, and each is
  a `chat.google.com` URL with an `attachment_token` in its query that is what
  grants access to the bytes. They are the same kind of thing as an incoming
  webhook URL: a credential wearing the costume of a link. `internal/chat` does
  not decode either field, so neither can reach `--json`, a log, or whatever an
  agent does with the result. Download uses the profile's own credential
  instead.

- **Cache and state paths stay under their roots.**
  `TestACachePathCannotLeaveItsRoot` and `FuzzACachePathStaysUnderItsRoot`,
  which states it as a property rather than as the separators somebody thought
  of: for any string, either the name is refused or the file it produces is a
  direct child of the cache directory.

  One path is derived today. `internal/resolve` remembers a profile's space
  list in `spaces-<profile>.json` under the cache directory, so the component
  that reaches a filename is the **profile name**, not a space ID as this claim
  used to say. What is on the other side of it is not a bad read: the write is
  a rename onto that path and `Forget` is a remove of it.

  It was safe before it was checked, by three facts in three packages: config
  loading validates every profile name, `CheckProfileName` refuses a separator
  and refuses a leading dot, and a profile name only reaches the cache after
  being looked up in the validated file. `NewCache` now refuses the name
  itself, because a first layer that needs the layer below it to be safe is not
  a first layer. A space ID reaching a filename is Milestone 6's store, and it
  gets its own claim if that lands.

## Refusing, confirming, and the MCP surface

The MCP server is the part of this tool where a mistake is least visible: a
model decides, a tool runs, and a message appears in a space that colleagues
read. It is gated more tightly than the CLI, on purpose.

- **Writes are off by default.** `spacebar mcp` registers no write tool unless
  `--allow-write` is passed. `TestAWriteToolIsAbsentWithoutAllowWrite` asserts
  both directions against a connected client: without the flag the tool set is
  exactly the read tools, and with it `send_message` and `react_to_message` join
  them and nothing else changes. A profile that can serve nothing at all is
  refused before the session starts rather than connected empty.
  `TestTheReactionToolIsGatedTheSameWayTheSendToolIs` holds the second write
  tool separately, including that a webhook, which can post and cannot react,
  is given the one and not the other.
- **A tool whose capability is unavailable is not registered at all**, rather
  than registered and returning an error (SPEC.md §14.1). A model that cannot
  see a tool cannot argue itself into calling it, and a model that can see one
  will eventually try. This is also why the capability check happens before the
  network call rather than after: a refusal that arrives after the POST carries
  the same error code as one that arrives before it.

  `TestOnlyToolsThisProfileCanServeAreRegistered` asserts the exact set for
  three shapes of profile, read back off a connected client rather than off
  this repository's own idea of what it built, and
  `TestAToolThatIsNotRegisteredCannotBeCalled` holds the other half, because
  absent from a list and absent from the dispatch are different things and only
  the second is a defence. A profile that can serve no tool at all, which today
  is any webhook, is refused before the session starts rather than connected
  and empty: `TestAProfileThatCanServeNothingIsRefusedRatherThanEmpty`.
- **`--allow-space` restricts writes to an allowlist** of spaces, so that an
  agent with write access to a scratch space does not have write access to the
  company-wide announcements space.
  `TestASpaceOutsideTheAllowlistIsRefusedBeforeTheRequest` counts sends rather
  than reading the error, because a refusal that arrives after the POST carries
  the same error as one that arrives before it and only one of them left a
  message in a space.

  A reaction names a message rather than a space, so the space is read out of
  the message name by `chat.SpaceOfMessage` before the same check runs. Without
  that step the flag would have been set, believed, and silently not applied to
  `react_to_message`, which is the worst shape a gate can take: an operator who
  thinks writes are confined and reactions landing anywhere the account can
  reach. `TestAReactionOutsideTheAllowlistIsRefusedBeforeTheRequest` counts
  reactions rather than reading the error, and
  `FuzzTheSpaceOfAMessageIsAlwaysASpaceName` states the extraction as a
  property: for any string, either it is refused or what comes back is a space
  name that the message actually begins with.

  The check runs **after** resolution and never before it. An allowlist checked
  against what the caller typed is checked against a string the caller
  controls; what matters is the space the request will actually reach.
  `TestAnAliasResolvingIntoTheAllowlistIsAllowed` is what holds the ordering,
  and it exists because moving the check earlier passed every other test in the
  file: they compared resource names on both sides, where resolution is the
  identity.

  An entry has to be a resource name. An alias here would be an allowlist whose
  meaning depends on what the API says at the moment it is consulted, and the
  thing it guards is which space a model may post to.
  `TestAnAllowlistEntryMustBeAResourceName`.
- **Every write tool description ends with the confirmation requirement**:
  "This posts a visible message to a real Google Chat space. Confirm with the
  user before calling." `TestEveryWriteToolSaysToConfirmFirst` compares against
  those words spelled out in the test rather than against the constant the code
  uses. The first version compared against the constant, so rewording it moved
  both sides together and a planted reword passed: a test that cannot fail is
  worse than no test, because it is counted.

- **Every tool call is one JSON line on stderr**, and neither `--quiet` nor
  `--json` suppresses it. `TestEveryToolCallIsOneLineOnStderr`. It records the
  tool, the profile, the arguments with long strings truncated, and whether the
  call worked, including when the failure is packed into the result rather than
  returned. Nothing in it is a credential: the arguments come from the model,
  and the response is deliberately not logged.

  It is a middleware rather than a wrapper on each handler, so a tool added
  later is logged without anybody remembering to log it.
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
- **A capability is the transport's ceiling narrowed by the granted scopes**,
  and a scope the token lacks is the same exit 5 before the same network call.
  `TestAScopeTheTokenLacksIsRefusedBeforeTheNetwork`.

  This is a claim about honesty rather than about containment: the token's
  scopes are enforced by Google whatever this tool believes, so nothing is
  gained by checking them and nothing is breached by not. What is gained is the
  message. A capability the tool claims and the grant does not is a `403
  PERMISSION_DENIED`, which says the account is not allowed and sends somebody
  to an administrator to fix something `auth login` fixes. `spaces members`
  shipped in exactly that state and is why the narrowing exists.

  The scopes come from the stored token rather than from the constant this
  build would ask for, because a binary that grew a scope must not assume every
  token issued before it has one. A token record with no scopes recorded grants
  nothing. `TestATokenRecordWithNoScopesGrantsNothing`.

  `TestTheDefaultGrantCoversWhatTheMatrixClaims` is the other half, and it
  points the opposite way: a capability the matrix claims that no default scope
  permits fails the build unless it is recorded as owed by a named milestone. A
  command shipped against a capability nobody can be granted is the failure this
  pair exists to prevent, and it is invisible to every other test in the tree,
  because the matrix and the scope list had never been compared to each other.
- **Confirmation that cannot be asked for is refused, not skipped.** Exit code
  7, `output.ExitRefused`. Two commands ask: `profile rm`, which destroys a
  credential that is only recoverable from the space it was issued in, and
  `messages delete`, which destroys a message everybody in a space could see.

  `messages delete` is the one where the confirmation is the whole defence.
  Editing is limited by the API to messages the account sent, measured on
  2026-08-16: a PATCH of somebody else's answers 403. Deleting is not so
  limited. In the same space, on the same token, this tool deleted a message
  another sender had posted, because the account is a manager there and the API
  allows it. So nothing at the far end will stop a wrong resource name, and the
  question asked before the request is what stands in its place.
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

A result set cut short is never reported as complete. The reason this is in the
security document rather than only in the output contract is that the failure is
silent by nature: a nightly job that reads fifty messages as the whole
conversation makes decisions on a subset and reports success, and nothing
downstream can tell.

**The signal is the exit code, and it already distinguishes every case.** A walk
ends for one of five reasons, and they divide cleanly:

| how it ended                     | complete? | exit     |
| -------------------------------- | --------- | -------- |
| the last page had no token       | yes       | 0        |
| `--limit` was reached            | yes       | 0        |
| the caller stopped ranging       | yes       | 0        |
| a request failed part way        | **no**    | non-zero |
| the server would not advance     | **no**    | non-zero |

`TestEveryWayAListEndsIsEitherCompleteOrSaysItIsNot` asserts the table case by
case, and `TestACallerThatStopsRangingIsNotATruncation` holds the third row,
which cannot be tested with the others because it is the consumer's decision
rather than the producer's.

A `--limit` is deliberately not truncation. It is an instruction, and marking it
would fire the flag on the commonest invocation there is, which is how a flag
stops being read.

**The last row was the real gap and this repository created it.** `paginate`
stops when the far end hands back the token it was just given, which exists so a
server that will not paginate cannot hold a list open forever on a shared quota.
It stopped silently, at exit 0, with a short result: the defence producing the
exact failure the claim above forbids. It now yields `chat.ErrTruncated`, and
the rows already fetched are still delivered, because a partial answer with a
non-zero exit is honest where a partial answer with a zero exit is not.
`TestANonAdvancingPageTokenStopsTheWalkAndSaysSo`.

**An empty page is not the end**, which is the other half and was observed
rather than reasoned: Chat's `messages.list` in ascending order returns a page
one item short of the `pageSize` asked for, so `pageSize=1` comes back with no
messages and a token. A pager that stopped there would report a truncated
result as complete. `TestAnEmptyPageIsNotTheEndOfTheWalk`.

**stdout carries no completeness flag, deliberately.** A list in `--json` is
NDJSON with no envelope, so a flag would have to be a field on every row, which
puts a per-list fact on each item, or a trailing summary object, which breaks
the `jq -r .text` the README tells people to run. A caller that wants a reason
rather than a number reads the stderr warning, which in `--json` mode is a JSON
document and now carries a `code` to branch on.

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
tool prints what the API returns. That now includes a membership's
`affiliation`, which `spaces members` prints as INTERNAL or EXTERNAL and which
somebody reads before deciding whether a space is safe to post in. It is the
far end's claim, printed unaltered and never filled in here when it is absent,
and it is worth exactly as much as everything else on this list. It can withhold
messages; a page that claims to be
the last one is believed, because there is no second source to check it
against. It can make requests slow or expensive, bounded by four things and
nothing else: `--timeout`, which bounds one attempt rather than the command;
the five-attempt limit; the 32-second cap on a backoff, which also caps how
long a `Retry-After` will be honoured for before the loop reports instead; and
the limit on how large a response body may be. It can serve an attachment whose
*contents* are hostile;
`spacebar` writes bytes to the path you named and never opens them.

A limit rather than an attack, in the same column, and worth stating beside it
because it is read at the same moment. A space can grant access to a Google
Group, and then everybody in that group is in the space without a membership of
their own. `spaces members` does not return that membership unless
`--show-groups` asks for it, and when it does the row carries no affiliation,
because the API sends none. The group's own members are not listed and are not
reachable from a Chat scope at all. So the question "who can see what I post
here" has an honest answer only as far as the edge of a group: this tool can
tell you that a group has access and cannot tell you who is in it, and no
amount of Chat scope would change that.

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
terminal, get a process started, or get a walk that *failed* reported as one
that finished.

That last one is worth separating from withholding, because the two look alike
and only one is defended. A page that fails is an error and a non-zero exit: a
list that dies on page four does not return three pages and exit 0. A page that
*lies*, claiming to be the last when it is not, is believed, because there is no
second source to check it against. So the guarantee is that this tool will not
invent completeness, not that the far end cannot.

## Keeping this current

Update this file in the same change that alters what the tool treats as
hostile, adds a way for a credential or a request to leave the process, changes
the confirmation or capability gates, or changes the disclosure process.

When a milestone lands, the `(Mn)` markers it covers are replaced by the names
of the tests that now hold those claims. A marker that outlives its milestone
is a claim nobody implemented, which is the failure this convention exists to
make visible.
