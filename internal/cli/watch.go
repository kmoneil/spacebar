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
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/transport"
)

func newWatchCmd(opts *Options) *cobra.Command {
	var (
		interval time.Duration
		since    string
		events   string
		filter   string
		refresh  bool
	)

	cmd := &cobra.Command{
		Use:   "watch SPACE",
		Short: "Follow everything that happens in a space, not just new messages",
		Long: `Follow everything that happens in a space, not just new messages.

  ` + meta.AppName + ` watch spaces/AAAAAAA
  ` + meta.AppName + ` watch eng --events message,reaction,membership
  ` + meta.AppName + ` watch eng --since 2h --json

` + meta.AppName + ` tail polls for messages, so it cannot see an edit, a
deletion, or a reaction: a poll on createTime returns new messages and none of
those makes one. This polls spaceEvents instead, which reports them as events.

--events takes any of message, reaction, membership and space, and defaults to
message and reaction, which is the conversation. Membership and space updates
are administrative and arrive on a different rhythm, so they are opt-in rather
than noise in the common case.

The interval floor is 2s and a smaller one is refused rather than rounded up,
for the reason it is on tail: per-space quota is shared with every other app
acting in that space.

Ctrl-C exits 0. It is how this command is meant to end.

This endpoint needs a Chat app configured on the Cloud project behind the
profile, which is a separate step from enabling the Chat API. Without it the
refusal is a 404 that mentions neither, and this tool explains it.`,

		Args: exactlyOne("watch needs a space.\n  %s watch spaces/AAAAAAA"),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Everything that can be refused without the network is refused
			// first, so a mistyped interval or event type costs no keyring read.
			if err := chat.CheckInterval(interval); err != nil {
				return err
			}
			types, err := chat.EventTypesFor(events)
			if err != nil {
				return err
			}
			from, err := parseSince(since)
			if err != nil {
				return err
			}

			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "watch", transport.CanRead); err != nil {
				return err
			}

			space, err := opened.Resolve(cmd.Context(), args[0], refresh)
			if err != nil {
				return err
			}

			// Ctrl-C cancels the context rather than killing the process, so
			// the loop ends where it chooses to and the exit code is this
			// command's own. The same handling tail has, for the same reason.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return finish(r, opened, stream(r, opened.Transport.Watch(ctx, chat.WatchRequest{
				Space:    space,
				Types:    types,
				Filter:   filter,
				Interval: interval,
				Since:    from,
			}), rows.ForEvent))
		},
	}

	f := cmd.Flags()
	f.DurationVar(&interval, "interval", 0, "how often to poll, at least 2s")
	f.StringVar(&events, "events", chat.DefaultEventGroups,
		"which events to ask for: any of message, reaction, membership, space")
	f.StringVar(&since, "since", "", sinceHelp)
	f.StringVar(&filter, "filter", "",
		"the API's own filter expression, replacing the built one; it has to carry its own event_types")
	addRefreshFlag(cmd, &refresh)

	return cmd
}
