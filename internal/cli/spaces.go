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
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/transport"
)

func newSpacesCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spaces",
		Short: "List and inspect spaces",
		Long: `List and inspect spaces.

Every one of these needs a profile that can read, which means a profile
authorized as you. An incoming webhook is write-only and fixed to one space, so
none of them work on one, and each says so before making a request.`,
	}

	cmd.AddCommand(
		newSpacesListCmd(opts),
		newSpacesGetCmd(opts),
		newSpacesMembersCmd(opts),
	)
	return cmd
}

func newSpacesListCmd(opts *Options) *cobra.Command {
	var (
		limit  int
		filter string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the spaces this profile can reach",
		Long: `List the spaces this profile can reach.

  ` + meta.AppName + ` spaces list
  ` + meta.AppName + ` spaces list --limit 0            # every one
  ` + meta.AppName + ` spaces list --json | jq -r .name

Columns are name, type, display name, whether it is a direct message with an
app, and when the space was last active, separated by a tab. A direct message
and a group chat have no display name of their own, so those rows are blank in
the third column rather than filled in with a guess about who is in them.

The fourth column says "bot" for a direct message with a Chat app and is empty
for everything else. It is there because without it every direct message is the
same row: the same blank name, the same DIRECT_MESSAGE, and no way to tell a
conversation with a colleague from one with an app.`,

		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "spaces list", transport.CanListSpaces); err != nil {
				return err
			}

			return finish(r, opened, stream(r, opened.Transport.Spaces(cmd.Context(), chat.ListSpacesRequest{
				Filter: filter,
				Limit:  limit,
			}), rows.ForSpace))
		},
	}

	f := cmd.Flags()
	f.IntVar(&limit, "limit", defaultLimit, limitHelp)
	f.StringVar(&filter, "filter", "", "the API's own filter expression, passed through unaltered")
	return cmd
}

func newSpacesGetCmd(opts *Options) *cobra.Command {
	var refresh bool

	cmd := &cobra.Command{
		Use:   "get SPACE",
		Short: "Read one space",
		Long: `Read one space.

  ` + meta.AppName + ` spaces get spaces/AAAAAAA
  ` + meta.AppName + ` spaces get eng-alerts        # an alias
  ` + meta.AppName + ` spaces get 'Ops'             # a display name

The argument is a space resource name, an alias, a display name, or an address.
A display name matches whole or as a substring, ignoring case, and two spaces
matching is refused rather than guessed at.`,

		Args: exactlyOne("spaces get needs a space.\n  %s spaces get spaces/AAAAAAA"),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "spaces get", transport.CanRead); err != nil {
				return err
			}

			target, err := opened.Resolve(cmd.Context(), args[0], refresh)
			if err != nil {
				return err
			}

			space, err := opened.Transport.GetSpace(cmd.Context(), target)
			if err != nil {
				return finish(r, opened, err)
			}

			data, _ := rows.ForSpace(*space)
			fields := output.Fields{
				{Label: "name", Value: space.Name},
				{Label: "type", Value: space.SpaceType},
				{Label: "display name", Value: space.DisplayName},
				{Label: "last active", Value: space.LastActiveTime},
			}

			// Only when it is true. A line saying a room is not a direct
			// message with an app answers a question nobody reading a room
			// asked, and unlike a list this output has no columns to keep
			// aligned.
			if space.SingleUserBotDm {
				fields = append(fields, output.Field{
					Label: "direct message with",
					Value: "a Chat app, not a person",
				})
			}
			return r.Result(data, fields)
		},
	}

	addRefreshFlag(cmd, &refresh)
	return cmd
}

