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

// Package mcpsrv serves this tool's read paths to a model over MCP.
//
// A thin adapter, like internal/cli and for the same reason (SPEC.md §4): the
// resolution, the capability gate, the pagination and the published shapes all
// live in the packages the CLI uses, so a behaviour that differs between the
// two is a bug rather than a feature of one of them.
//
// The rule that shapes this package is §14.1's: a tool whose capability is
// unavailable is not registered at all, rather than registered and returning an
// error. A model that cannot see a tool cannot argue itself into calling it,
// and a model that can see one will call it, be refused, try again differently,
// and eventually tell the person that the tool is broken. That is the opposite
// of the CLI's rule for flags, where an unregistered --file would say the tool
// cannot do attachments at all rather than that this profile cannot, and the
// difference is who is reading: a person is served by knowing the flag exists.
package mcpsrv

import (
	"context"
	"encoding/json"
	"io"
	"iter"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/store"
	"github.com/kmoneil/spacebar/internal/transport"
)

// defaultLimit is how many items a list tool returns when the caller does not
// say, and it is the CLI's number for the CLI's reason: the common case is
// somebody finding out what is going on in a space, and a default of everything
// would spend a per-space quota shared with every other app acting in it.
const defaultLimit = 25

// maxLimit bounds what a caller can ask for in one tool call.
//
// A list arrives as one JSON document rather than as a stream, so it is read
// into memory here and again by whatever is on the other end, and a model's
// context is the smaller of the two. The CLI has no such bound because it
// streams: a person piping a thousand messages into a file is fine, and a
// thousand messages arriving in one tool result is a context window spent on
// one call.
const maxLimit = 200

// Server is a configured MCP server, before it is connected to anything.
type Server struct {
	srv     *mcp.Server
	profile *profile.Open
	tools   []string

	// allowed is the space allowlist, empty when every space is allowed. Held
	// as a set because the check runs on every write.
	allowed map[string]bool

	// index is the local message index, nil when there is none. See indexed.
	index *store.NDJSON

	audit func(string)
}

// Options is what the server needs to exist.
type Options struct {
	// Profile is an opened profile: its transport, its name, and its aliases.
	Profile *profile.Open

	// AllowWrite registers the tools that change something in a space. Off by
	// default, and the default is the point: a model with a message-sending
	// tool and no gate is one confused turn away from posting to a company-wide
	// announcements space, and the people who find out are everybody in it.
	AllowWrite bool

	// AllowSpaces restricts writes to these space resource names. Empty means
	// every space this profile can reach, which is what --allow-write on its
	// own asks for.
	AllowSpaces []string

	// Audit receives one line per tool call. Never nil in the command; a nil
	// one here means a test that is not asserting on the log.
	Audit func(string)

	// Index is the local message index, and search_messages is registered only
	// when it holds something. That makes it the one tool in SPEC.md §14.1
	// gated on a fact about this machine rather than on a capability of the
	// credential, and the rule is the same either way: a model is never shown a
	// tool that cannot answer. An index with no spaces in it would answer every
	// search with nothing, which reads to a model as "there is no such message"
	// rather than as "nobody has synced anything".
	//
	// Nil means no index, which is what a caller that has not built one passes.
	Index *store.NDJSON
}

