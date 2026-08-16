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
	"github.com/kmoneil/spacebar/internal/transport"
)

// spaceRow is the --json shape of one space.
//
// A shape of this repository's choosing rather than the wire struct, because a
// golden file makes it a public API the moment one records it. Passing the API's
// own document through would mean every field Google adds becomes part of this
// tool's contract without anybody deciding it should be.
type spaceRow struct {
	Name        string `json:"name"`
	SpaceType   string `json:"space_type,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

func rowForSpace(s chat.Space) (any, []string) {
	return spaceRow{
			Name:        s.Name,
			SpaceType:   s.SpaceType,
			DisplayName: s.DisplayName,
		}, []string{
			s.Name,
			s.SpaceType,
			s.DisplayName,
		}
}

// memberRow is the --json shape of one membership.
type memberRow struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
	Role  string `json:"role,omitempty"`

	// Member is users/NNN, which is the stable identifier. DisplayName is not:
	// it is chosen by the account holder, is not unique, and is untrusted text.
	Member      string `json:"member,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	MemberType  string `json:"member_type,omitempty"`
}

func rowForMember(m chat.Membership) (any, []string) {
	row := memberRow{Name: m.Name, State: m.State, Role: m.Role}
	if m.Member != nil {
		row.Member = m.Member.Name
		row.DisplayName = m.Member.DisplayName
		row.MemberType = m.Member.Type
	}

	return row, []string{row.Member, row.DisplayName, m.State, m.Role}
}

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

Columns are name, type, and display name, separated by a tab. A direct message
and a group chat have no display name of their own, so those rows are blank in
the last column rather than filled in with a guess about who is in them.`,

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
			}), rowForSpace))
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

			target, err := resolveTarget(cmd.Context(), opened, args[0], refresh)
			if err != nil {
				return err
			}

			space, err := opened.Transport.GetSpace(cmd.Context(), target)
			if err != nil {
				return finish(r, opened, err)
			}

			data, _ := rowForSpace(*space)
			return r.Result(data, output.Fields{
				{Label: "name", Value: space.Name},
				{Label: "type", Value: space.SpaceType},
				{Label: "display name", Value: space.DisplayName},
			})
		},
	}

	addRefreshFlag(cmd, &refresh)
	return cmd
}

func newSpacesMembersCmd(opts *Options) *cobra.Command {
	var (
		limit   int
		refresh bool
	)

	cmd := &cobra.Command{
		Use:   "members SPACE",
		Short: "List who is in a space",
		Long: `List who is in a space.

  ` + meta.AppName + ` spaces members spaces/AAAAAAA

Columns are the member's resource name, their display name, their state, and
their role. State is worth reading: an invited member is not a member yet, and
a list that showed both the same way would answer the question wrongly.

The display name column is blank, measured against a real space rather than
read from documentation: this endpoint returns a member as a resource name and
a type and nothing else. The column is kept rather than removed because it is
one observation, on one space, of one membership, and a column removed on that
evidence would have to come back the first time somebody sees a name in it.

The resource name is the identifier in any case. A display name is chosen by
the account holder, is not unique, and is untrusted text that is escaped before
it reaches a terminal.`,

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

			target, err := resolveTarget(cmd.Context(), opened, args[0], refresh)
			if err != nil {
				return err
			}

			return finish(r, opened, stream(r, opened.Transport.Members(cmd.Context(), chat.ListMembersRequest{
				Space: target,
				Limit: limit,
			}), rowForMember))
		},
	}

	cmd.Flags().IntVar(&limit, "limit", defaultLimit, limitHelp)
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
