// Copyright 2026 Kevin O'Neil
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
)

// space is a Chat-shaped server with a webhook profile pointed at it.
type space struct {
	mu       sync.Mutex
	requests int
	bodies   []string
	queries  []string

	// refusing fails the test if anything reaches the server, which is how a
	// dry run is checked: by counting requests rather than by reading what came
	// back, because a refusal that arrives after the POST looks the same from
	// the outside.
	refusing *testing.T

	// url is the webhook URL for this server, for a command that reads one.
	url string
}

// stdinFor returns the webhook URL when a command reads one, and nothing
// otherwise.
func (s *space) stdinFor(needsURL bool) string {
	if !needsURL {
		return ""
	}
	return s.url + "\n"
}

// refuse makes any request from here on fail t.
func (s *space) refuse(t *testing.T) {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusing = t
}

func (s *space) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *space) lastBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		return ""
	}
	return s.bodies[len(s.bodies)-1]
}

func (s *space) lastQuery() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		return ""
	}
	return s.queries[len(s.queries)-1]
}

// configured stands up the server, configures a webhook profile for it, and
// leaves the tree ready for a send.
//
// The keyring is mocked and the configuration directory is a fresh temporary
// one, so nothing here touches the machine it runs on.
func configured(t *testing.T) *space {
	t.Helper()
	isolate(t)

	s := &space{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		s.mu.Lock()
		s.requests++
		s.bodies = append(s.bodies, string(body))
		s.queries = append(s.queries, r.URL.RawQuery)
		refusing := s.refusing
		s.mu.Unlock()

		if refusing != nil {
			refusing.Errorf("a request reached the network: %s %s", r.Method, r.URL.Path)
		}

		var sent chat.Message
		_ = json.Unmarshal(body, &sent)
		// What a real webhook send returns, measured against a live space:
		// name, space, text and thread. No createTime, no sender, and no
		// formattedText, which is why a live check was the only way to learn
		// how Chat interprets markup. A fixture more generous than the wire
		// tests the fixture.
		reply, _ := json.Marshal(chat.Message{
			Name:   "spaces/AAAATestSpace/messages/BBB",
			Text:   sent.Text,
			Thread: sent.Thread,
			Space:  &chat.Space{Name: "spaces/AAAATestSpace"},
		})
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		_, _ = w.Write(reply)
	}))
	t.Cleanup(server.Close)

	s.url = server.URL + "/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
	if got := runCLIIn(t, s.url+"\n", "profile", "set-webhook", "alerts"); got.exit != output.ExitOK {
		t.Fatalf("configuring: exit %d\n%s", got.exit, got.stderr)
	}
	return s
}

// TestTheZeroCeremonyCaseWorks is goal number one of the whole project, and it
// is one argument.
//
// A webhook profile posts to one space and is the only thing that
// authenticates the request, so there is nothing for a target argument to say
// that the URL has not already said. Requiring one would mean reading an
// identifier off a URL before every send.
func TestTheZeroCeremonyCaseWorks(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, "", "send", "deploy done")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if strings.TrimSpace(s.lastBody()) != `{"text":"deploy done"}` {
		t.Errorf("body = %s", s.lastBody())
	}
	for _, want := range []string{"spaces/AAAATestSpace/messages/BBB", "alerts"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, got.stdout)
		}
	}
}

// TestNamingTheSpaceWorksToo, because SPEC.md §10 puts the target first and a
// script written against that has to keep working.
func TestNamingTheSpaceWorksToo(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, "", "send", "spaces/AAAATestSpace", "deploy done")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if s.count() != 1 {
		t.Errorf("made %d requests, want 1", s.count())
	}
}

// TestTheWrongSpaceIsRefusedWithoutSending. Sending anyway would mean somebody
// who typed the wrong target watched their message arrive in a space full of
// people, with a success code saying it went where they asked.
func TestTheWrongSpaceIsRefusedWithoutSending(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, "", "send", "spaces/BBBBSomewhereElse", "deploy done")
	if got.exit != output.ExitUsage {
		t.Fatalf("exit = %d, want %d\n%s", got.exit, output.ExitUsage, got.stderr)
	}
	if s.count() != 0 {
		t.Errorf("%d requests were made", s.count())
	}
	if got.stdout != "" {
		t.Errorf("a failing command wrote to stdout: %q", got.stdout)
	}
}

