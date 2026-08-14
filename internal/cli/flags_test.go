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
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// secretish are fragments of a flag name that suggest the flag carries a
// credential. Deliberately broad, because the cost of a false positive is one
// line in notASecret and the cost of a false negative is a token in the shell
// history of everybody who ran the command.
var secretish = []string{"secret", "password", "passwd", "token", "credential", "webhook", "key"}

// notASecret names the flags that trip the check and are not credentials.
//
// It is a forcing function rather than an allowlist to be topped up. Every
// entry is a sentence somebody wrote on purpose, and the reason is here rather
// than in a commit message because the next person to add one reads this and
// not the history.
var notASecret = map[string]string{
	// Predicted by the card that added `send`, and it arrived exactly as
	// predicted. A thread key is a name a caller chooses for a conversation so
	// that later messages can join it; it authenticates nothing, it grants
	// nothing, and anybody who can already post to the space can read every
	// thread key in it by looking. It trips the check because the check matches
	// the fragment "key", which is the right thing for the check to do: the
	// cost of it firing here is this paragraph, and the cost of it not firing
	// on a real credential is that credential in everybody's shell history.
	"thread-key": "a thread identifier, chosen by the caller and visible to anybody in the space",
}

// TestNoFlagTakesASecret holds SPEC.md §15 and the invariant behind it.
//
// A credential reaches this tool from the keyring, from the environment, or
// from a file, and never as a flag value. An argument lands in the shell
// history, where it outlives the session, and in the process list, where every
// other user on the machine can read it while the command runs. Neither is
// something the operator can undo once it has happened.
//
// The check is on the name rather than on where the value ends up, because a
// name is what a user types and what documentation tells them to type. A flag
// called --webhook-url invites a webhook URL on the command line whatever the
// implementation does with it afterwards.
func TestNoFlagTakesASecret(t *testing.T) {
	var offenders []string

	walkCommands(New(&Options{}), func(cmd *cobra.Command) {
		cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if _, allowed := notASecret[f.Name]; allowed {
				return
			}
			for _, fragment := range secretish {
				if strings.Contains(f.Name, fragment) {
					offenders = append(offenders, cmd.CommandPath()+" --"+f.Name)
					return
				}
			}
		})
	})

	for _, offender := range offenders {
		t.Errorf("%s looks like it takes a credential as a flag value.\n"+
			"An argument lands in the shell history and in the process list, where every other "+
			"user on the machine can read it. Read the secret from stdin or from the "+
			"environment instead.\n"+
			"If this flag is not a credential, add it to notASecret with the reason.", offender)
	}
}

func walkCommands(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, child := range cmd.Commands() {
		walkCommands(child, visit)
	}
}
