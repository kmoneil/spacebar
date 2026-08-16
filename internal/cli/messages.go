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
	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/format"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/transport"
)

// The values --order takes are chat.OrderNewest and chat.OrderOldest, and the
// translation to the API's own strings is there rather than here: the MCP
// server takes the same argument, and a second copy of a two-case switch is how
// two adapters end up disagreeing about what "oldest" means.

func newMessagesCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "List and read messages",
		Long: `List and read messages.

Both need a profile that can read, which means a profile authorized as you. An
incoming webhook is write-only, so neither works on one, and each says so
before making a request.`,
	}

	cmd.AddCommand(
		newMessagesListCmd(opts),
		newMessagesGetCmd(opts),
		newMessagesEditCmd(opts),
		newMessagesDeleteCmd(opts),
		newMessagesDownloadCmd(opts),
	)
	return cmd
}

func newMessagesListCmd(opts *Options) *cobra.Command {
	var (
		limit       int
		order       string
		filter      string
		since       string
		until       string
		showDeleted bool
		refresh     bool
	)

	cmd := &cobra.Command{
		Use:   "list SPACE",
		Short: "List messages in a space",
		Long: `List messages in a space.

  ` + meta.AppName + ` messages list spaces/AAAAAAA
  ` + meta.AppName + ` messages list eng-alerts                 # an alias
  ` + meta.AppName + ` messages list 'Ops'                      # a display name
  ` + meta.AppName + ` messages list spaces/AAAAAAA --json | jq -r .text
  ` + meta.AppName + ` messages list eng-alerts --since 2h        # the last two hours
  ` + meta.AppName + ` messages list eng-alerts --since 2026-08-16T09:00:00Z --until 2026-08-16T17:00:00Z

Newest first, so that the default limit returns the latest messages rather than
the oldest ones in the space's history. --order oldest reverses it, and reading
a conversation in the order it happened is what ` + meta.AppName + ` tail will be for.

--since and --until take an RFC 3339 timestamp or how long ago, as in 90m or
2h, and both are strict: the API compares createTime with > and <, and refuses
>=, so a message posted at exactly the boundary is not returned. They combine
with --filter rather than replacing it, and your expression keeps its own
meaning, because it is parenthesized before anything is added to it.

Columns are the creation time, who sent it, and the text, separated by a tab.
The text is Chat markup exactly as the API returned it, and control characters
in it are escaped before they reach a terminal.`,

		Args: exactlyOne("messages list needs a space.\n  %s messages list spaces/AAAAAAA"),
		RunE: func(cmd *cobra.Command, args []string) error {
			orderBy, err := chat.OrderBy(order)
			if err != nil {
				return err
			}

			// Both parsed before the profile is loaded, so a mistyped time
			// costs no keyring read and no request.
			from, err := parseSince(since)
			if err != nil {
				return err
			}
			to, err := parseSince(until)
			if err != nil {
				return err
			}
			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "messages list", transport.CanRead); err != nil {
				return err
			}

			space, err := opened.Resolve(cmd.Context(), args[0], refresh)
			if err != nil {
				return err
			}

			return finish(r, opened, stream(r, opened.Transport.Messages(cmd.Context(), chat.ListMessagesRequest{
				Space:       space,
				OrderBy:     orderBy,
				Filter:      filter,
				Since:       from,
				Until:       to,
				ShowDeleted: showDeleted,
				Limit:       limit,
			}), rows.ForMessage))
		},
	}

	f := cmd.Flags()
	f.IntVar(&limit, "limit", defaultLimit, limitHelp)
	f.StringVar(&order, "order", chat.OrderNewest, "newest or oldest first")
	f.StringVar(&filter, "filter", "", "the API's own filter expression, passed through unaltered")
	f.StringVar(&since, "since", "", sinceHelp)
	f.StringVar(&until, "until", "", untilHelp)
	f.BoolVar(&showDeleted, "show-deleted", false, "include tombstones for deleted messages")
	addRefreshFlag(cmd, &refresh)
	return cmd
}

func newMessagesGetCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get MESSAGE",
		Short: "Read one message",
		Long: `Read one message.

  ` + meta.AppName + ` messages get spaces/AAAAAAA/messages/BBBBBBB

The argument is a message resource name, which is what ` + meta.AppName + ` send
reports and what the name column of ` + meta.AppName + ` messages list --json holds.`,

		Args: exactlyOne("messages get needs a message.\n  %s messages get spaces/AAAAAAA/messages/BBBBBBB"),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "messages get", transport.CanRead); err != nil {
				return err
			}

			message, err := opened.Transport.GetMessage(cmd.Context(), args[0])
			if err != nil {
				return finish(r, opened, err)
			}

			row, _ := rows.ForMessage(*message)
			return r.Block(row, row.Text)
		},
	}
}

func newMessagesEditCmd(opts *Options) *cobra.Command {
	var md bool

	cmd := &cobra.Command{
		Use:   "edit MESSAGE TEXT",
		Short: "Replace a message's text",
		Long: `Replace a message's text.

  ` + meta.AppName + ` messages edit spaces/AAAAAAA/messages/BBBBBBB 'deploy done, finally'

The argument is a message resource name, which is what ` + meta.AppName + ` send
reports and what the name field of ` + meta.AppName + ` messages list --json holds.

The text replaces the whole body rather than patching it. Chat markup is not
CommonMark, so --md translates the same way it does on send, and the
translation is one way: feeding its output back through --md means something
else.

Editing is limited to messages this account sent, measured rather than assumed:
editing your own answers 200 and editing somebody else's answers 403, in the
same space, on the same token, a second apart. The refusal comes from the API
rather than from this tool, because whose message it is is not something this
tool can know without asking.`,

		Args: exactlyTwo("messages edit needs a message and the new text.\n" +
			"  %s messages edit spaces/AAAAAAA/messages/BBBBBBB 'the new text'"),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := renderer(cmd, opts)

			text, warnings, err := format.Body(args[1], md)
			if err != nil {
				return err
			}
			r.Warnings(warnings)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "messages edit", transport.CanEdit); err != nil {
				return err
			}

			edited, err := opened.Transport.EditMessage(cmd.Context(), chat.EditRequest{
				Message: args[0],
				Text:    text,
			})
			if err != nil {
				return finish(r, opened, err)
			}

			row, _ := rows.ForMessage(*edited)
			return r.Result(row, output.Fields{
				{Label: "edited", Value: edited.Name},
				{Label: "text", Value: edited.Text},
			})
		},
	}

	cmd.Flags().BoolVar(&md, "md", false, "translate the body from CommonMark into Chat markup")
	return cmd
}

func newMessagesDeleteCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete MESSAGE",
		Aliases: []string{"rm"},
		Short:   "Delete a message",
		Long: `Delete a message.

  ` + meta.AppName + ` messages delete spaces/AAAAAAA/messages/BBBBBBB

This asks first, because it is the one command in this tool that destroys
something in a space rather than adding to it, and there is no undo: a deleted
message is gone for everybody who could see it.

With stdin not a terminal there is nobody to ask, so it exits 7 rather than
prompting. --yes answers in advance, which is what a script does.

Deleting is not limited to your own messages the way editing is. This account
deleted a message sent by somebody else, in a space where it is a manager, and
the API allowed it. So read the name twice before you type this: what stops a
mistake here is the confirmation and nothing else.`,

		Args: exactlyOne("messages delete needs a message.\n" +
			"  %s messages delete spaces/AAAAAAA/messages/BBBBBBB"),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "messages delete", transport.CanDelete); err != nil {
				return err
			}

			// After the capability check and before the request. "This profile
			// cannot delete" is a better first answer than a question about
			// something that was never going to happen.
			if err := r.Confirm(cmd.InOrStdin(), "Delete %s? This cannot be undone.", args[0]); err != nil {
				return err
			}

			if err := opened.Transport.DeleteMessage(cmd.Context(), args[0]); err != nil {
				return finish(r, opened, err)
			}

			return r.Result(map[string]any{"name": args[0], "deleted": true},
				output.Fields{{Label: "deleted", Value: args[0]}})
		},
	}
}