// TestArgumentsAreReadByArityRatherThanByInspection.
//
// Deciding whether an argument is a target or a message by looking at it is how
// a message gets sent as a space name, and the two are not distinguishable in
// general: "spaces/AAAA" is a plausible thing to say to a colleague.
func TestArgumentsAreReadByArityRatherThanByInspection(t *testing.T) {
	s := configured(t)

	// One argument that looks exactly like a space is still the message.
	got := runCLIIn(t, "", "send", "spaces/AAAATestSpace")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if strings.TrimSpace(s.lastBody()) != `{"text":"spaces/AAAATestSpace"}` {
		t.Errorf("a message that looks like a space was read as one: %s", s.lastBody())
	}
}

// TestATransportThatReachesManySpacesNeedsATarget.
//
// Tested against splitArgs directly, because the only transport this build has
// reaches one space. Milestone 3 brings the other one, and this is the rule it
// will land on.
func TestATransportThatReachesManySpacesNeedsATarget(t *testing.T) {
	target, text, err := splitArgs(roaming{}, []string{"deploy done"})
	if err == nil {
		t.Fatalf("one argument was accepted: target %q text %q", target, text)
	}
	if got := output.ExitCodeOf(err); got != output.ExitUsage {
		t.Errorf("exit code = %d, want %d", got, output.ExitUsage)
	}
	if !strings.Contains(err.Error(), "more than one space") {
		t.Errorf("the failure does not say why:\n%v", err)
	}

	// Two arguments are target and text, on every transport.
	target, text, err = splitArgs(roaming{}, []string{"spaces/AAAA", "deploy done"})
	if err != nil || target != "spaces/AAAA" || text != "deploy done" {
		t.Errorf("splitArgs = %q, %q, %v", target, text, err)
	}
}

// roaming is a transport that reaches more than one space.
//
// It stands in for a user-OAuth profile in the argument-splitting tests, whose
// only question is whether the transport has one fixed space or not: a webhook
// knows its space, so `send 'text'` is complete, and this one does not, so the
// first of two arguments is a target. Nothing here reaches a network, and the
// methods return nothing for that reason rather than by omission.
type roaming struct{}

func (roaming) Kind() config.Transport { return config.TransportUserOAuth }
func (roaming) Profile() string        { return "work" }

func (roaming) Capabilities() transport.Capabilities {
	return transport.CapabilitiesFor(config.TransportUserOAuth)
}

func (roaming) Send(context.Context, chat.SendRequest) (*chat.Message, error) {
	return nil, nil
}

func (roaming) Spaces(context.Context, chat.ListSpacesRequest) iter.Seq2[chat.Space, error] {
	return func(func(chat.Space, error) bool) {}
}

func (roaming) GetSpace(context.Context, string) (*chat.Space, error) { return nil, nil }

func (roaming) Members(context.Context, chat.ListMembersRequest) iter.Seq2[chat.Membership, error] {
	return func(func(chat.Membership, error) bool) {}
}

func (roaming) Messages(context.Context, chat.ListMessagesRequest) iter.Seq2[chat.Message, error] {
	return func(func(chat.Message, error) bool) {}
}

func (roaming) GetMessage(context.Context, string) (*chat.Message, error) { return nil, nil }

func (roaming) Watch(context.Context, chat.WatchRequest) iter.Seq2[chat.SpaceEvent, error] {
	return func(func(chat.SpaceEvent, error) bool) {}
}

func (roaming) WatchMany(context.Context, chat.WatchManyRequest) iter.Seq2[chat.SpaceEvent, error] {
	return func(func(chat.SpaceEvent, error) bool) {}
}

func (roaming) Upload(context.Context, chat.UploadRequest) (*chat.AttachmentDataRef, error) {
	return nil, nil
}

