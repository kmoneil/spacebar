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

package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/store"
	"github.com/kmoneil/spacebar/internal/transport"
)

// fake is a transport with the capabilities a test says it has and canned
// answers behind them.
//
// The capabilities are the point. Everything this package decides, it decides
// from them, so a fake that let them drift from the answers would be testing
// its own construction.
type fake struct {
	kind config.Transport
	caps transport.Capabilities

	spaces   []chat.Space
	members  []chat.Membership
	messages []chat.Message

	// fail is yielded instead of the last item, standing in for a page that
	// failed part way through a walk.
	fail error

	// sends counts what actually reached the transport, which is what an
	// allowlist test has to assert on: a refusal after the send carries the
	// same error as one before it.
	sends     int
	reactions int
}

func (f *fake) Kind() config.Transport               { return f.kind }
func (f *fake) Profile() string                      { return "work" }
func (f *fake) Capabilities() transport.Capabilities { return f.caps }
func (f *fake) Send(_ context.Context, req chat.SendRequest) (*chat.Message, error) {
	f.sends++
	return &chat.Message{
		Name:  req.Space + "/messages/BBB",
		Text:  req.Message.Text,
		Space: &chat.Space{Name: req.Space},
	}, nil
}

func (f *fake) Spaces(context.Context, chat.ListSpacesRequest) iter.Seq2[chat.Space, error] {
	return yield(f.spaces, f.fail)
}

func (f *fake) GetSpace(_ context.Context, name string) (*chat.Space, error) {
	for _, space := range f.spaces {
		if space.Name == name {
			return &space, nil
		}
	}
	return nil, output.Errorf("NOT_FOUND", output.ExitAPI, "no space named %q", name)
}

func (f *fake) Members(context.Context, chat.ListMembersRequest) iter.Seq2[chat.Membership, error] {
	return yield(f.members, f.fail)
}

func (f *fake) Messages(context.Context, chat.ListMessagesRequest) iter.Seq2[chat.Message, error] {
	return yield(f.messages, f.fail)
}

func (f *fake) GetMessage(_ context.Context, name string) (*chat.Message, error) {
	for _, message := range f.messages {
		if message.Name == name {
			return &message, nil
		}
	}
	return nil, output.Errorf("NOT_FOUND", output.ExitAPI, "no message named %q", name)
}

func (f *fake) Watch(context.Context, chat.WatchRequest) iter.Seq2[chat.SpaceEvent, error] {
	return func(func(chat.SpaceEvent, error) bool) {}
}

func (f *fake) WatchMany(context.Context, chat.WatchManyRequest) iter.Seq2[chat.SpaceEvent, error] {
	return func(func(chat.SpaceEvent, error) bool) {}
}

func (f *fake) Upload(context.Context, chat.UploadRequest) (*chat.AttachmentDataRef, error) {
	return nil, errors.New("the fake does not upload")
}

func (f *fake) Download(context.Context, string) ([]byte, error) {
	return nil, errors.New("the fake does not download")
}

func (f *fake) EditMessage(context.Context, chat.EditRequest) (*chat.Message, error) {
	return nil, errors.New("the fake does not edit")
}

func (f *fake) DeleteMessage(context.Context, string) error {
	return errors.New("the fake does not delete")
}

func (f *fake) React(_ context.Context, req chat.ReactRequest) (*chat.Reaction, error) {
	f.reactions++
	return &chat.Reaction{
		Name:  req.Message + "/reactions/1",
		Emoji: &chat.Emoji{Unicode: req.Emoji},
		User:  &chat.User{Name: "users/1"},
	}, nil
}

func (f *fake) FindDirectMessage(context.Context, string) (*chat.Space, error) {
	return nil, output.Errorf("NOT_FOUND", output.ExitAPI, "no direct message")
}

func (f *fake) Tail(context.Context, chat.TailRequest) iter.Seq2[chat.Message, error] {
	return yield(f.messages, f.fail)
}

func yield[T any](items []T, fail error) iter.Seq2[T, error] {
	return func(y func(T, error) bool) {
		var zero T
		for _, item := range items {
			if !y(item, nil) {
				return
			}
		}
		if fail != nil {
			y(zero, fail)
		}
	}
}

// full is what a profile authorized with every scope this build asks for can
// do, taken from the matrix rather than written out here.
func full() transport.Capabilities {
	return transport.CapabilitiesFor(config.TransportUserOAuth)
}

// connect starts a server and a client on a pair of in-memory pipes and returns
// the client's session.
//
// A real client rather than a direct call, because the claim is about what a
// model can see. A tool registered but not advertised, or advertised and not
// callable, would pass a test that only asked this package what it had built.
func connect(t *testing.T, f *fake) *mcp.ClientSession {
	t.Helper()
	return connectWith(t, Options{Profile: &profile.Open{Transport: f, Name: "work"}})
}

// connectWith is the same, for a test that cares about the options.
func connectWith(t *testing.T, opts Options) *mcp.ClientSession {
	t.Helper()

	server, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	serverSide, clientSide := mcp.NewInMemoryTransports()
	go func() {
		if err := server.srv.Run(context.Background(), serverSide); err != nil {
			// Not t.Error: the run ends when the session closes, and that is
			// not a failure.
			_ = err
		}
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), clientSide, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return session
}

