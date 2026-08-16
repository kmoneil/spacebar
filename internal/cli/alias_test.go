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
	"encoding/json"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
)

// TestAnAliasSurvivesTheConfigRoundTrip, which is the card's first falsifiable
// claim. It is stored as the space it resolved to, never as what was typed.
func TestAnAliasSurvivesTheConfigRoundTrip(t *testing.T) {
	isolate(t)
	if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
		t.Fatalf("set-webhook: exit %d\n%s", setup.exit, setup.stderr)
	}

	got := runCLIIn(t, "", "alias", "set", "eng", "spaces/AAAATestSpace")
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", got.exit, got.stderr)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if space := cfg.Profiles["alerts"].Aliases["eng"]; space != "spaces/AAAATestSpace" {
		t.Errorf("the alias round-tripped as %q", space)
	}

	list := runCLIIn(t, "", "alias", "list", "--json")
	var row aliasRow
	if err := json.Unmarshal([]byte(list.stdout), &row); err != nil {
		t.Fatalf("alias list --json is not one object per line: %v\n%s", err, list.stdout)
	}
	if row.Name != "eng" || row.Space != "spaces/AAAATestSpace" {
		t.Errorf("listed as %+v", row)
	}
}

// TestAnAliasNoResolutionStepCouldReachIsRefused.
//
// The card asked whether an alias may shadow a literal spaces/XXXX and said it
// must not. The same question applies to an address, which the card does not
// mention: resolution reads an @ as a person at step three, so an alias called
// bob@example.com would silently shadow the direct message with bob.
//
// One character rule answers both, which is why it is a rule about the name
// rather than two special cases. A fifth resolution step would otherwise have
// to remember to add a third.
func TestAnAliasNoResolutionStepCouldReachIsRefused(t *testing.T) {
	isolate(t)
	if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
		t.Fatalf("set-webhook: exit %d", setup.exit)
	}

	for _, tc := range []struct{ name, why string }{
		{"spaces/AAAA", "would never be consulted"},
		{"bob@example.com", "would shadow the direct message"},
		{"a/b", "would never be consulted"},
		{"", "has no name"},
		{"-leading", "does not start with a letter or a digit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runCLIIn(t, "", "alias", "set", tc.name, "spaces/AAAATestSpace")
			if got.exit != output.ExitUsage {
				t.Fatalf("exit = %d, want %d\n%s", got.exit, output.ExitUsage, got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("a failing command wrote to stdout: %q", got.stdout)
			}

			cfg, _ := config.Load()
			if _, ok := cfg.Profiles["alerts"].Aliases[tc.name]; ok {
				t.Errorf("%q was stored despite being refused", tc.name)
			}
		})
	}
}

// TestTheSlashAndAtRefusalsSayWhy, because somebody typing either is not making
// a typo. They are expecting resolution to work a way it does not, and a
// message about the permitted character set would not tell them that.
func TestTheSlashAndAtRefusalsSayWhy(t *testing.T) {
	isolate(t)
	if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
		t.Fatalf("set-webhook: exit %d", setup.exit)
	}

	slash := runCLIIn(t, "", "alias", "set", "spaces/AAAA", "spaces/AAAATestSpace")
	if !strings.Contains(slash.stderr, "resolved as a space directly") {
		t.Errorf("the slash refusal does not say why:\n%s", slash.stderr)
	}

	at := runCLIIn(t, "", "alias", "set", "bob@example.com", "spaces/AAAATestSpace")
	if !strings.Contains(at.stderr, "shadow the direct message") {
		t.Errorf("the @ refusal does not say why:\n%s", at.stderr)
	}
}

// TestAnAliasIsInvisibleFromAnotherProfile, which is the card's "done when".
//
// Aliases live in the profile, so two people sharing a machine, or one person
// with a work and a personal profile, cannot resolve each other's names. The
// failure this prevents is a name meaning one thing today and another after
// --profile changes.
func TestAnAliasIsInvisibleFromAnotherProfile(t *testing.T) {
	isolate(t)
	for _, name := range []string{"alerts", "releases"} {
		if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", name); setup.exit != output.ExitOK {
			t.Fatalf("set-webhook %s: exit %d\n%s", name, setup.exit, setup.stderr)
		}
	}

	if got := runCLIIn(t, "", "alias", "set", "eng", "spaces/AAAATestSpace", "--profile", "alerts"); got.exit != output.ExitOK {
		t.Fatalf("alias set: exit %d\n%s", got.exit, got.stderr)
	}

	other := runCLIIn(t, "", "alias", "list", "--profile", "releases")
	if other.exit != output.ExitOK {
		t.Fatalf("alias list on the other profile: exit %d\n%s", other.exit, other.stderr)
	}
	if other.stdout != "" {
		t.Errorf("another profile can see the alias:\n%s", other.stdout)
	}
	if !strings.Contains(other.stderr, "no aliases") {
		t.Errorf("the empty case does not say so on stderr:\n%s", other.stderr)
	}

	// And the one that set it still has it.
	mine := runCLIIn(t, "", "alias", "list", "--profile", "alerts")
	if !strings.Contains(mine.stdout, "eng") {
		t.Errorf("the alias is missing from the profile that set it:\n%s", mine.stdout)
	}
}