func (roaming) Download(context.Context, string) ([]byte, error) { return nil, nil }

func (roaming) EditMessage(context.Context, chat.EditRequest) (*chat.Message, error) {
	return nil, nil
}

func (roaming) DeleteMessage(context.Context, string) error { return nil }

func (roaming) React(context.Context, chat.ReactRequest) (*chat.Reaction, error) { return nil, nil }

func (roaming) FindDirectMessage(context.Context, string) (*chat.Space, error) { return nil, nil }

func (roaming) Tail(context.Context, chat.TailRequest) iter.Seq2[chat.Message, error] {
	return func(func(chat.Message, error) bool) {}
}

// TestWithoutMdTheBodyIsSentByteForByte.
//
// The claim on the card, and the reason --md is opt-in. Chat markup is not
// CommonMark, so translating by default would silently rewrite everything
// anybody pasted.
func TestWithoutMdTheBodyIsSentByteForByte(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, "**not translated** and _neither is this_\n", "send", "-")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(s.lastBody(), `**not translated** and _neither is this_`) {
		t.Errorf("the body was altered: %s", s.lastBody())
	}
}

// TestAShellsTrailingNewlineIsNotPartOfTheMessage.
//
// `echo hi | send -` puts a newline on the end that nobody typed and nobody
// means, and it arrives in the space as a blank line under the message. What is
// inside the message is untouched: a blank line between two paragraphs is
// something somebody wrote, so only the trailing ones go.
func TestAShellsTrailingNewlineIsNotPartOfTheMessage(t *testing.T) {
	s := configured(t)

	for _, tc := range []struct{ name, stdin, want string }{
		{"one newline", "deploy done\n", "deploy done"},
		{"several", "deploy done\n\n\n", "deploy done"},
		{"none", "deploy done", "deploy done"},
		{"a paragraph break survives", "deployed\n\nand verified\n", "deployed\n\nand verified"},
		{"leading space survives", "  indented\n", "  indented"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runCLIIn(t, tc.stdin, "send", "-"); got.exit != output.ExitOK {
				t.Fatalf("exit %d\n%s", got.exit, got.stderr)
			}

			var sent chat.Message
			if err := json.Unmarshal([]byte(s.lastBody()), &sent); err != nil {
				t.Fatalf("the request body is not JSON: %v", err)
			}
			if sent.Text != tc.want {
				t.Errorf("text = %q, want %q", sent.Text, tc.want)
			}
		})
	}
}

// TestMdTranslatesAndReportsWhatItCouldNotCarry.
//
// The warnings are the point as much as the translation. Chat has no tables, so
// the rows arrive as lines of text, and somebody who wrote a table has to be
// told that rather than finding out from a colleague.
func TestMdTranslatesAndReportsWhatItCouldNotCarry(t *testing.T) {
	s := configured(t)

	body := "deploy **done**\n\n| a | b |\n| - | - |\n| 1 | 2 |\n"
	got := runCLIIn(t, body, "send", "--md", "-")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(s.lastBody(), `deploy *done*`) {
		t.Errorf("the body was not translated: %s", s.lastBody())
	}
	if !strings.Contains(got.stderr, "no tables") {
		t.Errorf("nothing warned about the table:\n%s", got.stderr)
	}
	if strings.Contains(got.stdout, "no tables") {
		t.Errorf("a warning reached stdout:\n%s", got.stdout)
	}
}

// TestInvalidUTF8IsRefusedAndNothingIsSent.
//
// Replacing the bad bytes would send a message that is not the one the caller
// wrote, to people who have no way to tell, on behalf of somebody who was told
// it succeeded.
func TestInvalidUTF8IsRefusedAndNothingIsSent(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, "deploy \xff done", "send", "-")
	if got.exit != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", got.exit, output.ExitUsage)
	}
	if s.count() != 0 {
		t.Errorf("%d requests were made", s.count())
	}
	if !strings.Contains(got.stderr, "offset") {
		t.Errorf("the failure does not say where the bad byte is:\n%s", got.stderr)
	}
}