// advertised is what the peer says it can see, in order.
func advertised(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// TestOnlyToolsThisProfileCanServeAreRegistered.
//
// The rule from SPEC.md §14.1, asserted as the exact set rather than as a
// count: a count passes when one tool is swapped for another, which is the
// mistake that would matter. Read against a connected client, because a tool
// this package registered and the peer cannot see is not a tool.
func TestOnlyToolsThisProfileCanServeAreRegistered(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps transport.Capabilities
		want []string
	}{
		{
			name: "every scope this build asks for",
			caps: full(),
			want: []string{"get_message", "get_space", "list_members", "list_messages", "list_spaces"},
		},
		{
			// The scope gap that shipped once already: chat.memberships.readonly
			// was in no grant this tool issued, and `spaces members` answered 403
			// on every profile. Over MCP the tool is simply absent, which is the
			// difference the registration rule exists for.
			name: "a token without the memberships scope",
			caps: without(full(), transport.CanReadMembers),
			want: []string{"get_message", "get_space", "list_messages", "list_spaces"},
		},
		{
			name: "a token that can list spaces and read nothing",
			caps: transport.Capabilities{CanListSpaces: true},
			want: []string{"list_spaces"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := connect(t, &fake{kind: config.TransportUserOAuth, caps: tc.caps})
			if got := advertised(t, session); !slices.Equal(got, tc.want) {
				t.Errorf("tools = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAProfileThatCanServeNothingIsRefusedRatherThanEmpty.
//
// A webhook is write-only and every tool in this build reads, so the honest
// answer is a refusal at exit 5 naming the profile. An empty server would
// connect, advertise nothing, and leave somebody to work out from a model's
// confusion that their profile cannot read.
func TestAProfileThatCanServeNothingIsRefusedRatherThanEmpty(t *testing.T) {
	_, err := New(Options{Profile: &profile.Open{
		Name:      "alerts",
		Transport: &fake{kind: config.TransportWebhook, caps: transport.CapabilitiesFor(config.TransportWebhook)},
	}})
	if err == nil {
		t.Fatal("a webhook profile built a server with no tools in it")
	}

	out, ok := errors.AsType[*output.Error](err)
	if !ok {
		t.Fatalf("the refusal is not an output.Error: %T", err)
	}
	if out.Exit != output.ExitUnsupported {
		t.Errorf("exit %d, want %d", out.Exit, output.ExitUnsupported)
	}
	if !strings.Contains(out.Message, "alerts") {
		t.Errorf("the refusal does not name the profile:\n%s", out.Message)
	}
}

// TestAToolThatIsNotRegisteredCannotBeCalled, which is the half of the rule a
// registration test cannot see: absent from the list and absent from the
// dispatch are different things, and only the second one is a defence.
func TestAToolThatIsNotRegisteredCannotBeCalled(t *testing.T) {
	session := connect(t, &fake{
		kind: config.TransportUserOAuth,
		caps: without(full(), transport.CanReadMembers),
	})

	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_members",
		Arguments: map[string]any{"space": "spaces/AAA"},
	})
	if err == nil {
		t.Fatal("a tool that was never registered answered a call")
	}
}

// TestAListSaysWhetherThereIsMore.
//
// A tool result is one document rather than a stream, so a model handed the
// default twenty-five has no way to tell a full space from a small one. One
// extra item is asked for and this reports whether it arrived, which turns a
// guess into an observation.
func TestAListSaysWhetherThereIsMore(t *testing.T) {
	spaces := make([]chat.Space, 4)
	for i := range spaces {
		spaces[i] = chat.Space{Name: "spaces/" + string(rune('A'+i))}
	}
	session := connect(t, &fake{kind: config.TransportUserOAuth, caps: full(), spaces: spaces})

	var short listSpacesOut
	callTool(t, session, "list_spaces", map[string]any{"limit": 2}, &short)
	if len(short.Spaces) != 2 || !short.HasMore {
		t.Errorf("limit 2 of 4 returned %d spaces, has_more %v", len(short.Spaces), short.HasMore)
	}

	var whole listSpacesOut
	callTool(t, session, "list_spaces", map[string]any{"limit": 10}, &whole)
	if len(whole.Spaces) != 4 || whole.HasMore {
		t.Errorf("limit 10 of 4 returned %d spaces, has_more %v", len(whole.Spaces), whole.HasMore)
	}
}

// TestAFailedWalkIsAFailureRatherThanAShortList.
//
// The CLI can write the rows it already has and exit non-zero, because a shell
// pipeline can act on both halves. A tool result cannot: it is one document,
// and a partial one returned as a success is the truncation rule violated in
// the place it matters most, because the reader is a model that will summarize
// it as though it were the answer.
func TestAFailedWalkIsAFailureRatherThanAShortList(t *testing.T) {
	session := connect(t, &fake{
		kind:   config.TransportUserOAuth,
		caps:   full(),
		spaces: []chat.Space{{Name: "spaces/AAA"}},
		fail:   errors.New("the second page failed"),
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_spaces"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatalf("a walk that failed came back as a success: %+v", result)
	}
	if !strings.Contains(contentOf(result), "second page") {
		t.Errorf("the failure does not say what went wrong: %s", contentOf(result))
	}
}

// TestTheStructuredShapeIsTheOneTheCLIPublishes.
//
// One contract, two adapters. The field names here are what `--json` emits,
// because both come from internal/rows, and this is the assertion that says so
// rather than the comment claiming it.
func TestTheStructuredShapeIsTheOneTheCLIPublishes(t *testing.T) {
	session := connect(t, &fake{
		kind: config.TransportUserOAuth,
		caps: full(),
		spaces: []chat.Space{{
			Name:            "spaces/AAA",
			SpaceType:       "DIRECT_MESSAGE",
			SingleUserBotDm: true,
			LastActiveTime:  "2026-08-16T09:00:00Z",
		}},
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_spaces"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	// Compared as values rather than as bytes. The peer receives an object and
	// decodes it, so key order is the encoder's business here, unlike --json,
	// where the goldens freeze the byte order a script may be cutting on.
	// The field names are the contract in both.
	const want = `{"spaces":[{"name":"spaces/AAA","space_type":"DIRECT_MESSAGE",` +
		`"single_user_bot_dm":true,"last_active_time":"2026-08-16T09:00:00Z"}]}`

	var expected any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("the expected shape does not parse: %v", err)
	}

	body, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshalling the structured content: %v", err)
	}
	var got any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding the structured content: %v", err)
	}

	if !reflect.DeepEqual(got, expected) {
		t.Errorf("structured content changed, which every caller sees:\n got %s\nwant %s", body, want)
	}
}

// TestALimitBeyondTheCeilingIsRefusedRatherThanClamped, on the rule that a value
// is never silently altered: somebody who asked for a thousand and quietly got
// two hundred has an answer that is short for a reason nothing told them.
func TestALimitBeyondTheCeilingIsRefusedRatherThanClamped(t *testing.T) {
	session := connect(t, &fake{kind: config.TransportUserOAuth, caps: full()})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_spaces",
		Arguments: map[string]any{"limit": 1000},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("a limit of 1000 was accepted")
	}
	if !strings.Contains(contentOf(result), "200") {
		t.Errorf("the refusal does not name the ceiling: %s", contentOf(result))
	}
}

// callTool calls one and decodes its structured content.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any, into any) {
	t.Helper()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s): %s", name, contentOf(result))
	}

	body, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
}

