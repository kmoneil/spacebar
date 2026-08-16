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
	"strings"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
)

// The values --order takes. Short words rather than the API's own strings,
// because "createTime DESC" is a shell-quoting problem and an implementation
// detail of an endpoint this tool exists to hide.
const (
	orderNewest = "newest"
	orderOldest = "oldest"
)

// messageRow is the --json shape of one message.
//
// Chosen here rather than passed through from the wire, so that a field the API
// adds does not silently become part of this tool's contract. Text is the
// message as Chat markup, unaltered: this tool does not rewrite a value to make
// it easier to read, and a body that went out as Chat markup comes back as Chat
// markup.
type messageRow struct {
	Name       string `json:"name"`
	CreateTime string `json:"create_time,omitempty"`

	// LastUpdateTime is set once a message has been edited, and its presence is
	// the only way to tell an edited message from an original one.
	LastUpdateTime string `json:"last_update_time,omitempty"`

	Sender      string `json:"sender,omitempty"`
	DisplayName string `json:"sender_display_name,omitempty"`
	SenderType  string `json:"sender_type,omitempty"`

	Thread      string `json:"thread,omitempty"`
	ThreadReply bool   `json:"thread_reply,omitempty"`

	Text string `json:"text,omitempty"`
}

func rowForMessage(m chat.Message) (any, []string) {
	row := messageRow{
		Name:           m.Name,
		CreateTime:     m.CreateTime,
		LastUpdateTime: m.LastUpdateTime,
		ThreadReply:    m.ThreadReply,
		Text:           m.Text,
	}
	if m.Sender != nil {
		row.Sender = m.Sender.Name
		row.DisplayName = m.Sender.DisplayName
		row.SenderType = m.Sender.Type
	}
	if m.Thread != nil {
		row.Thread = m.Thread.Name
	}

	// The display name is preferred in the text column and the resource name is
	// the fallback, which is the opposite of how --json orders them. A person
	// reading a terminal wants to know who said it; a script wants a value it
	// can compare, and --json carries both.
	who := row.DisplayName
	if who == "" {
		who = row.Sender
	}

	// output.Cell escapes the tab and the newline, so a message body cannot
	// forge a column or a row here. That is why the body can be a column at all.
	return row, []string{m.CreateTime, who, m.Text}
}

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
			orderBy, err := orderByFor(order)
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

			space, err := resolveTarget(cmd.Context(), opened, args[0], refresh)
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
			}), rowForMessage))
		},
	}

	f := cmd.Flags()
	f.IntVar(&limit, "limit", defaultLimit, limitHelp)
	f.StringVar(&order, "order", orderNewest, "newest or oldest first")
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

			data, _ := rowForMessage(*message)
			row, _ := data.(messageRow)
			return r.Block(data, row.Text)
		},
	}
}

// orderByFor turns the flag into the API's ordering, refusing anything else.
//
// Refused rather than passed through, because a typo that reached the API would
// come back as an INVALID_ARGUMENT naming a field the caller never typed, and a
// value silently ignored would return the opposite order with a success code.
func orderByFor(order string) (string, error) {
	switch strings.ToLower(order) {
	case orderNewest:
		return chat.OrderNewestFirst, nil
	case orderOldest:
		return chat.OrderOldestFirst, nil
	}
	return "", output.Usagef("--order takes %q or %q, and %q is neither.", orderNewest, orderOldest, order)
}