// TestDryRunSendsNothing. A --dry-run that posted would be the worst failure
// this command has: somebody checking what would happen would have it happen.
func TestDryRunSendsNothing(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, "", "send", "--dry-run", "not sent")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if s.count() != 0 {
		t.Fatalf("--dry-run made %d requests", s.count())
	}
	// stdout is the request, exactly, and nothing else. Which profile it
	// resolved to is commentary and goes to stderr.
	for _, want := range []string{"POST ", "spaces/AAAATestSpace", "not sent", "key=REDACTED"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the dry run does not show %q:\n%s", want, got.stdout)
		}
	}
	if !strings.Contains(got.stderr, "alerts") {
		t.Errorf("nothing said which profile it would have gone through:\n%s", got.stderr)
	}

	// The card's claim: a body from stdin is shown as it will be sent, so the
	// asterisks survive without --md.
	got = runCLIIn(t, "**not translated**", "send", "--dry-run", "-")
	if !strings.Contains(got.stdout, "**not translated**") {
		t.Errorf("the dry run altered the body:\n%s", got.stdout)
	}
	if s.count() != 0 {
		t.Errorf("--dry-run made %d requests", s.count())
	}
}

// TestFlagsThatContradictEachOtherAreRefusedBeforeAnything.
func TestFlagsThatContradictEachOtherAreRefusedBeforeAnything(t *testing.T) {
	s := configured(t)

	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"two names for the message", []string{"send", "--message-id", "client-a", "--idempotent", "hi"}, "both name the message"},
		{"a message id without the prefix", []string{"send", "--message-id", "mine", "hi"}, chat.MessageIDPrefix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runCLIIn(t, "", tc.args...)
			if got.exit != output.ExitUsage {
				t.Fatalf("exit = %d, want %d\n%s", got.exit, output.ExitUsage, got.stderr)
			}
			if !strings.Contains(got.stderr, tc.says) {
				t.Errorf("the failure does not mention %q:\n%s", tc.says, got.stderr)
			}
		})
	}
	if s.count() != 0 {
		t.Errorf("%d requests were made by refused invocations", s.count())
	}
}

// TestAMissingArgumentIsAUsageFailureAndNeverAStdinRead.
//
// The card asks for this specifically. Reading stdin because an argument was
// forgotten would leave somebody looking at a command that appears to have
// hung, which is the failure SPEC.md §11.3 is about.
func TestAMissingArgumentIsAUsageFailureAndNeverAStdinRead(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, "this must not be read as the message\n", "send")
	if got.exit != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", got.exit, output.ExitUsage)
	}
	if s.count() != 0 {
		t.Errorf("%d requests were made", s.count())
	}
}

// TestACapabilityThisProfileLacksIsExitFiveWithNoRequest.
//
// The flags are registered rather than left out on purpose. An unregistered
// flag is exit 2, "unknown flag --file", which says this tool cannot do
// attachments at all; a registered one is exit 5 naming the capability and the
// profile, which says this profile cannot, and that is both true and something
// somebody can act on.
func TestACapabilityThisProfileLacksIsExitFiveWithNoRequest(t *testing.T) {
	s := configured(t)

	for _, tc := range []struct {
		flag, value, says string
	}{
		{"--file", "report.pdf", "attachment upload"},
		{"--reply-to", "spaces/AAAATestSpace/messages/CCC", "read access"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			got := runCLIIn(t, "", "send", tc.flag, tc.value, "hi")
			if got.exit != output.ExitUnsupported {
				t.Fatalf("exit = %d, want %d\n%s", got.exit, output.ExitUnsupported, got.stderr)
			}
			for _, want := range []string{tc.says, "alerts", "webhook"} {
				if !strings.Contains(got.stderr, want) {
					t.Errorf("the refusal does not mention %q:\n%s", want, got.stderr)
				}
			}
		})
	}
	if s.count() != 0 {
		t.Errorf("%d requests were made by refused invocations", s.count())
	}
}

