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
	"context"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/transport"
)

func newWatchCmd(opts *Options) *cobra.Command {
	var (
		interval time.Duration
		since    string
		events   string
		filter   string
		all      bool
		refresh  bool
	)

	cmd := &cobra.Command{
		Use:   "watch [SPACE]",
		Short: "Follow everything that happens in a space, not just new messages",
		Long: `Follow everything that happens in a space, not just new messages.

  ` + meta.AppName + ` watch spaces/AAAAAAA
  ` + meta.AppName + ` watch eng --events message,reaction,membership
  ` + meta.AppName + ` watch eng --since 2h --json
  ` + meta.AppName + ` watch --all

` + meta.AppName + ` tail polls for messages, so it cannot see an edit, a
deletion, or a reaction: a poll on createTime returns new messages and none of
those makes one. This polls spaceEvents instead, which reports them as events.

--events takes any of message, reaction, membership and space, and defaults to
message and reaction, which is the conversation. Membership and space updates
are administrative and arrive on a different rhythm, so they are opt-in rather
than noise in the common case.

--since bounds when an event happened rather than when a message was created,
and the two come apart on exactly what this command exists to show: an edit at
09:05 of a message posted at 08:00 is returned by --since 09:00. It is strictly
after, so an event at exactly the instant named is not returned.

The interval floor is 2s and a smaller one is refused rather than rounded up,
for the reason it is on tail: per-space quota is shared with every other app
acting in that space.

After five polls with nothing new a space is polled less often, doubling up to
a minute, or staying where it is if you asked for less often than that. Any
event resets it. With --all that is per space, so one busy space does not hold
thirty quiet ones at the base interval, and a space that has been silent all
night still answers within a minute of somebody speaking.

--all watches every space this profile can reach, instead of one named space.
Two things about it are worth knowing before you leave it running.

The spaces are polled one at a time, round robin, at a rate this process holds
to 10 requests a second however many there are. Google's quota for reading
space events is 3000 a minute for the whole Cloud project, which is fifty a
second, and the project is shared by everybody using the same OAuth client, so
taking all of it would mean the first person to start --all denies the second.
Below twenty spaces the 2s floor is what decides the pace and nothing is slowed
at all. Above it each space comes round less often: thirty spaces every 3s, a
hundred every 10s. The interval chosen is printed on stderr at startup rather
than left to be worked out, unless --quiet or --json says the reader is not a
person.

The list of spaces is taken once, at the start. A space created while this is
running is not picked up, because re-listing costs requests on the same quota
and a watch whose subject changes underneath it is harder to reason about.
Restart to pick up a new one. A space that goes away, or that this account
turns out not to be allowed to read, is dropped with a line on stderr saying
which and why, and the others carry on.

Ctrl-C exits 0. It is how this command is meant to end.

This endpoint needs a Chat app configured on the Cloud project behind the
profile, which is a separate step from enabling the Chat API. Without it the
refusal is a 404 that mentions neither, and this tool explains it.`,

		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Everything that can be refused without the network is refused
			// first, so a mistyped interval or event type costs no keyring read.
			if err := checkWatchTarget(all, args); err != nil {
				return err
			}
			if err := chat.CheckInterval(interval); err != nil {
				return err
			}
			asked, err := watchWindow(events, since)
			if err != nil {
				return err
			}
			asked.interval, asked.filter, asked.refresh = interval, filter, refresh

			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "watch", transport.CanRead); err != nil {
				return err
			}

			// Ctrl-C cancels the context rather than killing the process, so
			// the loop ends where it chooses to and the exit code is this
			// command's own. The same handling tail has, for the same reason.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			if all {
				return watchEverySpace(ctx, r, opened, asked)
			}
			return watchOneSpace(ctx, r, opened, asked, args[0])
		},
	}

	f := cmd.Flags()
	f.DurationVar(&interval, "interval", 0, intervalHelp)
	f.StringVar(&events, "events", chat.DefaultEventGroups,
		"which events to ask for: any of message, reaction, membership, space")
	f.StringVar(&since, "since", "", watchSinceHelp)
	f.StringVar(&filter, "filter", "",
		"the API's own filter expression, replacing the built one; it has to carry its own event_types")
	f.BoolVar(&all, "all", false, "watch every space this profile can reach, instead of one")
	addRefreshFlag(cmd, &refresh)

	return cmd
}