// New builds a server with exactly the tools this profile can serve.
//
// A profile that can serve none of them is a refusal rather than an empty
// server. The alternative is a session that connects, offers nothing, and
// leaves the person to work out from a model's confusion that their profile is
// a write-only webhook. Exit 5 with the name of the profile is the same answer
// the CLI gives, arriving at the only moment this command can give it.
func New(opts Options) (*Server, error) {
	if opts.Profile == nil || opts.Profile.Transport == nil {
		return nil, output.Errorf("CONFIG", output.ExitUsage,
			"the MCP server needs an opened profile.")
	}

	allowed, err := allowlist(opts.AllowSpaces)
	if err != nil {
		return nil, err
	}

	s := &Server{
		srv: mcp.NewServer(&mcp.Implementation{
			Name:    meta.AppName,
			Version: meta.Version,
		}, nil),
		profile: opts.Profile,
		allowed: allowed,
		index:   opts.Index,
		audit:   opts.Audit,
	}

	caps := opts.Profile.Transport.Capabilities()
	register(s, caps, transport.CanListSpaces, listSpacesTool, s.listSpaces)
	register(s, caps, transport.CanRead, getSpaceTool, s.getSpace)
	register(s, caps, transport.CanReadMembers, listMembersTool, s.listMembers)
	register(s, caps, transport.CanRead, listMessagesTool, s.listMessages)
	register(s, caps, transport.CanRead, getMessageTool, s.getMessage)

	// The write tools, behind the flag as well as behind the capability. Two
	// gates rather than one, because they answer different questions: the
	// capability says this profile could, and the flag says this operator
	// agreed to let a model.
	if opts.AllowWrite {
		register(s, caps, transport.CanSend, sendMessageTool, s.sendMessage)
		register(s, caps, transport.CanReact, reactToMessageTool, s.reactToMessage)
	}

	// The index tool, gated on there being an index rather than on a
	// capability. It needs no transport at all: a webhook profile can search
	// what a user-authorized one copied down, because the answer is on disk.
	if s.indexed() {
		mcp.AddTool(s.srv, searchMessagesTool, s.searchMessages)
		s.tools = append(s.tools, searchMessagesTool.Name)
	}

	if len(s.tools) == 0 {
		return nil, noToolsErr(opts.Profile.Name, opts.Profile.Transport.Kind(), opts.AllowWrite)
	}

	s.srv.AddReceivingMiddleware(s.auditing)
	return s, nil
}

// Tools is what was registered, in registration order.
//
// Exported so that the claim can be asserted against the server rather than
// against the test's own idea of it, and so that the command can say on stderr
// what a model is about to be offered.
func (s *Server) Tools() []string { return append([]string(nil), s.tools...) }

// Run serves the protocol over one pair of streams until the peer disconnects
// or the context is cancelled.
//
// The streams are passed in rather than taken from the process, which is what
// keeps the rule that only internal/output may name os.Stdout true here as much
// as everywhere else. For `spacebar mcp` they are the ones cobra handed the
// command; for a test they are an in-memory pair.
//
// Nothing but the protocol may be written to that writer. stdout carries JSON-RPC
// framing, so a stray line anywhere in this process would corrupt a session
// rather than merely being noise, which is the sharpest version of the
// stdout-is-data rule this repository has.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	return s.srv.Run(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(in),

		// Not closed by this package. The writer belongs to the caller, and on
		// the real invocation it is the process's stdout.
		Writer: nopCloser{out},
	})
}

// register adds a tool when the profile can serve it, and records the name.
//
// Generic over the tool's input and output types so that the schema the SDK
// infers is the handler's own signature, rather than a second description of it
// that can disagree.
func register[In, Out any](s *Server, caps transport.Capabilities, needs transport.Capability,
	tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out],
) {
	if !caps.Has(needs) {
		return
	}
	mcp.AddTool(s.srv, tool, handler)
	s.tools = append(s.tools, tool.Name)
}

// noToolsErr is what a profile that can serve nothing is told.
//
// Which advice depends on why. A webhook with no --allow-write can serve
// exactly one tool and was not asked to, so the fix is the flag; a webhook that
// was asked to has it, and cannot read, so the fix is a different profile.
func noToolsErr(name string, kind config.Transport, allowWrite bool) error {
	fix := "Use a profile authorized as you:\n" +
		"  " + meta.AppName + " auth setup --profile NAME < client_secret.json\n" +
		"  " + meta.AppName + " auth login --profile NAME"
	if kind == config.TransportWebhook && !allowWrite {
		fix = "A webhook can post, which is a write, so this needs --allow-write:\n" +
			"  " + meta.AppName + " mcp --profile " + name + " --allow-write"
	}

	return output.Errorf("UNSUPPORTED", output.ExitUnsupported,
		"profile %q cannot serve any MCP tool, so there is nothing to run.\n%s", name, fix)
}