// TestACardGoesThroughOnAWebhook, which is the one thing this transport can do
// that a user-authorized one cannot.
func TestACardGoesThroughOnAWebhook(t *testing.T) {
	s := configured(t)

	path := filepath.Join(t.TempDir(), "card.json")
	card := `[{"cardId":"deploy","card":{"header":{"title":"Deployed"},"sections":[{"widgets":[{"textParagraph":{"text":"v1.2.3"}}]}]}}]`
	if err := os.WriteFile(path, []byte(card), 0o600); err != nil {
		t.Fatalf("writing the card: %v", err)
	}

	got := runCLIIn(t, "", "send", "--card", path, "shipped")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(s.lastBody(), `"cardsV2"`) || !strings.Contains(s.lastBody(), "v1.2.3") {
		t.Errorf("the card did not reach the request: %s", s.lastBody())
	}
}

// TestACardFileThatIsTheWrongShapeIsNamedRatherThanGuessedAt.
//
// Pasting one card where a list belongs is the common mistake. Wrapping it
// would be this tool deciding what somebody meant.
func TestACardFileThatIsTheWrongShapeIsNamedRatherThanGuessedAt(t *testing.T) {
	s := configured(t)
	dir := t.TempDir()

	for _, tc := range []struct{ name, body, says string }{
		{"one card", `{"cardId":"a","card":{}}`, "holds one card"},
		{"no card field", `[{"cardId":"a"}]`, "no \"card\" field"},
		{"empty", `[]`, "no cards"},
		{"not json", `{`, "not the JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("writing: %v", err)
			}

			got := runCLIIn(t, "", "send", "--card", path, "shipped")
			if got.exit != output.ExitUsage {
				t.Fatalf("exit = %d, want %d\n%s", got.exit, output.ExitUsage, got.stderr)
			}
			if !strings.Contains(got.stderr, tc.says) {
				t.Errorf("the failure does not say %q:\n%s", tc.says, got.stderr)
			}
		})
	}
	if s.count() != 0 {
		t.Errorf("%d requests were made", s.count())
	}
}

// TestIdempotentDerivesAMessageIDTheAPIWillRefuseTwice.
func TestIdempotentDerivesAMessageIDTheAPIWillRefuseTwice(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, "", "send", "--idempotent", "deploy done")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(s.lastQuery(), "messageId=client-") {
		t.Errorf("no derived message ID reached the request: %s", s.lastQuery())
	}

	// The same message derives the same ID, which is the whole point: a
	// retrying agent cannot double-post.
	first := s.lastQuery()
	if got := runCLIIn(t, "", "send", "--idempotent", "deploy done"); got.exit != output.ExitOK {
		t.Fatalf("exit %d", got.exit)
	}
	if s.lastQuery() != first {
		t.Errorf("the same message derived two IDs:\n%s\n%s", first, s.lastQuery())
	}

	// A different message does not.
	if got := runCLIIn(t, "", "send", "--idempotent", "deploy failed"); got.exit != output.ExitOK {
		t.Fatalf("exit %d", got.exit)
	}
	if s.lastQuery() == first {
		t.Error("two different messages derived the same ID, so one would be dropped as a duplicate")
	}
}

// TestAThreadKeyReachesTheBodyAndImpliesAReplyOption.
//
// Without the option the API is documented to ignore the thread key silently,
// so a send that looked like it worked would quietly start a new thread every
// time.
func TestAThreadKeyReachesTheBodyAndImpliesAReplyOption(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, "", "send", "--thread-key", "deploys", "deploy done")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(s.lastBody(), `"threadKey":"deploys"`) {
		t.Errorf("the thread key did not reach the body: %s", s.lastBody())
	}
	if !strings.Contains(s.lastQuery(), "messageReplyOption="+chat.ReplyFallbackToNewThread) {
		t.Errorf("no reply option was sent, so the API would ignore the thread key: %s", s.lastQuery())
	}
}