// watchAsked is what the flags said, gathered so that the two ways of running
// this command take one argument rather than six.
type watchAsked struct {
	types    []string
	from     time.Time
	interval time.Duration
	filter   string
	refresh  bool
}

// watchWindow parses the two flags that can fail, before any profile is loaded.
func watchWindow(events, since string) (watchAsked, error) {
	types, err := chat.EventTypesFor(events)
	if err != nil {
		return watchAsked{}, err
	}
	from, err := parseSince(since)
	if err != nil {
		return watchAsked{}, err
	}
	return watchAsked{types: types, from: from}, nil
}

// watchEverySpace is --all: one budgeted rotation over every space the profile
// can reach.
func watchEverySpace(ctx context.Context, r *output.Renderer, opened *profile.Open, asked watchAsked) error {
	spaces, err := everySpace(ctx, opened)
	if err != nil {
		return err
	}

	// Said once, at the start, because both facts are things somebody would
	// otherwise have to discover: how often each space is actually polled, and
	// that the list will not grow.
	r.Note("watching %d spaces, one every %s, from a list taken now: "+
		"a space created after this point is not picked up.",
		len(spaces), chat.IntervalForSpaces(asked.interval, len(spaces)))

	return finish(r, opened, stream(r, opened.Transport.WatchMany(ctx, chat.WatchManyRequest{
		Spaces:   spaces,
		Types:    asked.types,
		Filter:   asked.filter,
		Interval: asked.interval,
		Since:    asked.from,
		OnDropped: func(space string, err error) {
			r.Warn("no longer watching %s: %v", space, err)
		},
	}), rows.ForEvent))
}

// watchOneSpace is the named-space form, which is what this command was before
// --all and is unchanged by it.
func watchOneSpace(ctx context.Context, r *output.Renderer, opened *profile.Open,
	asked watchAsked, target string,
) error {
	space, err := opened.Resolve(ctx, target, asked.refresh)
	if err != nil {
		return err
	}

	return finish(r, opened, stream(r, opened.Transport.Watch(ctx, chat.WatchRequest{
		Space:    space,
		Types:    asked.types,
		Filter:   asked.filter,
		Interval: asked.interval,
		Since:    asked.from,
	}), rows.ForEvent))
}

// checkWatchTarget refuses the two ways of saying what to watch at once, and
// the way of saying neither.
//
// Refused rather than resolved by precedence, for the reason --since and
// --backfill are on tail: either precedence rule leaves somebody watching
// something they did not ask for, with nothing saying so. A named space beside
// --all is not a narrowing, it is a contradiction.
func checkWatchTarget(all bool, args []string) error {
	switch {
	case all && len(args) > 0:
		return output.Usagef("watch --all watches every space, so it cannot also take %q.\n"+
			"Ask for one of them.", args[0])
	case !all && len(args) == 0:
		return output.Usagef("watch needs a space, or --all.\n"+
			"  %s watch spaces/AAAAAAA\n  %s watch --all", meta.AppName, meta.AppName)
	case !all && len(args) > 1:
		return output.Usagef("watch takes one space. Use --all to watch every one.")
	}
	return nil
}

// everySpace is the list --all watches, taken once.
//
// Every space the profile can reach, direct messages included: somebody asking
// to watch everything means everything, and a direct message is where the
// message they are waiting for is as likely to arrive as anywhere.
func everySpace(ctx context.Context, opened *profile.Open) ([]string, error) {
	var spaces []string
	for space, err := range opened.Transport.Spaces(ctx, chat.ListSpacesRequest{}) {
		if err != nil {
			return nil, err
		}
		if err := chat.CheckSpaceName(space.Name); err != nil {
			return nil, err
		}
		spaces = append(spaces, space.Name)
	}
	if len(spaces) == 0 {
		return nil, output.Usagef("there is nothing to watch: profile %q can reach no spaces.", opened.Name)
	}
	return spaces, nil
}
