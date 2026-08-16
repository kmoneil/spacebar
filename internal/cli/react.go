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

// reactionRow is the --json shape of one reaction.
//
// Not in internal/rows with the others, deliberately: those three are projected
// by both adapters, and nothing over MCP reacts yet. It moves there the day
// m5-02 registers a react tool, and putting it there now would be a shape
// nobody has agreed to in a package whose whole point is agreement.
type reactionRow struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Emoji   string `json:"emoji,omitempty"`
	User    string `json:"user,omitempty"`
}

func newReactCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "react MESSAGE EMOJI",
		Short: "React to a message with an emoji",
		Long: `React to a message with an emoji.

  ` + meta.AppName + ` react spaces/AAAAAAA/messages/BBBBBBB 👍

The emoji is the character itself, not a shortcode. ":thumbsup:" is refused by
the API at the type level rather than by this tool being fussy, and translating
one would mean carrying a shortcode table here that goes stale. Paste the
emoji: every terminal and every shell can carry it, and it is one character.

The reaction is yours, from the account this profile is authorized as. An
incoming webhook cannot react at all, because reacting is not something a
webhook URL can do, and that is refused before any request.`,

		Args: exactlyTwo("react needs a message and an emoji.\n" +
			"  %s react spaces/AAAAAAA/messages/BBBBBBB 👍"),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := renderer(cmd, opts)

			// Before the profile is loaded, so a shortcode costs no keyring
			// read and no request.
			if err := chat.CheckEmoji(args[1]); err != nil {
				return err
			}

			opened, err := openProfile(opts, r)
			if err != nil {
				return err
			}
			if err := transport.Require(opened.Transport, "react", transport.CanReact); err != nil {
				return err
			}

			added, err := opened.Transport.React(cmd.Context(), chat.ReactRequest{
				Message: args[0],
				Emoji:   args[1],
			})
			if err != nil {
				return finish(r, opened, err)
			}

			row := reactionRow{Name: added.Name, Message: args[0], Emoji: args[1]}
			if added.Emoji != nil && added.Emoji.Unicode != "" {
				row.Emoji = added.Emoji.Unicode
			}
			if added.User != nil {
				row.User = added.User.Name
			}

			return r.Result(row, output.Fields{
				{Label: "reacted", Value: args[0]},
				{Label: "emoji", Value: row.Emoji},
			})
		},
	}
}
