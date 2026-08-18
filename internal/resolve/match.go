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

package resolve

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
)

// byDisplayName matches a target against the display names this profile can
// reach.
//
// Case-folded exact first, then case-folded substring, and never a ranking.
// Somebody can learn "it matches if the name contains what I typed, ignoring
// case". Nobody can learn a scoring function, and a tool that silently picks
// the wrong space is worse than one that asks.
//
// The exact pass exists because a substring-only rule makes a prefix of another
// name permanently unusable: with spaces called "Ops" and "Ops Escalation",
// typing "Ops" would be ambiguous forever, and the space whose name it is
// exactly would be the one you could not reach.
func byDisplayName(ctx context.Context, r Reader, target string, opts Options) (string, error) {
	if err := transport.Require(r, "resolving "+target, transport.CanListSpaces); err != nil {
		// A profile name in this slot is far likelier than somebody expecting a
		// write-only webhook to serve a display-name lookup, and the capability
		// refusal answers the second question: it says the transport cannot
		// list spaces and tells them to go and create an OAuth client, which is
		// a long way to walk to fix an argument in the wrong position.
		//
		// Nothing is given up by answering the likelier question, because the
		// lookup could not have succeeded on this transport either way. That is
		// what makes it safe here and not below.
		if name, ok := opts.profileNamed(target); ok {
			return "", profileInTheSpaceSlotErr(name, "")
		}
		return "", err
	}

	spaces, err := spacesFor(ctx, r, opts)
	if err != nil {
		return "", err
	}

	folded := strings.ToLower(strings.TrimSpace(target))
	var exact, partial []chat.Space
	for _, space := range spaces {
		name := strings.ToLower(strings.TrimSpace(space.DisplayName))
		switch {
		case name == "":
			// A direct message and a group chat have no display name. Skipping
			// them is not a limitation: matching "" against "" would make every
			// unnamed space a candidate for an empty target, and the empty
			// target never reaches this function anyway.
		case name == folded:
			exact = append(exact, space)
		case strings.Contains(name, folded):
			partial = append(partial, space)
		}
	}

	if len(exact) > 0 {
		return oneOf(target, exact, "exactly")
	}
	if len(partial) > 0 {
		return oneOf(target, partial, "")
	}

	// After the match and never before it. A space really called this is a real
	// answer and wins, which is the whole reason the two namespaces can share a
	// word without either becoming unreachable: resolution is unchanged, and a
	// profile is only ever offered as the explanation for a target that did not
	// resolve.
	if name, ok := opts.profileNamed(target); ok {
		return "", profileInTheSpaceSlotErr(name, fmt.Sprintf(
			"Nothing this profile can reach is called that either. "+
				"See what there is with: %s spaces list", meta.AppName))
	}

	return "", output.Usagef("nothing this profile can reach is called %q.\n"+
		"Names are matched whole or as a substring, ignoring case. "+
		"See what there is with: %s spaces list\n"+
		"If the list looks stale, add --refresh.", target, meta.AppName)
}

// profileNamed is the configured profile a target names, if it names one.
//
// Case-folded, and it answers with the configured spelling rather than with
// what was typed, because the answer goes straight into a --profile somebody is
// about to paste and --profile itself is exact. Telling them to run
// `--profile Alerts` when the profile is `alerts` would be a second dead end.
func (o Options) profileNamed(target string) (string, bool) {
	folded := strings.ToLower(strings.TrimSpace(target))
	for _, name := range o.Profiles {
		if strings.ToLower(strings.TrimSpace(name)) == folded {
			return name, true
		}
	}
	return "", false
}

// profileInTheSpaceSlotErr says the target names a profile, and where a profile
// actually goes.
//
// Exit 2 rather than the 5 the capability path would have returned, because
// this is the caller's mistake about the arguments and not the transport's
// limit. Both are permanent and neither is worth a retry; what differs is what
// to change, and the exit code is the only part of that a script reads.
//
// The name is printed unsanitised, which is safe for the reason a profile name
// is safe as a path component: config.CheckProfileName runs over every key when
// the file is loaded, and it admits letters, digits and three punctuation
// marks. The target is not printed at all, only the profile it matched.
func profileInTheSpaceSlotErr(name, more string) error {
	message := fmt.Sprintf("%q is a profile, not a space.\n"+
		"The first argument names the space to act on. A profile is chosen with --profile, "+
		"which every command takes, or with %s for a whole process.\n"+
		"Take it out of the arguments and add: --profile %s",
		name, config.EnvProfile(), name)

	if more != "" {
		message += "\n" + more
	}
	return output.Usagef("%s", message)
}

// oneOf returns the single match, or refuses and lists them.
//
// Never guesses, per SPEC.md §10.1. Two spaces matching is not a tie to be
// broken, it is a question only the person who typed it can answer, and the
// cost of answering wrongly is a message in front of people who were not meant
// to see it.
func oneOf(target string, matches []chat.Space, how string) (string, error) {
	if len(matches) == 1 {
		// From the API or from a file on disk, either way a value from
		// elsewhere heading for a request path.
		if err := chat.CheckSpaceName(matches[0].Name); err != nil {
			return "", err
		}
		return matches[0].Name, nil
	}

	// Sorted so that the list is the same on every run. An ambiguity somebody
	// is going to paste into an issue should not reorder itself.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].DisplayName != matches[j].DisplayName {
			return matches[i].DisplayName < matches[j].DisplayName
		}
		return matches[i].Name < matches[j].Name
	})

	qualifier := "match"
	if how != "" {
		qualifier = how + " match"
	}

	var b strings.Builder
	for _, space := range matches {
		// Sanitised, because a display name is untrusted text from a space this
		// account can reach and this string is going to a terminal.
		b.WriteString("\n  " + output.Cell(space.Name) + "\t" + output.Cell(space.DisplayName))
	}

	// Naming the alias command is the point of the second sentence: somebody
	// hitting this twice wants a name that is never ambiguous, and that is what
	// an alias is for.
	return "", output.Usagef("%d spaces %s %q, and this tool does not guess which one you meant:%s\n"+
		"Name the one you want directly, or give it an alias: %s alias set NAME %s",
		len(matches), qualifier, target, b.String(), meta.AppName, matches[0].Name)
}

// spacesFor is the space list this resolution matches against, from the cache
// when it is fresh and from the API when it is not.
func spacesFor(ctx context.Context, r Reader, opts Options) ([]chat.Space, error) {
	if !opts.Refresh && opts.Cache != nil {
		if spaces, ok := opts.Cache.Read(); ok {
			return spaces, nil
		}
	}

	var spaces []chat.Space
	for space, err := range r.Spaces(ctx, chat.ListSpacesRequest{}) {
		if err != nil {
			return nil, err
		}
		spaces = append(spaces, space)
	}

	// A write failure is not a resolution failure. The answer is already in
	// hand, and refusing it because a cache file could not be written would
	// turn a full disk into "this tool cannot find your space".
	if opts.Cache != nil {
		_ = opts.Cache.Write(spaces)
	}
	return spaces, nil
}
