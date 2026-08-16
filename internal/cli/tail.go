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
	"github.com/kmoneil/spacebar/internal/transport"
)

func newTailCmd(opts *Options) *cobra.Command {
	var (
		interval time.Duration
		backfill int
		refresh  bool
	)

	cmd := &cobra.Command{
		Use:   "tail SPACE",
		Short: "Follow a space as messages arrive",
		Long: `Follow a space as messages arrive.

  ` + meta.AppName + ` tail spaces/AAAAAAA
  ` + meta.AppName + ` tail eng --backfill 20        # the last 20, then follow
  ` + meta.AppName + ` tail eng --json | jq -r .text

Oldest first, because this is a conversation being read in the order it
happened. That is the opposite of ` + meta.AppName + ` messages list, which is
newest first so that a limit cuts from the recent end.

Google Chat offers no socket, so this polls. The interval floor is 2s and a
smaller one is refused rather than rounded up: per-space quota is shared with
every other app acting in that space, so a tight loop degrades the space for
everybody in it. After five polls with nothing new the interval doubles, up to
a minute, and any message resets it.

Ctrl-C exits 0. It is how this command is meant to end, so it is not a failure.

Two things it does not do. It does not replay what was already there unless
--backfill asks. And it never corrects itself: a message edited or deleted while
you are watching is not shown again, because a poll sees new messages and an
edit does not make one.`,

		Args: exactlyOne("tail needs a space.\n  %s tail spaces/AAAAAAA"),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Refused before the profile is even loaded, so a bad interval
			// costs no keyring read and no network call.
			if err := chat.CheckInterval(interval); err != nil {
				return err
			}

			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "tail", transport.CanRead); err != nil {
				return err
			}

			space, err := resolveTarget(cmd.Context(), opened, args[0], refresh)
			if err != nil {
				return err
			}

			// Ctrl-C cancels the context rather than killing the process, so the
			// loop ends where it chooses to and the exit code is this command's
			// own. NotifyContext restores the default handler on stop, which
			// matters because a second Ctrl-C should still work if something
			// here ever hangs.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return finish(r, opened, stream(r, opened.Transport.Tail(ctx, chat.TailRequest{
				Space:    space,
				Interval: interval,
				Backfill: backfill,
			}), rowForMessage))
		},
	}

	f := cmd.Flags()
	f.DurationVar(&interval, "interval", 0, "how often to poll, at least 2s")
	f.IntVar(&backfill, "backfill", 0, "print this many existing messages before following")
	addRefreshFlag(cmd, &refresh)

	return cmd
}
