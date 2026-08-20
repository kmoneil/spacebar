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
	"strconv"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/store"
	"github.com/kmoneil/spacebar/internal/transport"
)

func newSyncCmd(opts *Options) *cobra.Command {
	var (
		all     bool
		limit   int
		refresh bool
	)

	cmd := &cobra.Command{
		Use:   "sync [SPACE]",
		Short: "Copy a space's messages into the local index",
		Long: `Copy a space's messages into the local index.

  ` + meta.AppName + ` sync spaces/AAAAAAA
  ` + meta.AppName + ` sync eng --limit 5000
  ` + meta.AppName + ` sync --all

There is no message search API for an ordinary user. spaces.search is
administrator-only and searches spaces rather than messages, so searching what
was said means keeping a copy, and this is the command that makes one.
` + meta.AppName + ` search reads it back.

It is resumable and holds no cursor of its own. The index already knows the
window it covers, so a run fetches everything newer than the newest message it
holds and everything older than the oldest, and an interrupted run resumes by
asking the same question again. Nothing is fetched twice and no gap is left in
the middle. The cost is one request per run to discover there is nothing older,
which is cheaper than a second file that can disagree with the first.

--limit bounds how many messages one run fetches per space, so a space with a
hundred thousand messages can be brought down over several runs rather than in
one. It is not a truncation: the run stops where it stopped, and the next one
carries on from there.

The index is the only copy of a message that has since been deleted, because
the API will not answer for one twice. It lives under XDG_DATA_HOME rather than
the cache directory for that reason, and nothing here ever removes it.`,

		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkSyncTarget(all, args); err != nil {
				return err
			}

			r := renderer(cmd, opts)

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "sync", transport.CanRead); err != nil {
				return err
			}
			return runSync(cmd, r, opened, all, args, limit, refresh)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "sync every space this profile can reach")
	f.IntVar(&limit, "limit", 0, "stop after this many messages per space; 0 means every one")
	addRefreshFlag(cmd, &refresh)

	return cmd
}

// runSync walks the target spaces, split from the command for the complexity
// ceiling.
func runSync(cmd *cobra.Command, r *output.Renderer, opened *profile.Open,
	all bool, args []string, limit int, refresh bool,
) error {
	index, err := openIndex()
	if err != nil {
		return err
	}

	spaces, err := syncTargets(cmd.Context(), opened, all, args, refresh)
	if err != nil {
		return err
	}

	for _, space := range spaces {
		result, err := store.Sync(cmd.Context(), index, opened.Transport, space, limit)
		if err != nil {
			return finish(r, opened, err)
		}

		// The walk returns this rather than printing it, because only
		// internal/output writes to a process stream. Which of the two adapters
		// is rendering decides what to say about a run that found nothing, and
		// this one says it to a person on stderr.
		if result.Fetched == 0 {
			r.Note("%s: nothing new, %d messages held.", space, result.Held)
		}
		if err := r.Item(result, result.Space,
			strconv.Itoa(result.Fetched), strconv.Itoa(result.Held)); err != nil {
			return finish(r, opened, err)
		}
	}
	return finish(r, opened, nil)
}

// checkSyncTarget refuses naming a space and --all at once, and naming neither.
func checkSyncTarget(all bool, args []string) error {
	switch {
	case all && len(args) > 0:
		return output.Usagef("sync --all covers every space, so it cannot also take %q.\n"+
			"Ask for one of them.", args[0])
	case !all && len(args) == 0:
		return output.Usagef("sync needs a space, or --all.\n"+
			"  %s sync spaces/AAAAAAA\n  %s sync --all", meta.AppName, meta.AppName)
	case !all && len(args) > 1:
		return output.Usagef("sync takes one space. Use --all to cover every one.")
	}
	return nil
}

// openIndex opens the on-disk index.
func openIndex() (*store.NDJSON, error) {
	dir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	return store.NewNDJSON(dir), nil
}

// syncTargets is the list of spaces to bring down.
func syncTargets(ctx context.Context, opened *profile.Open, all bool, args []string, refresh bool) ([]string, error) {
	if all {
		return everySpace(ctx, opened)
	}
	space, err := opened.Resolve(ctx, args[0], refresh)
	if err != nil {
		return nil, err
	}
	return []string{space}, nil
}