// TestSendingWithNoProfileConfiguredSaysWhatToDo, which is the first thing
// somebody hits on a fresh machine.
func TestSendingWithNoProfileConfiguredSaysWhatToDo(t *testing.T) {
	isolate(t)

	got := runCLIIn(t, "", "send", "hello")
	if got.exit == output.ExitOK {
		t.Fatal("a send with no profile succeeded")
	}
	if got.stdout != "" {
		t.Errorf("a failing command wrote to stdout: %q", got.stdout)
	}
	if !strings.Contains(got.stderr, "no profile") {
		t.Errorf("the failure does not say what is missing:\n%s", got.stderr)
	}
}

// TestVerboseShowsTheRequestWithoutTheCredential, on the send path as well as
// on the setup one.
func TestVerboseShowsTheRequestWithoutTheCredential(t *testing.T) {
	configured(t)

	got := runCLIIn(t, "", "--verbose", "send", "deploy done")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	for _, want := range []string{"> POST", "key=REDACTED", "token=REDACTED", "< 200 OK"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("--verbose is missing %q:\n%s", want, got.stderr)
		}
	}
	for _, secret := range []string{testKey, testToken} {
		if strings.Contains(got.stderr, secret) {
			t.Errorf("--verbose printed a credential:\n%s", got.stderr)
		}
	}
}

// TestTheJSONResultIsOneObjectOnStdout, which is what a caller piping into jq
// gets and the only thing they get.
func TestTheJSONResultIsOneObjectOnStdout(t *testing.T) {
	configured(t)

	got := runCLIIn(t, "", "--json", "send", "deploy done")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	var result sendResult
	if err := json.Unmarshal([]byte(got.stdout), &result); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, got.stdout)
	}
	if result.Message != "spaces/AAAATestSpace/messages/BBB" {
		t.Errorf("message = %q", result.Message)
	}
	if result.Space != "spaces/AAAATestSpace" || result.Profile != "alerts" {
		t.Errorf("result = %+v", result)
	}
	// A webhook send returns no timestamp, so there is none to report. The
	// field stays because a user-authorized read will have one, and
	// TestATimestampIsPassedThroughRatherThanReformatted holds what happens
	// then.
	if result.CreateTime != "" {
		t.Errorf("create_time = %q, and a webhook send returns none", result.CreateTime)
	}
}

// TestABodyLargerThanChatAcceptsIsRefusedRatherThanSent.
func TestABodyLargerThanChatAcceptsIsRefusedRatherThanSent(t *testing.T) {
	s := configured(t)

	got := runCLIIn(t, strings.Repeat("x", maxBodyBytes+1), "send", "-")
	if got.exit != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", got.exit, output.ExitUsage)
	}
	if s.count() != 0 {
		t.Errorf("%d requests were made", s.count())
	}
}

// TestWhereTheMessageWentIsNotTakenOnTheServersWord.
//
// A webhook is issued for one space and is the only thing that authenticates
// the request, so the API cannot put the message anywhere else: the URL is the
// fact and a response naming a different space is saying something that cannot
// be true. The threat model says a hostile space can lie about the data, and
// the line a person reads to confirm where their message went is the worst
// place to believe it.
func TestWhereTheMessageWentIsNotTakenOnTheServersWord(t *testing.T) {
	isolate(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A server claiming the message landed somewhere it cannot have.
		_, _ = fmt.Fprint(w, `{"name":"spaces/CCCCLies/messages/BBB","space":{"name":"spaces/CCCCLies"}}`)
	}))
	t.Cleanup(server.Close)

	url := server.URL + "/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
	if got := runCLIIn(t, url+"\n", "profile", "set-webhook", "alerts"); got.exit != output.ExitOK {
		t.Fatalf("configuring: exit %d\n%s", got.exit, got.stderr)
	}

	got := runCLIIn(t, "", "--json", "send", "deploy done")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	var result sendResult
	if err := json.Unmarshal([]byte(got.stdout), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if result.Space != "spaces/AAAATestSpace" {
		t.Errorf("space = %q, want the one the webhook is for; the server was believed", result.Space)
	}

	// The message name is still reported as it came back, because that is the
	// server's to assign and there is no second source for it.
	if result.Message != "spaces/CCCCLies/messages/BBB" {
		t.Errorf("message = %q, and the name is the server's to give", result.Message)
	}
}

