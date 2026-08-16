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
	"github.com/kmoneil/spacebar/internal/meta"
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
