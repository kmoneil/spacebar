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
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/mcpsrv"
	"github.com/kmoneil/spacebar/internal/meta"
)

func newMCPCmd(opts *Options) *cobra.Command {
	var (
		allowWrite  bool
		allowSpaces []string
	)

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve this profile to a model over MCP",
		Long: `Serve this profile to a model over MCP, on stdin and stdout.

Reading only, unless --allow-write is given.

Not a command to run by hand. An MCP client starts it, speaks JSON-RPC to it
over the pipe, and stops it when the session ends. In a client's configuration
it is the command and the profile is the argument:

  {"command": "` + meta.AppName + `", "args": ["mcp", "--profile", "work"]}

stdout carries the protocol and nothing else, so anything this command has to
say goes to stderr, including which tools it registered.

A tool this profile cannot serve is not registered at all, rather than
registered and failing when it is called. A model that cannot see a tool cannot
argue itself into calling it, and one that can see a broken tool will call it,
be refused, try again differently, and eventually tell you the tool is broken.
That is the opposite of how flags work in the rest of this tool, and it is
deliberate: a person reading --help is served by knowing a flag exists.

Writes are off unless --allow-write says otherwise, and that is the default
worth understanding. A model with a message-sending tool and no gate is one
confused turn away from posting to a company-wide space, and the people who
find out are everybody in it. Without the flag, send_message is not registered
at all, so there is no tool for a model to talk itself into calling.

--allow-space, repeatable, narrows it further: a write to any other space is
refused before the request. It takes space resource names, because it is
checked against what a tool call resolves to rather than against what the model
typed, and an alias here would be an allowlist whose meaning depends on what
the API says at the time.

Every tool call is written to stderr as one JSON line, whatever --quiet and
--json say. It records the tool, the profile, the arguments with long strings
truncated, and whether it worked. Nothing in it is a credential.

Message bodies are written by people, some of them outside your organization.
They reach a model as data, and this tool escapes what it renders, but nothing
here can make the text itself trustworthy: a message that asks the model to do
something is still a message.`,

		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}

			// The index is opened rather than required. A machine that has
			// never run `sync` has none, and that is a normal state: the
			// search tool is simply not registered, which is the same answer
			// every other unavailable tool gets.
			index, err := openIndex()
			if err != nil {
				return err
			}

			server, err := mcpsrv.New(mcpsrv.Options{
				Profile:     opened,
				AllowWrite:  allowWrite,
				AllowSpaces: allowSpaces,
				Index:       index,
				Audit:       r.Audit,
			})
			if err != nil {
				return err
			}

			// On stderr, because stdout is the protocol. It is the one line that
			// says what a model is about to be offered, which is the question
			// somebody debugging a client configuration is asking.
			r.Note("serving %s as profile %q: %s",
				meta.AppName, opened.Name, strings.Join(server.Tools(), ", "))

			// The same signal handling as tail, and for the same reason: a client
			// that stops the server is ending it the way it is meant to end, so
			// the exit code is this command's own rather than the signal's.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return server.Run(ctx, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}

	f := cmd.Flags()
	f.BoolVar(&allowWrite, "allow-write", false,
		"register the tools that change something in a space")
	f.StringArrayVar(&allowSpaces, "allow-space", nil,
		"restrict writes to this space resource name; repeatable")

	return cmd
}
