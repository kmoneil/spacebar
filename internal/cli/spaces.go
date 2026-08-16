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
		refresh     bool
	)

	cmd := &cobra.Command{
		Use:   "members SPACE",
		Short: "List who is in a space",
		Long: `List who is in a space.

  ` + meta.AppName + ` spaces members spaces/AAAAAAA
  ` + meta.AppName + ` spaces members spaces/AAAAAAA --show-invited

Columns are the member's resource name, whether they are a person or an app,
their state, their role, and their affiliation, separated by a tab.

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
them. A membership held by a Google Group is not returned either, and there is
no flag for it yet.`,

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

			return finish(r, opened, stream(r, opened.Transport.Members(cmd.Context(), chat.ListMembersRequest{
				Space:       target,
				ShowInvited: showInvited,
				Limit:       limit,
			}), rows.ForMember))
		},
	}

	f := cmd.Flags()
	f.IntVar(&limit, "limit", defaultLimit, limitHelp)
	f.BoolVar(&showInvited, "show-invited", false, "include people who were invited and have not joined")
	addRefreshFlag(cmd, &refresh)
	return cmd
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
