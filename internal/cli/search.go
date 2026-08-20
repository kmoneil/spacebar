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
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/resolve"
	"github.com/kmoneil/spacebar/internal/rows"
	"github.com/kmoneil/spacebar/internal/store"
)

func newSearchCmd(opts *Options) *cobra.Command {
	var (
		space   string
		limit   int
		since   string
		until   string
		refresh bool
	)

	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search the local index for what was said",
		Long: `Search the local index for what was said.

  ` + meta.AppName + ` search "deploy"
  ` + meta.AppName + ` search "deploy" --space eng --limit 10
  ` + meta.AppName + ` search "deploy" --since 30d --json

This searches what ` + meta.AppName + ` sync has copied, not Google Chat. There
is no message search API for an ordinary user, so there is nothing to ask: the
index is the answer or there is none.

That has one consequence worth stating plainly. A space you have never synced
is not searched, and a search that quietly skipped it would answer a narrower
question than the one you asked. So the spaces that were skipped are named on
stderr, from the space list this profile already has cached, and no network
call is made to find out.

Matching is case-folded substring over the message body. This is one person's
readable history rather than a corpus, and a scan of it returns in well under a
second; anything cleverer is a decision that needs evidence rather than a
feature.

The index is a snapshot of what sync copied, and this is the part worth knowing
before you rely on it. A message edited after it was copied is found by the text
it had then, and a message deleted after it was copied is still found. sync
walks createTime in two windows, everything newer than the newest message it
holds and everything older than the oldest, and an edit does not change
createTime, so nothing brings a message it already holds back for a second look.
Deleting a message with this tool removes it from the space and leaves the copy
alone.

So a result is what was said, not necessarily what is there now. Check against
the space with ` + meta.AppName + ` messages get before acting on an old one.

There is no reindex. The index is the only copy of a message the API will not
answer for twice, which is why nothing here prunes it and why the honest way to
refresh a space is to move its file out of the data directory and sync again,
knowing what that discards.`,

		Args: exactlyOne("search needs something to look for.\n  %s search \"deploy\""),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := parseSince(since)
			if err != nil {
				return err
			}
			to, err := parseSince(until)
			if err != nil {
				return err
			}

			r := renderer(cmd, opts)

			index, err := openIndex()
			if err != nil {
				return err
			}

			// The profile is opened for its aliases and its cached space list,
			// not for the network. No capability is required, because nothing
			// here makes a request: a webhook profile can search what a
			// user-authorized one synced.
			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}

			target := ""
			if space != "" {
				target, err = opened.Resolve(cmd.Context(), space, refresh)
				if err != nil {
					return err
				}
			}

			if err := reportUnsearched(r, opened.Name, index, target); err != nil {
				return err
			}

			err = stream(r, index.Search(cmd.Context(), store.Query{
				Space: target,
				Text:  args[0],
				Since: from,
				Until: to,
				Limit: limit,
			}), searchRow)

			// After the rows rather than before them, because the index cannot
			// know it skipped anything until it has read the files, and a
			// warning is printed whether or not the search then failed: a
			// record the index holds and will not answer with is worth saying
			// either way.
			r.Warnings(index.Warnings())
			return finish(r, opened, err)
		},
	}

	f := cmd.Flags()
	f.StringVar(&space, "space", "", "search one space instead of every indexed one")
	f.IntVar(&limit, "limit", defaultLimit, limitHelp)
	f.StringVar(&since, "since", "", sinceHelp)
	f.StringVar(&until, "until", "", "only messages created strictly before this time")
	addRefreshFlag(cmd, &refresh)

	return cmd
}

// reportUnsearched names the spaces this search did not look in.
//
// The card's third falsifiable claim, and it is a claim about honesty rather
// than about function: a search that silently skips an unsynced space answers a
// narrower question than the one that was asked, and nothing in the output says
// so.
//
// It compares the index against the space list the resolver already cached, so
// it costs no request. A profile that has never listed its spaces has no cache
// and therefore nothing to compare, and says that instead of guessing.
func reportUnsearched(r *output.Renderer, profileName string, index *store.NDJSON, target string) error {
	indexed, err := index.Spaces()
	if err != nil {
		return err
	}

	if target != "" {
		if !slices.Contains(indexed, target) {
			return output.Usagef("%s is not in the local index, so there is nothing to search.\n"+
				"Copy it down first:\n  %s sync %s", target, meta.AppName, target)
		}
		return nil
	}

	if len(indexed) == 0 {
		return output.Usagef("the local index is empty, so there is nothing to search.\n"+
			"Copy a space down first:\n  %s sync --all", meta.AppName)
	}

	known, ok := resolve.NewCache(profileName).Read()
	if !ok {
		// No cached list means no way to know what is missing. Saying that is
		// better than saying nothing, because "3 results" reads as complete.
		r.Note("searched %d indexed spaces. This profile has no cached space list, "+
			"so there is no way to say which spaces are missing from the index.", len(indexed))
		return nil
	}

	var missing []string
	for _, s := range known {
		if !slices.Contains(indexed, s.Name) {
			missing = append(missing, s.Name)
		}
	}
	if len(missing) == 0 {
		r.Note("searched all %d spaces this profile can reach.", len(indexed))
		return nil
	}

	r.Warn("searched %d of %d spaces. Not searched, because they are not in the index: %s\nRun: %s sync --all",
		len(indexed), len(indexed)+len(missing), strings.Join(missing, " "), meta.AppName)
	return nil
}

// searchRow is the projection for a result that is already a published message.
//
// stream's other callers hand it a wire type and a function that projects one.
// The index stores rows.Message, so the projection has already happened and
// this only chooses the columns.
//
// The space is one of them, which the message-listing columns are not, because
// a search without --space crosses spaces and a result nobody can place is a
// result nobody can act on. It is read out of the message's own name rather
// than carried alongside: a name contains its space, so the two cannot
// disagree.
func searchRow(m rows.Message) (rows.Message, []string) {
	space, err := chat.SpaceOfMessage(m.Name)
	if err != nil {
		space = ""
	}

	who := m.DisplayName
	if who == "" {
		who = m.Sender
	}
	return m, []string{m.CreateTime, space, who, m.Text}
}