func contentOf(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

// without returns these capabilities with one taken away, which is what a
// missing scope produces.
func without(caps transport.Capabilities, drop transport.Capability) transport.Capabilities {
	switch drop {
	case transport.CanReadMembers:
		caps.CanReadMembers = false
	case transport.CanRead:
		caps.CanRead = false
	case transport.CanListSpaces:
		caps.CanListSpaces = false
	}
	return caps
}

// TestTheSpaceArgumentIsDescribedOneWay.
//
// A struct tag is a compile-time literal, so the sentence describing a space
// cannot be written once and referenced from three tools. This is what holds it
// together instead: the schemas are read back off a connected client and
// compared, so an edit to one of them fails here rather than shipping three
// tools that describe the same argument differently.
//
// Only `space`, and the first version of this test was wrong to ask for more.
// It demanded that every repeated argument name carry one sentence, and caught
// `limit` and `filter`, where the difference is the point: "how many messages"
// beats "how many items" for the model reading it, and the filter on a message
// list composes with the time window while the one on a space list does not.
// A space is the one argument that is the same thing everywhere, resolved by
// the same four steps, so it is the one where two wordings would be two claims.
func TestTheSpaceArgumentIsDescribedOneWay(t *testing.T) {
	session := connect(t, &fake{kind: config.TransportUserOAuth, caps: full()})

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	described := map[string]string{}
	for _, tool := range result.Tools {
		description, ok := properties(t, tool)["space"]
		if !ok {
			continue
		}
		described[tool.Name] = description
	}

	if len(described) < 3 {
		t.Fatalf("only %d tools take a space, so this is not testing what it says: %v", len(described), described)
	}

	var first, firstTool string
	for tool, description := range described {
		if description == "" {
			t.Errorf("%s describes its space argument with nothing", tool)
		}
		if first == "" {
			first, firstTool = description, tool
			continue
		}
		if description != first {
			t.Errorf("the space argument is described two ways:\n  %s: %s\n  %s: %s",
				firstTool, first, tool, description)
		}
	}
}

// properties reads a tool's argument descriptions off the schema its peer was
// handed.
func properties(t *testing.T, tool *mcp.Tool) map[string]string {
	t.Helper()

	body, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshalling the schema for %s: %v", tool.Name, err)
	}

	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatalf("decoding the schema for %s: %v", tool.Name, err)
	}

	out := map[string]string{}
	for name, property := range schema.Properties {
		out[name] = property.Description
	}
	return out
}