func newSpacesMembersCmd(opts *Options) *cobra.Command {
	var (
		limit       int
		showInvited bool
		showGroups  bool
		refresh     bool
	)

	cmd := &cobra.Command{
		Use:   "members SPACE",
		Short: "List who is in a space",
		Long: `List who is in a space.

  ` + meta.AppName + ` spaces members spaces/AAAAAAA
  ` + meta.AppName + ` spaces members spaces/AAAAAAA --show-invited
  ` + meta.AppName + ` spaces members spaces/AAAAAAA --show-groups

Columns are the member's resource name, whether they are a person, an app, or a
group, their state, their role, and their affiliation, separated by a tab.

Affiliation is INTERNAL or EXTERNAL: whether they are inside your organization
or outside it. It is the column to read before posting something you would not
send outside. An app's membership has no affiliation at all, so that column is
blank on those rows, and nothing here fills in a value the API did not send.

There is no display name column. This endpoint returns a member as a resource
name and a type and nothing else, measured across seven memberships in five
spaces rather than read from documentation, and the sender of a message comes
back the same way, so it is how a user-authorized read answers rather than
something about one space. The resource name is the identifier in any case.
--json still carries a display_name field, so a caller gets one for free if the
API ever starts sending it.

By default this lists members who have joined. Somebody who has been invited
and has not accepted is not returned at all unless --show-invited asks for
them, and a membership held by a Google Group is not returned unless
--show-groups does. Both are the API's own defaults rather than choices made
here.

--show-groups matters more than it looks. A space can grant access to a group,
and then everybody in that group is in the space without a membership of their
own, so on such a space the default list is not the whole answer to "who can
see what I post here". A group row carries groups/NNN in the first column and
GROUP in the second. It carries no role and no affiliation, because the API
sends neither: the column that says who is outside your organization is blank
on exactly the row that can grant access to the most people, and how many
people that is, and who they are, is not a question a Chat scope can answer.
There is no group display name or address either, only groups/NNN, for the same
reason there is none for a person.`,

		Args: exactlyOne("spaces members needs a space.\n  %s spaces members spaces/AAAAAAA"),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "spaces members", transport.CanReadMembers); err != nil {
				return err
			}

			target, err := opened.Resolve(cmd.Context(), args[0], refresh)
			if err != nil {
				return err
			}

			// Counted during the walk and read after it, so the sentence is
			// only printed when there was something to say. It goes to stderr
			// after the rows, because stdout is the answer and this is about
			// the answer.
			//
			// Warn and not Note, which was the first version and was wrong.
			// Note is suppressed under --json and --quiet, which is right for
			// conversational chrome and exactly wrong for this: --json is what
			// a program reads, and a program told nothing is a program that
			// reports a narrowed membership list as the whole answer. That is
			// the defect this change exists to remove, and choosing Note would
			// have left it in place for the one reader who cannot check.
			// search.go warns for the same reason when it could not search
			// every space.
			hidden := 0
			err = finish(r, opened, stream(r, opened.Transport.Members(cmd.Context(), chat.ListMembersRequest{
				Space:        target,
				ShowInvited:  showInvited,
				ShowGroups:   showGroups,
				HiddenGroups: &hidden,
				Limit:        limit,
			}), rows.ForMember))
			if err != nil {
				return err
			}
			if hidden > 0 {
				r.Warn("%s", hiddenGroupsNote(hidden))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.IntVar(&limit, "limit", defaultLimit, limitHelp)
	f.BoolVar(&showInvited, "show-invited", false, "include people who were invited and have not joined")
	f.BoolVar(&showGroups, "show-groups", false, "include memberships held by a Google Group")
	addRefreshFlag(cmd, &refresh)
	return cmd
}

// hiddenGroupsNote is what somebody is told when the list they just read is
// not the whole answer.
//
// It says the number, what it means, and the flag, in that order, because the
// first thing somebody needs is to know the list is short and the last is what
// to type. It does not say who is in the group: a Chat scope reaches groups/NNN
// and no further, so there is no name, no address and no count of people to
// offer, and implying otherwise would send somebody looking for a command that
// does not exist.
//
// Printed only when something was withheld. A note on every complete list is
// noise, and noise is what teaches people to skip the line that mattered.
func hiddenGroupsNote(hidden int) string {
	return fmt.Sprintf("%d membership(s) held by a Google Group were not listed, and everybody in "+
		"such a group is in this space without a membership of their own.\n"+
		"So this is not the whole answer to who can see what you post here. Pass --show-groups to "+
		"list them, which names the group and nothing about who is in it.", hidden)
}

// exactlyOne builds an argument check that fails at exit 2 with a message
// naming the command's own shape.
//
// cobra.ExactArgs returns a plain error, which lands in the generic bucket at
// exit 1, and the number of arguments is always the caller's mistake. The
// format string takes one verb for the application name, so a rename stays a
// change to meta.AppName and to nothing else.
func exactlyOne(format string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == 1 {
			return nil
		}
		return output.Usagef(format, meta.AppName)
	}
}
