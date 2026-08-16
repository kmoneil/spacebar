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
	"sort"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// aliasRow is the --json shape of one alias.
type aliasRow struct {
	Name  string `json:"name"`
	Space string `json:"space"`
}

func newAliasCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alias",
		Short: "Name a space something you will remember",
		Long: `Name a space something you will remember.

An alias belongs to one profile and lives in the configuration file beside it.
It stores a space resource name, resolved when the alias is set, so that what is
recorded is a space and not a display name that will drift out from under it
when somebody renames the room.

Aliases are the second step of resolution and the only one that needs no
permission, so they work on a webhook profile, where reading is refused.`,
	}

	cmd.AddCommand(
		newAliasSetCmd(opts),
		newAliasListCmd(opts),
		newAliasRemoveCmd(opts),
	)
	return cmd
}

func newAliasSetCmd(opts *Options) *cobra.Command {
	var refresh bool

	cmd := &cobra.Command{
		Use:   "set NAME TARGET",
		Short: "Point a name at a space",
		Long: `Point a name at a space.

  ` + meta.AppName + ` alias set eng spaces/AAAAAAA
  ` + meta.AppName + ` alias set eng 'Engineering'      # resolved now, stored as a space
  ` + meta.AppName + ` alias set bob bob@example.com    # the direct message with them

The target is resolved when the alias is set, and the space it resolved to is
what gets stored. A display name is a label somebody else controls: storing one
would mean an alias that quietly starts pointing somewhere new the day a room is
renamed, or stops working the day it is.

An alias cannot contain a slash or an @, because resolution reads a name of
either shape as something other than an alias, and one that could never be
consulted is worse than one that is refused.`,

		Args: exactlyTwo("alias set needs a name and a target.\n  %s alias set eng spaces/AAAAAAA"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAliasSet(cmd, opts, args[0], args[1], refresh)
		},
	}

	addRefreshFlag(cmd, &refresh)
	return cmd
}

// runAliasSet resolves the target and records the alias.
//
// Split out of the command for the cognitive complexity ceiling, which is the
// gate working: the resolution, the dry run, and the write were three decisions
// in one closure, and the middle one is the easiest to get wrong.
func runAliasSet(cmd *cobra.Command, opts *Options, name, target string, refresh bool) error {
	r := renderer(cmd, opts)

	if err := config.CheckAliasName(name); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	active, _, err := cfg.Active(opts.Profile)
	if err != nil {
		return err
	}

	// Opened rather than read out of the configuration, because resolving a
	// display name or an address needs a transport that can read, and the
	// refusal for one that cannot belongs to the gate.
	opened, err := openProfile(opts, r)
	if err != nil {
		return err
	}

	space, err := opened.Resolve(cmd.Context(), target, refresh)
	if err != nil {
		return err
	}
	if space == "" {
		// Only reachable with an empty target, which the argument count already
		// refuses. Handled because storing "" would be an alias that resolves to
		// the profile's own space, which is a different feature nobody asked for.
		return output.Usagef("alias set needs a target.\n  %s alias set %s spaces/AAAAAAA",
			meta.AppName, name)
	}

	if opts.DryRun {
		r.Note("nothing was stored.")
		return reportAlias(r, name, space, active)
	}

	if err := storeAlias(cfg, active, name, space); err != nil {
		return err
	}
	return reportAlias(r, name, space, active)
}

// storeAlias writes one alias into the active profile.
func storeAlias(cfg *config.Config, active, name, space string) error {
	profile := cfg.Profiles[active]
	if profile.Aliases == nil {
		profile.Aliases = map[string]string{}
	}
	profile.Aliases[name] = space
	cfg.Profiles[active] = profile
	return cfg.Save()
}

// reportAlias is the one shape a set reports, so that a dry run and a real one
// cannot describe the same thing differently.
func reportAlias(r *output.Renderer, name, space, active string) error {
	return r.Result(aliasRow{Name: name, Space: space}, output.Fields{
		{Label: "alias", Value: name},
		{Label: "space", Value: space},
		{Label: "profile", Value: active},
	})
}

func newAliasListCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List this profile's aliases",
		Long: `List this profile's aliases.

Columns are the alias and the space it points at, separated by a tab. Nothing
here reads a credential or touches the network: an alias is a line in the
configuration file, so this answers on a machine with no connection.`,

		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := renderer(cmd, opts)

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			active, profile, err := cfg.Active(opts.Profile)
			if err != nil {
				return err
			}

			if len(profile.Aliases) == 0 {
				// Nothing on stdout, because zero results is what a caller
				// parsing this has to see. The sentence goes to stderr, where it
				// cannot corrupt that.
				r.Note("profile %q has no aliases. Add one with: %s alias set NAME spaces/AAAAAAA",
					active, meta.AppName)
				return nil
			}

			names := make([]string, 0, len(profile.Aliases))
			for name := range profile.Aliases {
				names = append(names, name)
			}
			sort.Strings(names)

			for _, name := range names {
				space := profile.Aliases[name]
				if err := r.Item(aliasRow{Name: name, Space: space}, name, space); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newAliasRemoveCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:     "rm NAME",
		Aliases: []string{"remove"},
		Short:   "Forget an alias",
		Long: `Forget an alias.

There is no confirmation, unlike removing a profile, and the difference is what
is lost. A profile takes a credential with it and a webhook URL only comes back
from the space it was issued in. An alias is a line pointing at a space that is
still there, and setting it again costs one command.`,

		Args: exactlyOne("alias rm needs a name.\n  %s alias rm eng"),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := renderer(cmd, opts)

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			active, profile, err := cfg.Active(opts.Profile)
			if err != nil {
				return err
			}

			name := args[0]
			if _, ok := profile.Aliases[name]; !ok {
				// A failure rather than a silent success. "There was no such
				// alias" is the answer to a question somebody asked, and exiting
				// 0 would tell a script that a name it expected to remove had
				// been removed.
				return output.Usagef("profile %q has no alias called %q.\nSee them with: %s alias list",
					active, name, meta.AppName)
			}

			if opts.DryRun {
				r.Note("nothing was removed.")
				return r.Result(aliasRow{Name: name, Space: profile.Aliases[name]}, output.Fields{
					{Label: "would remove", Value: name},
				})
			}

			delete(profile.Aliases, name)
			cfg.Profiles[active] = profile
			if err := cfg.Save(); err != nil {
				return err
			}

			return r.Result(aliasRow{Name: name}, output.Fields{{Label: "removed", Value: name}})
		},
	}
}

// exactlyTwo builds an argument check for a command taking a name and a value.
//
// The same reasoning as exactlyOne: cobra.ExactArgs returns a plain error that
// lands in the generic bucket at exit 1, and an argument count is always the
// caller's mistake, which is exit 2.
func exactlyTwo(format string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == 2 {
			return nil
		}
		return output.Usagef(format, meta.AppName)
	}
}