// allowlist turns the --allow-space values into the set writes are checked
// against.
//
// Resource names only, and an alias or a display name is refused here rather
// than resolved. An allowlist entry that needs resolving is an allowlist whose
// meaning depends on what the API says at the moment it is consulted, and the
// thing it is guarding is which space a model may post to.
func allowlist(spaces []string) (map[string]bool, error) {
	if len(spaces) == 0 {
		return nil, nil
	}

	allowed := make(map[string]bool, len(spaces))
	for _, space := range spaces {
		if err := chat.CheckSpaceName(space); err != nil {
			return nil, output.Errorf("USAGE", output.ExitUsage,
				"--allow-space takes a space resource name, and %q is not one.\n"+
					"It is checked against what a tool call resolves to, so an alias or a display name "+
					"here would be an allowlist whose meaning depends on what the API says at the time.\n"+
					"%s spaces list prints the names.", space, meta.AppName)
		}
		allowed[space] = true
	}
	return allowed, nil
}

// checkAllowed refuses a tool call that touches a space outside the allowlist.
//
// Called after resolution and never before it. An allowlist checked against
// what the caller typed is checked against a string the caller controls; the
// space that will actually be reached is the one that arrives out of
// internal/resolve, and only that one is worth comparing.
//
// **Reads as well as writes**, which they were not. `--allow-space` narrowed
// only what a model could put into a space, and its own help said it "narrows
// it further" without saying which half. An operator who confined a server to
// one space had confined half of it: every read tool still reached everything
// the profile could, which is the larger surface, because message bodies are
// hostile input by this project's own threat model and a model that has been
// talked into something by one is a model that can read the rest.
//
// Extending the flag rather than adding a second one, and the reason is in the
// alternative: two flags that both take space names is the shape somebody sets
// one of and believes they set both. Nothing is released, so there is no agent
// whose reach this silently narrows, and one flag now means the thing an
// operator reading it already assumed.
func (s *Server) checkAllowed(space string) error {
	if len(s.allowed) == 0 || s.allowed[space] {
		return nil
	}
	return output.Errorf("REFUSED", output.ExitRefused,
		"%s is not in this server's --allow-space list, so nothing there was read or written.", space)
}

// allows reports whether a space may be touched at all.
//
// The same question checkAllowed asks, for the two tools that filter rather
// than refuse. A model that asked what it can reach is answered with what it
// can reach, and a search across "everything" means everything it is allowed.
func (s *Server) allows(space string) bool {
	return len(s.allowed) == 0 || s.allowed[space]
}

// collect reads a bounded number of items out of a streaming list.
//
// It asks for one more than the caller wanted so that "there are more" is
// something observed rather than guessed at. A model handed twenty-five
// messages with no way to know whether that is the whole space would either
// treat a page as the conversation or ask again forever; this is one extra item
// on one page, and it is the difference between an answer and a guess.
//
// An error ends the call. The CLI can write the rows it already has and exit
// non-zero, because a person or a shell pipeline can act on both halves; a tool
// result is one document, and a partial one with a success code is the failure
// mode the truncation rule exists to prevent.
func collect[T any](seq iter.Seq2[T, error], limit int) ([]T, bool, error) {
	out := make([]T, 0, limit)
	for item, err := range seq {
		if err != nil {
			return nil, false, err
		}
		out = append(out, item)
		if len(out) > limit {
			return out[:limit], true, nil
		}
	}
	return out, false, nil
}

// limitOf turns what a caller asked for into what will be fetched.
//
// Zero means "did not say", which is the default rather than "everything": a
// tool argument left out is not an instruction, and the CLI's --limit 0 spelling
// for everything would make an omitted field mean an unbounded fetch.
func limitOf(asked int) (int, error) {
	switch {
	case asked < 0:
		return 0, output.Errorf("USAGE", output.ExitUsage,
			"limit is %d; it counts items, so it cannot be negative.", asked)
	case asked == 0:
		return defaultLimit, nil
	case asked > maxLimit:
		return 0, output.Errorf("USAGE", output.ExitUsage,
			"limit is %d, and one tool call returns at most %d.\n"+
				"A list arrives as one document rather than as a stream, so a larger answer "+
				"costs the model's context rather than the terminal's scrollback. "+
				"Ask again with the window this one ended at.", asked, maxLimit)
	}
	return asked, nil
}