// TestATimestampIsPassedThroughRatherThanReformatted.
//
// A webhook send returns no createTime, which is why the fixture above has
// none. A user-authorized read will, and when it does the string is carried
// exactly: decoding it into a time.Time to print it again is a conversion that
// can lose or change something, and this tool does not alter a value to make it
// representable.
func TestATimestampIsPassedThroughRatherThanReformatted(t *testing.T) {
	isolate(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB","createTime":"2026-08-14T18:42:20.123456Z"}`)
	}))
	t.Cleanup(server.Close)

	url := server.URL + "/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
	if got := runCLIIn(t, url+"\n", "profile", "set-webhook", "alerts"); got.exit != output.ExitOK {
		t.Fatalf("configuring: exit %d\n%s", got.exit, got.stderr)
	}

	got := runCLIIn(t, "", "--json", "send", "deploy done")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	var result sendResult
	if err := json.Unmarshal([]byte(got.stdout), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if result.CreateTime != "2026-08-14T18:42:20.123456Z" {
		t.Errorf("create_time = %q, and the sub-second part is the caller's to keep", result.CreateTime)
	}
}

// TestMentionsArePrependedInTheOrderTheyWereGiven.
//
// The body is not searched for a place to put them. A message is never
// rewritten to make a flag fit, so they go in front, in order, and the text
// somebody wrote arrives unaltered behind them.
func TestMentionsArePrependedInTheOrderTheyWereGiven(t *testing.T) {
	got, err := withMentions("deploy done", []string{"a@example.test", "users/123", "b@example.test"})
	if err != nil {
		t.Fatalf("withMentions: %v", err)
	}
	want := "<users/a@example.test> <users/123> <users/b@example.test> deploy done"
	if got != want {
		t.Errorf("withMentions =\n  %q\nwant\n  %q", got, want)
	}

	// No mentions must not touch the body at all, not even its whitespace.
	if got, err := withMentions("deploy done", nil); err != nil || got != "deploy done" {
		t.Errorf("withMentions with no mentions = %q, %v", got, err)
	}
}

// TestAMentionThatCannotBeRepresentedIsRefusedBeforeAnythingIsSent.
//
// format.Mention is the only place a mention is built, so an address that
// cannot sit inside the wrapper fails here rather than posting as literal text
// in front of colleagues.
func TestAMentionThatCannotBeRepresentedIsRefusedBeforeAnythingIsSent(t *testing.T) {
	if _, err := withMentions("hi", []string{"a>b@example.test"}); err == nil {
		t.Fatal("an address carrying a closing bracket was accepted")
	}
	if _, err := withMentions("hi", []string{"fine@example.test", "a>b@example.test"}); err == nil {
		t.Error("a bad address after a good one was accepted")
	}
}

// TestAMentionThatMatchedNobodyIsWarnedAbout.
//
// Measured on 2026-08-17 against the real API: an address that is nobody is not
// refused. Chat answers 200 and posts the message with "<users/>" where the
// mention should be, no annotation, and nobody notified. There is no way to
// refuse it beforehand, so the failure is read out of the body the API echoes
// back.
//
// A warning and exit 0, matching what --md does when a table cannot be
// represented: the message was posted, so a non-zero exit would say it was not,
// and this tool may not write a result to stdout on a failure. What must not
// happen is silence.
func TestAMentionThatMatchedNobodyIsWarnedAbout(t *testing.T) {
	if got := unresolvedMentions("<users/> deploy done"); len(got) != 1 {
		t.Errorf("a dropped mention produced %d warnings, want 1", len(got))
	}
	if got := unresolvedMentions("@Kevin O'Neil deploy done"); got != nil {
		t.Errorf("a mention that landed warned anyway: %v", got)
	}
	if got := unresolvedMentions("deploy done"); got != nil {
		t.Errorf("a message with no mention warned: %v", got)
	}
}
