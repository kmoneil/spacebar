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
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
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
}

func (f *fake) Kind() config.Transport               { return f.kind }
func (f *fake) Profile() string                      { return "work" }
func (f *fake) Capabilities() transport.Capabilities { return f.caps }
func (f *fake) Send(context.Context, chat.SendRequest) (*chat.Message, error) {
	return nil, errors.New("the fake does not send")
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

func (f *fake) React(context.Context, chat.ReactRequest) (*chat.Reaction, error) {
	return nil, errors.New("the fake does not react")
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

	server, err := New(Options{Profile: &profile.Open{Transport: f, Name: "work"}})
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
