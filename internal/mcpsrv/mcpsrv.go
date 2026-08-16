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
	"io"
	"iter"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
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
}

// Options is what the server needs to exist.
type Options struct {
	// Profile is an opened profile: its transport, its name, and its aliases.
	Profile *profile.Open
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

	s := &Server{
		srv: mcp.NewServer(&mcp.Implementation{
			Name:    meta.AppName,
			Version: meta.Version,
		}, nil),
		profile: opts.Profile,
	}

	caps := opts.Profile.Transport.Capabilities()
	register(s, caps, transport.CanListSpaces, listSpacesTool, s.listSpaces)
	register(s, caps, transport.CanRead, getSpaceTool, s.getSpace)
	register(s, caps, transport.CanReadMembers, listMembersTool, s.listMembers)
	register(s, caps, transport.CanRead, listMessagesTool, s.listMessages)
	register(s, caps, transport.CanRead, getMessageTool, s.getMessage)

	if len(s.tools) == 0 {
		return nil, noToolsErr(opts.Profile.Name, opts.Profile.Transport.Kind())
	}
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
func noToolsErr(name string, kind config.Transport) error {
	return output.Errorf("UNSUPPORTED", output.ExitUnsupported,
		"profile %q cannot serve any MCP tool, so there is nothing to run.\n"+
			"Every tool in this build reads, and %q is an incoming webhook, which is write-only.\n"+
			"Use a profile authorized as you:\n"+
			"  %s auth setup --profile NAME < client_secret.json\n"+
			"  %s auth login --profile NAME",
		name, name, meta.AppName, meta.AppName)
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