// TestAClientThatDisconnectsEndsTheRunWithoutAnError.
//
// An MCP client starts this server and stops it when the session ends, so
// hanging up is how the command is meant to finish, in the way Ctrl-C is how
// `tail` is meant to finish. A non-zero exit there would put a failure in
// somebody's client log for the ordinary end of every session.
//
// The narrow claim, measured rather than assumed: a peer that goes away between
// messages ends Run at nil. A peer that goes away while a request is in flight
// does not, and the error carries the SDK's own wording, which is a different
// situation and is left alone rather than swallowed by matching on a string.
func TestAClientThatDisconnectsEndsTheRunWithoutAnError(t *testing.T) {
	server, err := New(Options{Profile: &profile.Open{
		Name:      "work",
		Transport: &fake{kind: config.TransportUserOAuth, caps: full()},
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	serverSide, clientSide := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- server.srv.Run(context.Background(), serverSide) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), clientSide, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := <-done; err != nil {
		t.Errorf("a client that disconnected ended the run with %v", err)
	}
}

// TestAWriteToolIsAbsentWithoutAllowWrite.
//
// The first claim on the card, and the one the whole design rests on. A model
// that cannot see a tool cannot argue itself into calling it, so the gate is
// registration rather than a refusal at call time.
//
// Both directions are asserted. Without the flag the tool set is exactly the
// read tools; with it, the write tools join them and nothing else changes.
//
// The want list is spelled out rather than derived from what the server built,
// which is the whole point: a test that asks the code what it registered and
// then agrees would pass on the day a tool is registered by mistake.
func TestAWriteToolIsAbsentWithoutAllowWrite(t *testing.T) {
	reads := []string{"get_message", "get_space", "list_members", "list_messages", "list_spaces"}

	closed := connect(t, &fake{kind: config.TransportUserOAuth, caps: full()})
	if got := advertised(t, closed); !slices.Equal(got, reads) {
		t.Errorf("without --allow-write the tools are %v, want %v", got, reads)
	}

	open := connectWith(t, Options{
		Profile:    &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
		AllowWrite: true,
	})
	want := append(append([]string(nil), reads...), "send_message", "react_to_message")
	slices.Sort(want)
	if got := advertised(t, open); !slices.Equal(got, want) {
		t.Errorf("with --allow-write the tools are %v, want %v", got, want)
	}
}

// TestAWebhookWithAllowWriteRegistersExactlyOneTool.
//
// m5-01's claim, which had to wait for the flag. A webhook can post and can do
// nothing else, so this is the one profile shape where the tool set is a single
// write tool, and it is also the population this project exists for.
func TestAWebhookWithAllowWriteRegistersExactlyOneTool(t *testing.T) {
	session := connectWith(t, Options{
		Profile: &profile.Open{
			Name:      "alerts",
			Transport: &fake{kind: config.TransportWebhook, caps: transport.CapabilitiesFor(config.TransportWebhook)},
		},
		AllowWrite: true,
	})

	if got := advertised(t, session); !slices.Equal(got, []string{"send_message"}) {
		t.Errorf("tools = %v, want exactly send_message", got)
	}
}

// TestEveryWriteToolSaysToConfirmFirst.
//
// Compared as a suffix against the constant rather than searched for as a
// substring of some words: the sentence is a promise to whoever reads the tool
// list, and a reworded one is a different promise. SPEC.md §14.2 specifies it
// exactly.
func TestEveryWriteToolSaysToConfirmFirst(t *testing.T) {
	session := connectWith(t, Options{
		Profile:    &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
		AllowWrite: true,
	})

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	// Spelled out here rather than compared against the constant, which is what
	// the first version of this test did and which asserted nothing: rewording
	// the constant moved both sides of the comparison together, and a planted
	// reword passed. SPEC.md §14.2 specifies these words, so these words are in
	// the test.
	const want = "This posts a visible message to a real Google Chat space. " +
		"Confirm with the user before calling."

	writes := 0
	for _, tool := range result.Tools {
		if !strings.HasPrefix(tool.Name, "send_") {
			continue
		}
		writes++
		if !strings.HasSuffix(tool.Description, want) {
			t.Errorf("%s does not end with the confirmation sentence:\n%s", tool.Name, tool.Description)
		}
	}
	if writes == 0 {
		t.Fatal("no write tool was registered, so this asserted nothing")
	}
}

// TestASpaceOutsideTheAllowlistIsRefusedBeforeTheRequest.
//
// Counted rather than read out of the error, which is the card's own
// instruction and the same rule the dry-run tests follow: a refusal that
// arrives after the POST carries the same error as one that arrives before it,
// and only one of them left a message in a space.
func TestASpaceOutsideTheAllowlistIsRefusedBeforeTheRequest(t *testing.T) {
	sent := &fake{kind: config.TransportUserOAuth, caps: full()}
	session := connectWith(t, Options{
		Profile:     &profile.Open{Name: "work", Transport: sent},
		AllowWrite:  true,
		AllowSpaces: []string{"spaces/AAA"},
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"space": "spaces/BBB", "text": "hello"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("a space outside the allowlist was accepted")
	}
	if sent.sends != 0 {
		t.Errorf("the refusal arrived after %d sends", sent.sends)
	}

	// And the allowed one goes through, because an allowlist that refuses
	// everything is not an allowlist.
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"space": "spaces/AAA", "text": "hello"},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if sent.sends != 1 {
		t.Errorf("the allowed space produced %d sends, want 1", sent.sends)
	}
}

// TestAnAllowlistEntryMustBeAResourceName, because it is compared against what
// a call resolves to. An alias here would be an allowlist whose meaning depends
// on what the API says at the moment it is consulted.
func TestAnAllowlistEntryMustBeAResourceName(t *testing.T) {
	for _, bad := range []string{"eng-alerts", "Ops", "bob@example.test", "spaces/", ""} {
		_, err := New(Options{
			Profile:     &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
			AllowWrite:  true,
			AllowSpaces: []string{bad},
		})
		if err == nil {
			t.Errorf("--allow-space %q was accepted", bad)
		}
	}
}

// TestEveryToolCallIsOneLineOnStderr, per SPEC.md §14.2, and the line has to be
// one JSON object rather than a sentence: it is read by whatever is collecting
// logs, and a message body can contain anything at all.
func TestEveryToolCallIsOneLineOnStderr(t *testing.T) {
	var lines []string
	session := connectWith(t, Options{
		Profile:    &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
		AllowWrite: true,
		Audit:      func(line string) { lines = append(lines, line) },
	})

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"space": "spaces/AAA",
			"text":  "deploy done\nwith a newline in it",
		},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("one call produced %d lines: %v", len(lines), lines)
	}
	if strings.Contains(lines[0], "\n") {
		t.Errorf("the audit line is not one line:\n%s", lines[0])
	}

	var record struct {
		Tool    string         `json:"tool"`
		Profile string         `json:"profile"`
		OK      bool           `json:"ok"`
		Args    map[string]any `json:"args"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("the audit line is not JSON: %v\n%s", err, lines[0])
	}
	if record.Tool != "send_message" || record.Profile != "work" || !record.OK {
		t.Errorf("the audit line does not say what happened: %s", lines[0])
	}
	if record.Args["text"] != "deploy done\nwith a newline in it" {
		t.Errorf("the audit line does not carry what was posted: %s", lines[0])
	}

	// A refusal is recorded as one. A line that said ok for every call would be
	// a log nobody could use to find the call that went wrong.
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"space": "spaces/AAA", "text": ""},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("the refusal was not recorded: %v", lines)
	}
	if strings.Contains(lines[1], `"ok":true`) {
		t.Errorf("a refused call was recorded as a success: %s", lines[1])
	}
}

// TestALongMessageIsTruncatedInTheAuditLine, because a model can send a long
// one and this goes to a terminal beside everything else on stderr.
func TestALongMessageIsTruncatedInTheAuditLine(t *testing.T) {
	var lines []string
	session := connectWith(t, Options{
		Profile:    &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
		AllowWrite: true,
		Audit:      func(line string) { lines = append(lines, line) },
	})

	long := strings.Repeat("x", maxAuditedValue*2)
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"space": "spaces/AAA", "text": long},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	if len(lines[0]) > maxAuditedValue*2 {
		t.Errorf("the audit line is %d bytes, so nothing was truncated", len(lines[0]))
	}
	if !strings.Contains(lines[0], "...") {
		t.Errorf("a truncated value does not say so: %s", lines[0])
	}
}

// TestAnAliasResolvingIntoTheAllowlistIsAllowed.
//
// The card's first recon item says the allowlist is checked after resolution
// and not before, and the tests written for it could not tell the difference:
// they used resource names on both sides, where resolution is the identity.
// Moving the check before resolution passed every one of them.
//
// This is the case that separates the two. The allowlist holds a resource name,
// the call names an alias for it, and the two strings are not equal. Checked
// before resolution, this is refused; checked after, it goes through, which is
// the whole point of an allowlist that names spaces rather than words.
func TestAnAliasResolvingIntoTheAllowlistIsAllowed(t *testing.T) {
	sent := &fake{kind: config.TransportUserOAuth, caps: full()}
	session := connectWith(t, Options{
		Profile: &profile.Open{
			Name:      "work",
			Transport: sent,
			Aliases:   map[string]string{"eng": "spaces/AAA"},
		},
		AllowWrite:  true,
		AllowSpaces: []string{"spaces/AAA"},
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"space": "eng", "text": "hello"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("an alias for an allowed space was refused: %s", contentOf(result))
	}
	if sent.sends != 1 {
		t.Errorf("sends = %d, want 1", sent.sends)
	}

	// And an alias for a space outside it is still refused, with nothing sent.
	// The two halves together are what says the check reads the resolved space
	// rather than the word.
	outside := &fake{kind: config.TransportUserOAuth, caps: full()}
	other := connectWith(t, Options{
		Profile: &profile.Open{
			Name:      "work",
			Transport: outside,
			Aliases:   map[string]string{"ops": "spaces/BBB"},
		},
		AllowWrite:  true,
		AllowSpaces: []string{"spaces/AAA"},
	})

	refused, err := other.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"space": "ops", "text": "hello"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !refused.IsError {
		t.Error("an alias for a space outside the allowlist was accepted")
	}
	if outside.sends != 0 {
		t.Errorf("the refusal arrived after %d sends", outside.sends)
	}
}

// TestEveryReadToolAnswersThroughTheSamePackagesTheCLIUses.
//
// One round trip per tool, against a connected client. The claim is not that
// the handlers work in isolation, which a direct call would show, but that each
// registered tool is reachable by name, takes the arguments its schema
// advertises, and comes back as the shape internal/rows publishes.
//
// It is a table so that a tool added later without a test is visible: the count
// at the bottom is compared against what the server registered.
func TestEveryReadToolAnswersThroughTheSamePackagesTheCLIUses(t *testing.T) {
	backing := &fake{
		kind: config.TransportUserOAuth,
		caps: full(),
		spaces: []chat.Space{{
			Name: "spaces/AAA", DisplayName: "Ops", SpaceType: "SPACE",
		}},
		members: []chat.Membership{{
			Name:        "spaces/AAA/members/m",
			State:       "JOINED",
			Role:        "ROLE_MEMBER",
			Member:      &chat.User{Name: "users/1", Type: "HUMAN"},
			Affiliation: "INTERNAL",
		}},
		messages: []chat.Message{{
			Name:       "spaces/AAA/messages/BBB",
			Text:       "deploy done",
			CreateTime: "2026-08-16T09:00:00Z",
		}},
	}
	session := connect(t, backing)

	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"list_spaces", nil, "spaces/AAA"},
		{"get_space", map[string]any{"space": "spaces/AAA"}, "Ops"},
		{"list_members", map[string]any{"space": "spaces/AAA"}, "INTERNAL"},
		{"list_messages", map[string]any{"space": "spaces/AAA"}, "deploy done"},

		// With an order, and without one. The second row is why this table
		// exists: calling every tool once found that an absent order was
		// refused with a message about a value the model never sent.
		{"list_messages", map[string]any{"space": "spaces/AAA", "order": "oldest"}, "deploy done"},
		{"get_message", map[string]any{"message": "spaces/AAA/messages/BBB"}, "deploy done"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      tc.tool,
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if result.IsError {
				t.Fatalf("%s: %s", tc.tool, contentOf(result))
			}

			body, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			if !strings.Contains(string(body), tc.want) {
				t.Errorf("%s answered %s, want it to contain %q", tc.tool, body, tc.want)
			}
		})
	}

	// Every read tool this profile registers has at least one row above. A tool
	// added without one fails here rather than shipping untested.
	if got := len(advertised(t, session)); got != 5 {
		t.Errorf("the server registered %d tools and this test exercises 5", got)
	}
}

// TestATargetThatCannotBeResolvedIsAToolErrorRatherThanAProtocolOne.
//
// A model sending a space name that does not exist should read the refusal and
// try something else. A protocol error would instead look to the client like
// the server is broken, which is a different conversation with the person
// watching.
func TestATargetThatCannotBeResolvedIsAToolErrorRatherThanAProtocolOne(t *testing.T) {
	session := connect(t, &fake{kind: config.TransportUserOAuth, caps: full()})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_space",
		Arguments: map[string]any{"space": "nothing-is-called-this"},
	})
	if err != nil {
		t.Fatalf("CallTool returned a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("a space that does not exist came back as a success")
	}
}

// TestTheReactionToolIsGatedTheSameWayTheSendToolIs.
//
// react_to_message is a write, so it carries both gates. §14.1 named it and
// milestone 5 shipped without it, which the m5-99 parity walk found; this is
// what stops it coming back as a tool that is registered when it should not be.
func TestTheReactionToolIsGatedTheSameWayTheSendToolIs(t *testing.T) {
	closed := connect(t, &fake{kind: config.TransportUserOAuth, caps: full()})
	if slices.Contains(advertised(t, closed), "react_to_message") {
		t.Error("react_to_message was registered without --allow-write")
	}

	// A capability the profile lacks keeps it out even with the flag. A webhook
	// can post and cannot react, so --allow-write on one registers send_message
	// and nothing else.
	webhook := connectWith(t, Options{
		Profile: &profile.Open{
			Name:      "alerts",
			Transport: &fake{kind: config.TransportWebhook, caps: transport.CapabilitiesFor(config.TransportWebhook)},
		},
		AllowWrite: true,
	})
	if slices.Contains(advertised(t, webhook), "react_to_message") {
		t.Error("a webhook, which cannot react, was given the reaction tool")
	}
}

// TestAReactionOutsideTheAllowlistIsRefusedBeforeTheRequest.
//
// The gap this closes is quiet rather than loud. --allow-space names spaces and
// a reaction names a message, so an allowlist that only knew how to compare
// spaces would not constrain this tool at all: the operator sets the flag,
// believes writes are confined, and reactions land anywhere the account can
// reach. The space is read out of the message name for that reason.
//
// Counted rather than read out of the error, for the reason the send test is: a
// refusal arriving after the request carries the same error as one arriving
// before it, and only one of them touched a real space.
func TestAReactionOutsideTheAllowlistIsRefusedBeforeTheRequest(t *testing.T) {
	reacted := &fake{kind: config.TransportUserOAuth, caps: full()}
	session := connectWith(t, Options{
		Profile:     &profile.Open{Name: "work", Transport: reacted},
		AllowWrite:  true,
		AllowSpaces: []string{"spaces/AAA"},
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "react_to_message",
		Arguments: map[string]any{"message": "spaces/BBB/messages/CCC", "emoji": "\U0001F44D"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("a reaction in a space outside the allowlist was accepted")
	}
	if reacted.reactions != 0 {
		t.Errorf("the refusal arrived after %d reactions", reacted.reactions)
	}

	// And a message in the allowed space goes through, because an allowlist that
	// refuses everything is not an allowlist.
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "react_to_message",
		Arguments: map[string]any{"message": "spaces/AAA/messages/CCC", "emoji": "\U0001F44D"},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if reacted.reactions != 1 {
		t.Errorf("the allowed reaction was made %d times", reacted.reactions)
	}
}

// TestTheSearchToolIsGatedOnAnIndexRatherThanACapability.
//
// §14.1's one tool whose gate is a fact about this machine rather than about
// the credential. The rule it follows is the same as every other tool's: a
// model is never shown one that cannot answer. An index with nothing in it
// would answer every search with nothing, which a model reads as "nobody said
// that" rather than as "nobody has synced anything", and those are different
// answers to different questions.
func TestTheSearchToolIsGatedOnAnIndexRatherThanACapability(t *testing.T) {
	// No index at all.
	none := connectWith(t, Options{
		Profile: &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
	})
	if slices.Contains(advertised(t, none), "search_messages") {
		t.Error("search_messages was registered with no index")
	}

	// An index that exists but holds nothing, which is what a machine that ran
	// sync and found no spaces looks like.
	empty := connectWith(t, Options{
		Profile: &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
		Index:   store.NewNDJSON(t.TempDir()),
	})
	if slices.Contains(advertised(t, empty), "search_messages") {
		t.Error("search_messages was registered against an empty index")
	}

	// And one with something in it.
	dir := t.TempDir()
	index := store.NewNDJSON(dir)
	if err := index.Append(context.Background(), "spaces/AAAATestSpace", []rows.Message{
		{Name: "spaces/AAAATestSpace/messages/AAA", CreateTime: "2026-08-17T09:00:00Z", Text: "deploy done"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	full := connectWith(t, Options{
		Profile: &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
		Index:   index,
	})
	if !slices.Contains(advertised(t, full), "search_messages") {
		t.Error("search_messages was not registered against an index holding a message")
	}
}

// TestTheSearchToolNeedsNoCapabilityAtAll.
//
// A webhook can post and can do nothing else, and it can still search what a
// user-authorized profile copied down, because the answer is on disk and no
// request is made. That is the case worth holding: this project exists for the
// population whose only credential is a webhook.
func TestTheSearchToolNeedsNoCapabilityAtAll(t *testing.T) {
	dir := t.TempDir()
	index := store.NewNDJSON(dir)
	if err := index.Append(context.Background(), "spaces/AAAATestSpace", []rows.Message{
		{Name: "spaces/AAAATestSpace/messages/AAA", CreateTime: "2026-08-17T09:00:00Z", Text: "deploy done"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	session := connectWith(t, Options{
		Profile: &profile.Open{
			Name:      "alerts",
			Transport: &fake{kind: config.TransportWebhook, caps: transport.CapabilitiesFor(config.TransportWebhook)},
		},
		Index: index,
	})
	if got := advertised(t, session); !slices.Equal(got, []string{"search_messages"}) {
		t.Fatalf("a webhook with an index serves %v, want exactly search_messages", got)
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_messages",
		Arguments: map[string]any{"query": "deploy"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("the search failed on a webhook profile: %+v", result.Content)
	}
}

// indexWith is an index holding one message in each named space, so that
// search_messages is registered and has something to find.
func indexWith(t *testing.T, spaces ...string) *store.NDJSON {
	t.Helper()

	index := store.NewNDJSON(t.TempDir())
	for _, space := range spaces {
		if err := index.Append(context.Background(), space, []rows.Message{
			{Name: space + "/messages/AAA", CreateTime: "2026-08-17T09:00:00Z", Text: "deploy done"},
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	return index
}

// TestEveryToolThatNamesASpaceIsHeldToTheAllowlist.
//
// `--allow-space` narrowed writes only, and its own help said it "narrows it
// further" without saying which half. So an operator who confined a server to
// one space had confined half of it: every read tool still reached everything
// the profile could, which is the larger surface. Message bodies are hostile
// input by this project's own threat model, and a model talked into something
// by one is a model that can read the rest.
//
// Walked per tool rather than asserted once, because the five that name a space
// reach it three different ways: three resolve an argument, get_message reads it
// out of a message resource name, and search_messages narrows "everything"
// rather than refusing. A test naming one of them holds one of them.
func TestEveryToolThatNamesASpaceIsHeldToTheAllowlist(t *testing.T) {
	const outside = "spaces/BBB"

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"get_space", map[string]any{"space": outside}},
		{"list_members", map[string]any{"space": outside}},
		{"list_messages", map[string]any{"space": outside}},
		{"get_message", map[string]any{"message": outside + "/messages/CCC"}},
		{"search_messages", map[string]any{"query": "deploy", "space": outside}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			reached := &fake{kind: config.TransportUserOAuth, caps: full()}
			session := connectWith(t, Options{
				Profile:     &profile.Open{Name: "work", Transport: reached},
				AllowWrite:  true,
				AllowSpaces: []string{"spaces/AAA"},
				Index:       indexWith(t, outside),
			})

			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      tc.tool,
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !result.IsError {
				t.Fatalf("%s answered for a space outside the allowlist: %s", tc.tool, contentOf(result))
			}
			if !strings.Contains(contentOf(result), outside) {
				t.Errorf("the refusal does not name the space:\n%s", contentOf(result))
			}
		})
	}
}

// TestListingSpacesUnderAnAllowlistShowsOnlyThose.
//
// Filtered rather than refused, because a model asking what it can reach should
// be answered with what it can reach. Listing a space it may not touch would
// publish the name and the display name of a room the operator confined it out
// of, which is a smaller leak than reading the messages and is still one.
func TestListingSpacesUnderAnAllowlistShowsOnlyThose(t *testing.T) {
	session := connectWith(t, Options{
		Profile: &profile.Open{Name: "work", Transport: &fake{
			kind:   config.TransportUserOAuth,
			caps:   full(),
			spaces: []chat.Space{{Name: "spaces/AAA"}, {Name: "spaces/BBB"}},
		}},
		AllowSpaces: []string{"spaces/AAA"},
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_spaces"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_spaces: %s", contentOf(result))
	}

	body := contentOf(result)
	if !strings.Contains(body, "spaces/AAA") {
		t.Errorf("the allowed space is missing:\n%s", body)
	}
	if strings.Contains(body, "spaces/BBB") {
		t.Errorf("a space outside the allowlist was listed:\n%s", body)
	}
}

// TestASearchOverMCPSaysWhatTheIndexWouldNotAnswerWith.
//
// internal/store refuses a record whose space disagrees with the file it was
// read from, because a record read from the wrong file would answer for a space
// it was never in and would move where a sync resumes. It says so rather than
// skipping silently: the index is the only copy of a message that no longer
// exists anywhere else, so one it holds and will not return is worth a
// sentence.
//
// The CLI printed that sentence and this server did not. The warnings sat in
// the index for the life of the session and nothing ever read them, so a search
// over a copied or restored file answered narrowly and reported the narrow
// answer as the whole one. That is the truncation rule broken at the one
// consumer that cannot check: a person reading a short list can wonder, and a
// model hands it on as fact.
//
// The mechanism was already built for this caller. NDJSON.Warnings names the
// MCP server in its doc comment, and the mutex guarding the list exists because
// search over MCP is served concurrently. Only the call was missing.
func TestASearchOverMCPSaysWhatTheIndexWouldNotAnswerWith(t *testing.T) {
	const space = "spaces/AAAATestSpace"
	const other = "spaces/AAAAOtherSpace"

	// Written by hand rather than through Append, which cannot produce a file
	// like this: it arrives from a restored backup, a directory copied between
	// machines, or somebody tidying by hand.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spaces"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	lines := strings.Join([]string{
		`{"space":"` + space + `","message":{"name":"` + space + `/messages/AAA",` +
			`"create_time":"2026-08-17T09:00:00Z","text":"deploy done"}}`,
		`{"space":"` + other + `","message":{"name":"` + other + `/messages/BBB",` +
			`"create_time":"2026-08-17T09:01:00Z","text":"deploy elsewhere"}}`,
		`{"space":"` + other + `","message":{"name":"` + other + `/messages/CCC",` +
			`"create_time":"2026-08-17T09:02:00Z","text":"deploy elsewhere again"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "spaces", "AAAATestSpace.ndjson"), []byte(lines), 0o600); err != nil {
		t.Fatalf("planting the file: %v", err)
	}

	session := connectWith(t, Options{
		Profile: &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
		Index:   store.NewNDJSON(dir),
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_messages",
		Arguments: map[string]any{"query": "deploy"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("the search failed: %s", contentOf(result))
	}

	var got struct {
		Messages []rows.Message `json:"messages"`
		Searched []string       `json:"searched"`
		Skipped  []string       `json:"skipped"`
	}
	body, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshalling the structured content: %v", err)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding the structured content: %v", err)
	}

	// The narrow answer itself is correct: the two foreign records are not in
	// it. What was missing is the tool saying so.
	if len(got.Messages) != 1 {
		t.Errorf("the search returned %d messages, want the one that belongs to the file:\n%s", len(got.Messages), body)
	}

	if len(got.Skipped) != 1 {
		t.Fatalf("the result carries %d skipped lines, want exactly one for the one file:\n%s", len(got.Skipped), body)
	}
	for _, want := range []string{"AAAATestSpace.ndjson", "2 record(s)", "another space"} {
		if !strings.Contains(got.Skipped[0], want) {
			t.Errorf("the skipped line does not mention %q:\n%s", want, got.Skipped[0])
		}
	}
}

// TestASearchThatSkippedNothingSaysNothing.
//
// The other half, and the half that keeps the first one worth reading. A field
// that is always populated is a field nobody checks, so an ordinary index has
// to produce no skipped line at all rather than an empty one.
func TestASearchThatSkippedNothingSaysNothing(t *testing.T) {
	session := connectWith(t, Options{
		Profile: &profile.Open{Name: "work", Transport: &fake{kind: config.TransportUserOAuth, caps: full()}},
		Index:   indexWith(t, "spaces/AAAATestSpace"),
	})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_messages",
		Arguments: map[string]any{"query": "deploy"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("the search failed: %s", contentOf(result))
	}

	body, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshalling the structured content: %v", err)
	}
	if strings.Contains(string(body), "skipped") {
		t.Errorf("a clean index produced a skipped field, which is a line every caller learns to ignore:\n%s", body)
	}
}