// TestRemovingAnAliasThatIsNotThereFails.
//
// Exit 0 would tell a script that a name it expected to remove had been
// removed. "There was no such alias" is the answer to the question that was
// asked, and it is a different answer.
func TestRemovingAnAliasThatIsNotThereFails(t *testing.T) {
	isolate(t)
	if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
		t.Fatalf("set-webhook: exit %d", setup.exit)
	}

	got := runCLIIn(t, "", "alias", "rm", "nothing")
	if got.exit != output.ExitUsage {
		t.Errorf("exit = %d, want %d\n%s", got.exit, output.ExitUsage, got.stderr)
	}
}

// TestAliasRoundTripThenRemove, the whole cycle, because set and rm sharing a
// map is where a delete on a nil map or a lost profile write would show up.
func TestAliasRoundTripThenRemove(t *testing.T) {
	isolate(t)
	if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
		t.Fatalf("set-webhook: exit %d", setup.exit)
	}

	if got := runCLIIn(t, "", "alias", "set", "eng", "spaces/AAAATestSpace"); got.exit != output.ExitOK {
		t.Fatalf("set: exit %d\n%s", got.exit, got.stderr)
	}
	if got := runCLIIn(t, "", "alias", "rm", "eng"); got.exit != output.ExitOK {
		t.Fatalf("rm: exit %d\n%s", got.exit, got.stderr)
	}

	cfg, _ := config.Load()
	if _, ok := cfg.Profiles["alerts"].Aliases["eng"]; ok {
		t.Error("the alias survived a remove")
	}

	// The profile is still there. Removing an alias is not removing a profile.
	if _, ok := cfg.Profiles["alerts"]; !ok {
		t.Error("removing an alias removed the profile")
	}
}

// TestADryRunOfAliasSetStoresNothing, the same rule profile set-webhook has: a
// dry run of a command that writes locally means the whole command, because the
// other reading is a --dry-run that wrote to disk.
func TestADryRunOfAliasSetStoresNothing(t *testing.T) {
	isolate(t)
	if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
		t.Fatalf("set-webhook: exit %d", setup.exit)
	}

	got := runCLIIn(t, "", "alias", "set", "eng", "spaces/AAAATestSpace", "--dry-run")
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", got.exit, got.stderr)
	}

	cfg, _ := config.Load()
	if _, ok := cfg.Profiles["alerts"].Aliases["eng"]; ok {
		t.Error("a dry run stored the alias")
	}

	// And rm, for the same reason.
	if set := runCLIIn(t, "", "alias", "set", "eng", "spaces/AAAATestSpace"); set.exit != output.ExitOK {
		t.Fatalf("set: exit %d", set.exit)
	}
	if dry := runCLIIn(t, "", "alias", "rm", "eng", "--dry-run"); dry.exit != output.ExitOK {
		t.Fatalf("rm --dry-run: exit %d\n%s", dry.exit, dry.stderr)
	}
	cfg, _ = config.Load()
	if _, ok := cfg.Profiles["alerts"].Aliases["eng"]; !ok {
		t.Error("a dry run removed the alias")
	}
}

// TestAnAliasWorksOnAWebhookProfile, which is the point of it being step two.
// A profile that cannot read can still be told what a name means, and the
// resolution that follows makes no request.
func TestAnAliasWorksOnAWebhookProfile(t *testing.T) {
	isolate(t)
	if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
		t.Fatalf("set-webhook: exit %d", setup.exit)
	}
	if got := runCLIIn(t, "", "alias", "set", "here", "spaces/AAAATestSpace"); got.exit != output.ExitOK {
		t.Fatalf("alias set on a webhook: exit %d\n%s", got.exit, got.stderr)
	}

	// The webhook's own space, reached by its alias, under --dry-run so that
	// nothing is sent.
	got := runCLIIn(t, "", "send", "here", "deploy done", "--dry-run")
	if got.exit != output.ExitOK {
		t.Fatalf("send through an alias: exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stdout, "spaces/AAAATestSpace") {
		t.Errorf("the alias did not reach the request:\n%s", got.stdout)
	}
}