// resolve turns a space argument into a resource name, exactly as the CLI does.
func (s *Server) resolve(ctx context.Context, space string) (string, error) {
	if space == "" {
		return "", output.Errorf("USAGE", output.ExitUsage, "no space was given.")
	}
	return s.profile.Resolve(ctx, space, false)
}

// nopCloser makes a writer satisfy the transport's io.WriteCloser without
// giving it the power to close the caller's stream. On the real invocation that
// stream is the process's stdout, and closing it would end far more than a
// session.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// auditing writes one line per tool call to stderr, per SPEC.md §14.2.
//
// A middleware rather than a wrapper on each handler, so that a tool added
// later is logged without anybody remembering to log it. It records the call
// rather than the result: what a person auditing this needs is what the model
// asked for and whether it worked.
//
// The arguments are included because for a write they are the message, and an
// audit log of "send_message was called" answers none of the questions somebody
// asks it. They are truncated, because a model can send a long one and this
// goes to a terminal.
//
// Nothing here can carry a credential. The arguments come from the model, the
// profile name is not a secret, and the response is deliberately not logged.
func (s *Server) auditing(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		// The raw params, which is what a server middleware is handed: the
		// typed CallToolParams is what a client sends, and by the time this
		// runs the arguments are still the bytes that arrived. Measured rather
		// than assumed, because the two types differ by one word and the audit
		// log silently recorded nothing when this matched on the wrong one.
		call, ok := req.GetParams().(*mcp.CallToolParamsRaw)
		if !ok || s.audit == nil {
			return next(ctx, method, req)
		}

		result, err := next(ctx, method, req)
		s.audit(auditLine(s.profile.Name, call, result, err))
		return result, err
	}
}

// maxAuditedValue bounds one string in the audit line.
const maxAuditedValue = 256

// auditLine builds the JSON object one call is recorded as.
func auditLine(profile string, call *mcp.CallToolParamsRaw, result mcp.Result, err error) string {
	record := map[string]any{
		"tool":    call.Name,
		"profile": profile,
		"ok":      err == nil,
	}
	if err != nil {
		record["error"] = truncate(err.Error())
	}
	if called, ok := result.(*mcp.CallToolResult); ok && called != nil && called.IsError {
		// A tool error is packed into the result rather than returned, so a
		// line that only looked at err would record every refusal as a success.
		record["ok"] = false
	}
	if args := auditedArgs(call.Arguments); args != nil {
		record["args"] = args
	}

	// encoding/json escapes everything below U+0020, so a message body cannot
	// break the line it is written on.
	line, marshalErr := json.Marshal(record)
	if marshalErr != nil {
		return `{"tool":"` + call.Name + `","profile":"` + profile + `","ok":false,"error":"the audit line could not be encoded"}`
	}
	return string(line)
}

// auditedArgs is the call's arguments with every string bounded.
//
// Decoded rather than passed through, so that a long message body is bounded
// before it reaches a terminal. A body that will not decode is recorded as
// nothing rather than as itself: the arguments come from a model, and the line
// they land on is one this tool promises is a single JSON object.
func auditedArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}

	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}

	out := make(map[string]any, len(fields))
	for name, value := range fields {
		if text, isText := value.(string); isText {
			out[name] = truncate(text)
			continue
		}
		out[name] = value
	}
	return out
}

func truncate(value string) string {
	runes := []rune(value)
	if len(runes) <= maxAuditedValue {
		return value
	}
	return string(runes[:maxAuditedValue]) + "..."
}

// indexed reports whether the local index holds anything worth searching.
//
// Anything, not merely existing: a directory with no spaces in it answers every
// search with nothing, and a model handed an empty answer reads it as "nobody
// said that" rather than as "nobody synced this". The distinction is the whole
// reason the gate is registration and not a runtime error.
func (s *Server) indexed() bool {
	if s.index == nil {
		return false
	}
	spaces, err := s.index.Spaces()
	return err == nil && len(spaces) > 0
}
