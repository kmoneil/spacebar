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
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// authorized writes a stored token for a profile, standing in for a consent
// that already happened. The flow itself is tested in internal/auth against a
// fake authorization server; what is tested here is the command around it.
func authorized(t *testing.T, name string, age time.Duration) {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	cfg.Profiles[name] = config.Profile{
		Transport: config.TransportUserOAuth,
		ClientID:  "1234.apps.googleusercontent.example",
		Scopes:    []string{auth.ScopeMessages},
	}
	cfg.DefaultProfile = name
	if err := cfg.Save(); err != nil {
		t.Fatalf("saving the configuration: %v", err)
	}

	store, err := auth.New()
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	if err := store.SaveToken(name, &auth.Token{
		AccessToken:  "ya29.access",
		RefreshToken: "1//refresh",
		Expiry:       time.Now().Add(time.Hour),
		TokenType:    "Bearer",
		Scopes:       []string{auth.ScopeMessages},
		ObtainedAt:   time.Now().Add(-age),
	}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
}

// TestAuthStatusAnswersWithoutTheNetwork.
//
// It is the command somebody runs when something is wrong, so it has to answer
// on a machine whose connection is not working. Nothing here refreshes.
func TestAuthStatusAnswersWithoutTheNetwork(t *testing.T) {
	isolate(t)
	authorized(t, "work", 2*24*time.Hour)

	got := runCLIIn(t, "", "--json", "auth", "status")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	var status auth.Status
	if err := json.Unmarshal([]byte(got.stdout), &status); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, got.stdout)
	}
	if status.Profile != "work" || status.Transport != "useroauth" {
		t.Errorf("status = %+v", status)
	}
	if status.NeedsReauth {
		t.Error("a token two days old was reported as needing a re-authorization")
	}
	if status.DaysRemaining == nil || *status.DaysRemaining < 4.5 {
		t.Errorf("days remaining = %v, want about 5", status.DaysRemaining)
	}

	// No token is a status rather than a failure: "not authorized" is the
	// answer to the question that was asked, and a non-zero exit would make a
	// script unable to ask it.
	isolate(t)
	cfg, _ := config.Load()
	cfg.Profiles = map[string]config.Profile{"work": {Transport: config.TransportUserOAuth}}
	cfg.DefaultProfile = "work"
	if err := cfg.Save(); err != nil {
		t.Fatalf("saving: %v", err)
	}

	got = runCLIIn(t, "", "--json", "auth", "status")
	if got.exit != output.ExitOK {
		t.Fatalf("a profile with no token exited %d", got.exit)
	}
	if err := json.Unmarshal([]byte(got.stdout), &status); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if !status.NeedsReauth {
		t.Error("a profile with no token reported itself authorized")
	}
}

// TestTheExpiryWarningIsOnStderrAndStdoutStaysClean, which is the card's own
// claim and the reason --json mode is worth checking separately: a warning that
// landed on stdout would corrupt the document a caller is parsing.
func TestTheExpiryWarningIsOnStderrAndStdoutStaysClean(t *testing.T) {
	isolate(t)
	authorized(t, "work", 6*24*time.Hour+time.Hour)

	got := runCLIIn(t, "", "auth", "status")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "still in testing") {
		t.Errorf("nothing warned about the boundary:\n%s", got.stderr)
	}
	if strings.Contains(got.stdout, "still in testing") {
		t.Errorf("the warning reached stdout:\n%s", got.stdout)
	}

	// In --json mode the warning is still on stderr, as one object per line,
	// and stdout is still exactly one document.
	got = runCLIIn(t, "", "--json", "auth", "status")
	if !strings.Contains(got.stderr, `"warning"`) {
		t.Errorf("the warning is not a JSON document on stderr:\n%s", got.stderr)
	}

	var status auth.Status
	if err := json.Unmarshal([]byte(got.stdout), &status); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, got.stdout)
	}
	// And it is repeated in the result, so that a caller reading only stdout is
	// not the one person who does not get it.
	if status.Warning == "" {
		t.Error("the warning is absent from the JSON result")
	}
}

// TestAFreshTokenWarnsAboutNothing, so that the warning means something when it
// does appear.
func TestAFreshTokenWarnsAboutNothing(t *testing.T) {
	isolate(t)
	authorized(t, "work", 3*24*time.Hour)

	got := runCLIIn(t, "", "auth", "status")
	if strings.Contains(got.stderr, "still in testing") {
		t.Errorf("a token three days old was warned about:\n%s", got.stderr)
	}
}

// TestAuthLogoutForgetsTheTokenAndKeepsTheProfile.
//
// There is no confirmation, unlike removing a profile, and the difference is
// recoverability: an authorization comes back by consenting again, and a webhook
// URL only comes back from the space it was created in.
func TestAuthLogoutForgetsTheTokenAndKeepsTheProfile(t *testing.T) {
	isolate(t)
	authorized(t, "work", time.Hour)

	got := runCLIIn(t, "", "auth", "logout")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	store, err := auth.New()
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	if _, err := store.LoadToken("work"); err == nil {
		t.Error("the token survived a logout")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, ok := cfg.Profiles["work"]; !ok {
		t.Error("logging out removed the profile, and authorizing again should need no other setup")
	}

	// Twice is not a failure. A logout that had already happened is a logout
	// that succeeded.
	if again := runCLIIn(t, "", "auth", "logout"); again.exit != output.ExitOK {
		t.Errorf("a second logout exited %d", again.exit)
	}
}

// TestLoginWithNoClientSaysWhatToDo.
//
// The first thing somebody hits on a build from source, where the client is
// deliberately empty. It named no command until Milestone 3, because
// `auth setup` did not exist and sending somebody from one dead end to another
// is worse than the first. This test asserted that absence, so when the command
// arrived the test went on holding the refusal to the older build's shape,
// which is how a stale assertion outlives the condition it was written for.
//
// It now asserts the opposite, and it asserts both halves: the command that
// stores a client, and the environment variables, which still work and are
// still what a scripted install uses.
func TestLoginWithNoClientSaysWhatToDo(t *testing.T) {
	isolate(t)
	t.Setenv(config.Env("CLIENT_ID"), "")

	got := runCLIIn(t, "", "auth", "login", "--profile", "work")
	if got.exit != output.ExitUsage {
		t.Fatalf("exit = %d, want %d\n%s", got.exit, output.ExitUsage, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("a failing command wrote to stdout: %q", got.stdout)
	}
	for _, want := range []string{
		config.Env("CLIENT_ID"),
		config.Env("CLIENT_SECRET"),
		"Internal",
		meta.AppName + " auth setup --profile NAME < client_secret.json",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the failure does not mention %q:\n%s", want, got.stderr)
		}
	}
}

// TestLoginWithNoProfileNameRefusesToGuess. A default invented here would
// authorize something the caller did not name.
func TestLoginWithNoProfileNameRefusesToGuess(t *testing.T) {
	isolate(t)
	t.Setenv(config.Env("CLIENT_ID"), "1234.apps.googleusercontent.example")

	got := runCLIIn(t, "", "auth", "login")
	if got.exit != output.ExitUsage {
		t.Fatalf("exit = %d, want %d\n%s", got.exit, output.ExitUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "--profile") {
		t.Errorf("the failure does not say how to name one:\n%s", got.stderr)
	}
}

// TestADryRunOfLoginConsentsToNothing.
//
// A dry run must not consent. Issuing a token is a side effect at Google's end
// and on this machine, and somebody who typed --dry-run to find out what would
// happen would have had it happen. So no browser opens, no listener binds, and
// nothing is stored.
func TestADryRunOfLoginConsentsToNothing(t *testing.T) {
	isolate(t)
	t.Setenv(config.Env("CLIENT_ID"), "1234.apps.googleusercontent.example")

	got := runCLIIn(t, "", "--dry-run", "auth", "login", "--profile", "work")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	for _, want := range []string{"work", "1234.apps.googleusercontent.example", auth.AuthEndpoint} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the dry run does not show %q:\n%s", want, got.stdout)
		}
	}

	store, err := auth.New()
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	if _, err := store.LoadToken("work"); err == nil {
		t.Error("a dry run stored a token")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, ok := cfg.Profiles["work"]; ok {
		t.Error("a dry run created a profile")
	}
}

// TestNoTokenReachesTheOutputOfAnyAuthCommand.
//
// The stored value is a credential and three commands read it. What a caller
// can act on is which profile has one, what it may do, and when it might stop
// working, and none of that is the token.
func TestNoTokenReachesTheOutputOfAnyAuthCommand(t *testing.T) {
	isolate(t)
	authorized(t, "work", 6*24*time.Hour+time.Hour)

	for _, args := range [][]string{
		{"auth", "status"},
		{"--json", "auth", "status"},
		{"--verbose", "auth", "status"},
		{"auth", "logout"},
	} {
		got := runCLIIn(t, "", args...)
		both := got.stdout + "\n" + got.stderr

		for _, secret := range []string{"ya29.access", "1//refresh"} {
			if strings.Contains(both, secret) {
				t.Errorf("%v printed a credential:\n%s", args, both)
			}
		}
	}
}

// consoleFile is what the Cloud console downloads for a desktop client, with
// endpoints that point somewhere they must never be followed to.
const consoleFile = `{"installed":{"client_id":"1234-abc.apps.googleusercontent.example",` +
	`"project_id":"a-project","auth_uri":"https://evil.invalid/steal",` +
	`"token_uri":"https://evil.invalid/collect","client_secret":"GOCSPX-notARealSecret"}}`

// TestSetupWithNothingOnStdinPrintsTheWalkthrough.
//
// This is the shape the card's third recon question decided. Setup cannot be a
// wizard: the people who need their own OAuth client are the ones whose
// organization blocks third-party applications, and they are on managed
// laptops, jump hosts and CI runners. So it prompts for nothing, blocks on
// nothing, needs no terminal, and is fully useful with no browser.
func TestSetupWithNothingOnStdinPrintsTheWalkthrough(t *testing.T) {
	isolate(t)

	got := runCLIIn(t, "", "auth", "setup", "--profile", "work")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	// Instructions are not a result. A caller in a script is not parsing prose.
	if got.stdout != "" {
		t.Errorf("the walkthrough reached stdout:\n%s", got.stdout)
	}
	for _, want := range []string{
		"Desktop app",         // the type that matters and is easy to get wrong.
		"Internal",            // the user type that avoids both limits.
		"chat.googleapis.com", // the API to enable.
		"seven-day",           // why Internal matters.
		"client_secret.json",  // what to do next.
		"profile set-webhook", // the way out for somebody with no Cloud project at all.
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the walkthrough does not mention %q:\n%s", want, got.stderr)
		}
	}
}

// TestSetupStoresTheConsoleFileAndIgnoresItsEndpoints.
//
// The ignoring is the security half. A client file carries auth_uri and
// token_uri, and a doctored one would send the consent screen and the client
// secret somewhere else if those were honoured.
func TestSetupStoresTheConsoleFileAndIgnoresItsEndpoints(t *testing.T) {
	isolate(t)

	got := runCLIIn(t, consoleFile, "auth", "setup", "--profile", "work")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if strings.Contains(got.stdout+got.stderr, "GOCSPX-notARealSecret") {
		t.Errorf("the client secret was printed:\n%s%s", got.stdout, got.stderr)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	profile := cfg.Profiles["work"]
	if profile.ClientID != "1234-abc.apps.googleusercontent.example" {
		t.Errorf("client_id = %q", profile.ClientID)
	}
	if !strings.HasPrefix(profile.ClientSecretRef, config.RefScheme) {
		t.Errorf("client_secret_ref = %q, and a secret never lands in the file", profile.ClientSecretRef)
	}

	// A later authorization uses the constant endpoint, not the file's.
	next := runCLIIn(t, "", "--json", "--dry-run", "auth", "login", "--profile", "work")
	if next.exit != output.ExitOK {
		t.Fatalf("dry run exit %d\n%s", next.exit, next.stderr)
	}
	if strings.Contains(next.stdout, "evil.invalid") {
		t.Errorf("a doctored endpoint reached the flow:\n%s", next.stdout)
	}
	if !strings.Contains(next.stdout, auth.AuthEndpoint) {
		t.Errorf("the constant endpoint is not what would be used:\n%s", next.stdout)
	}
}

// TestSetupDryRunStoresNothing.
func TestSetupDryRunStoresNothing(t *testing.T) {
	isolate(t)

	got := runCLIIn(t, consoleFile, "--dry-run", "auth", "setup", "--profile", "work")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, ok := cfg.Profiles["work"]; ok {
		t.Error("a dry run stored a client")
	}
}

// TestSendOnlyAsksForExactlyOneScope, which is the card's own claim. A mode
// that quietly asked for more would defeat the point of having it: the reason
// it exists is that a narrower scope materially improves the odds of an
// administrator approving the application at all.
func TestSendOnlyAsksForExactlyOneScope(t *testing.T) {
	isolate(t)
	t.Setenv(config.Env("CLIENT_ID"), "1234.apps.googleusercontent.example")

	got := runCLIIn(t, "", "--json", "--dry-run", "auth", "login", "--profile", "work", "--send-only")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	var result struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &result); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, got.stdout)
	}
	if len(result.Scopes) != 1 || result.Scopes[0] != auth.ScopeSendOnly {
		t.Errorf("--send-only asked for %v, want exactly the create scope", result.Scopes)
	}

	// And without it, the default set, which is still narrow.
	got = runCLIIn(t, "", "--json", "--dry-run", "auth", "login", "--profile", "work")
	if err := json.Unmarshal([]byte(got.stdout), &result); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	for _, scope := range result.Scopes {
		if scope == auth.ScopeSpaces {
			t.Errorf("the default asks for %q, which nothing uses yet", scope)
		}
	}
}

// TestSetupSaysWhatIsAlreadyConfigured, so that running it twice is informative
// rather than confusing.
func TestSetupSaysWhatIsAlreadyConfigured(t *testing.T) {
	isolate(t)

	if got := runCLIIn(t, consoleFile, "auth", "setup", "--profile", "work"); got.exit != output.ExitOK {
		t.Fatalf("setup: exit %d\n%s", got.exit, got.stderr)
	}

	got := runCLIIn(t, "", "auth", "setup", "--profile", "work")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "already has an OAuth client") {
		t.Errorf("a second run did not say one was configured:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "1234-abc.apps.googleusercontent.example") {
		t.Errorf("it did not say which:\n%s", got.stderr)
	}
}

// TestSetupRefusesAWebClientWithTheReason, because picking the wrong
// application type in the console is an easy mistake whose file parses fine.
func TestSetupRefusesAWebClientWithTheReason(t *testing.T) {
	isolate(t)

	web := strings.Replace(consoleFile, `"installed"`, `"web"`, 1)
	got := runCLIIn(t, web, "auth", "setup", "--profile", "work")
	if got.exit != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", got.exit, output.ExitUsage)
	}
	if !strings.Contains(got.stderr, "Desktop app") {
		t.Errorf("the refusal does not say which type to pick:\n%s", got.stderr)
	}
}

// TestSetupAtATerminalDoesNotWaitForInput.
//
// Found by running the command rather than by a test failing, which is the
// point of running it. `auth setup` with no redirection blocked on stdin
// forever: the command whose whole job is to print instructions showed a cursor
// and nothing else, to somebody who typed it because they did not know what to
// do.
//
// The distinction is worth stating, because two other commands here do block
// and are right to. `profile set-webhook` and `send -` exist to receive a
// value, so waiting is what was asked for and both say so before they wait.
// This one's default is to print, so waiting is an answer to a question nobody
// asked.
func TestSetupAtATerminalDoesNotWaitForInput(t *testing.T) {
	// A reader that fails the test if it is touched. Reading it is the bug,
	// whatever comes back.
	body, err := clientFileFrom(stdinThatMustNotBeRead{t}, true)
	if err != nil {
		t.Fatalf("clientFileFrom: %v", err)
	}
	if body != nil {
		t.Errorf("something was read at a terminal: %q", body)
	}

	// Piped in, it reads, because that is how the file arrives.
	body, err = clientFileFrom(strings.NewReader(consoleFile), false)
	if err != nil {
		t.Fatalf("clientFileFrom: %v", err)
	}
	if len(body) == 0 {
		t.Error("nothing was read from a pipe")
	}
}

type stdinThatMustNotBeRead struct{ t *testing.T }

func (s stdinThatMustNotBeRead) Read([]byte) (int, error) {
	s.t.Error("auth setup read stdin at a terminal, which blocks until the user gives up")
	return 0, io.EOF
}

// TestTheWalkthroughNeedsNoProfile.
//
// `spacebar auth setup` and nothing else is what somebody types when they do
// not yet know what any of this is, and demanding a profile name would refuse
// exactly that. Storing a client does need one, because it has to be filed
// against something. Found by running the command with no arguments, which
// failed at exit 2.
func TestTheWalkthroughNeedsNoProfile(t *testing.T) {
	isolate(t)

	got := runCLIIn(t, "", "auth", "setup")
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(got.stderr, "Desktop app") {
		t.Errorf("the walkthrough did not print:\n%s", got.stderr)
	}
	// The commands at the end are meant to be pasted, so an unnamed profile
	// reads as a placeholder rather than as a guess the reader has to notice.
	if !strings.Contains(got.stderr, "--profile NAME") {
		t.Errorf("the walkthrough named a profile nobody chose:\n%s", got.stderr)
	}

	// Storing one still needs a name.
	stored := runCLIIn(t, consoleFile, "auth", "setup")
	if stored.exit != output.ExitUsage {
		t.Errorf("storing a client with no profile exited %d, want %d", stored.exit, output.ExitUsage)
	}
}

// TestAuthRefusesAProfileThatCannotHoldAnAuthorization.
//
// Found by the Milestone 3 exit sweep, by running the auth group against a
// webhook profile rather than by reading anything. All three commands assumed
// a profile that could hold a token and none of them said so, and each was
// wrong differently:
//
//   - `auth setup` filed an OAuth client ID and secret against a profile whose
//     transport is webhook, exited 0, and printed "now authorize it".
//   - `auth login` said "no OAuth client is configured", which is a reason that
//     sends somebody to spend five minutes in the Cloud console creating one
//     they cannot use.
//   - `auth logout` said "logged out" for a profile that held nothing to
//     forget, while the credential a person means when they type that, the
//     webhook URL, stayed exactly where it was. A false report to somebody
//     trying to remove their access is the worst of the three.
func TestAuthRefusesAProfileThatCannotHoldAnAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"setup", []string{"auth", "setup", "--profile", "alerts"}, consoleFile},
		{"login", []string{"auth", "login", "--profile", "alerts"}, ""},
		{"logout", []string{"auth", "logout", "--profile", "alerts"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			if setup := runCLIIn(t, testWebhook, "profile", "set-webhook", "alerts"); setup.exit != output.ExitOK {
				t.Fatalf("set-webhook: exit %d\n%s", setup.exit, setup.stderr)
			}

			got := runCLIIn(t, tc.stdin, tc.args...)
			if got.exit != output.ExitUnsupported {
				t.Fatalf("exit = %d, want %d\n%s%s", got.exit, output.ExitUnsupported, got.stdout, got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("a failing command wrote to stdout: %q", got.stdout)
			}

			for _, want := range []string{
				"alerts",     // which profile.
				"webhook",    // what it is.
				"auth setup", // how to get one that can.
				"profile rm", // how to remove this one and its URL.
			} {
				if !strings.Contains(got.stderr, want) {
					t.Errorf("the refusal does not mention %q:\n%s", want, got.stderr)
				}
			}

			// The webhook is untouched. This is the half that made the logout
			// case a false report rather than a harmless one.
			after := runCLIIn(t, "", "send", "--dry-run", "deploy done")
			if after.exit != output.ExitOK {
				t.Errorf("the webhook stopped working after %v: exit %d\n%s", tc.args, after.exit, after.stderr)
			}
		})
	}
}

// TestAuthSetupStillCreatesAProfileThatDoesNotExistYet.
//
// The other side of the refusal above, and the reason it checks the transport
// rather than merely requiring the profile to be configured. `auth setup
// --profile work` on a fresh machine is how a user-OAuth profile comes into
// existence, and it is the invocation the README and docs/ADMIN.md both tell
// people to type.
func TestAuthSetupStillCreatesAProfileThatDoesNotExistYet(t *testing.T) {
	isolate(t)

	got := runCLIIn(t, consoleFile, "auth", "setup", "--profile", "brandnew")
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", got.exit, got.stderr)
	}

	list := runCLIIn(t, "", "profile", "list", "--json")
	if !strings.Contains(list.stdout, `"name":"brandnew"`) {
		t.Errorf("the profile was not created:\n%s", list.stdout)
	}
	if !strings.Contains(list.stdout, `"transport":"useroauth"`) {
		t.Errorf("the profile it created cannot hold the authorization it just set up:\n%s", list.stdout)
	}
}

// TestARemovalThatCouldNotHappenExitsNonZero is the exit code a script sees,
// which is the only part of this a script can act on.
//
// `profile rm` and `auth logout` both discarded what the credential store told
// them, so a fallback file at the wrong mode meant each printed its success
// line, exited 0, and left the credential on disk. The store-level tests hold
// the contract; this holds that the contract reaches the process.
//
// The keyring is mocked into failing, because that is the machine the fallback
// file exists for: a container, a CI runner, a headless server. With a working
// keyring the removal succeeds there and never consults the file at all.
func TestARemovalThatCouldNotHappenExitsNonZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"profile rm", []string{"--yes", "profile", "rm", "work"}, "removed"},
		{"auth logout", []string{"auth", "logout", "--profile", "work"}, "logged out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := isolate(t)
			keyring.MockInitWithError(errors.New("no keyring on this machine"))

			// A configured user-OAuth profile whose credential is in a fallback
			// file this package will refuse to read.
			cfg := &config.Config{
				DefaultProfile: "work",
				Profiles: map[string]config.Profile{
					"work": {Transport: config.TransportUserOAuth, ClientID: "x.apps.googleusercontent.com"},
				},
			}
			if err := cfg.SaveTo(path); err != nil {
				t.Fatalf("writing the configuration: %v", err)
			}

			creds := filepath.Join(filepath.Dir(path), auth.CredentialsFile)
			if err := os.WriteFile(creds, []byte(`{"spacebar/work/token":"1//a-refresh-token"}`), 0o644); err != nil {
				t.Fatalf("writing the fallback file: %v", err)
			}

			got := runCLIIn(t, "", tc.args...)
			if got.exit == output.ExitOK {
				t.Fatalf("exit 0 over a credential that is still on disk\nstdout: %s\nstderr: %s",
					got.stdout, got.stderr)
			}
			if strings.Contains(got.stdout, tc.says) {
				t.Errorf("stdout says %q for something that did not happen:\n%s", tc.says, got.stdout)
			}
			if !strings.Contains(got.stderr, "chmod") {
				t.Errorf("the failure does not say what to do:\n%s", got.stderr)
			}

			// And it is still there, which is what makes the failure true.
			body, err := os.ReadFile(creds)
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if !strings.Contains(string(body), "a-refresh-token") {
				t.Error("the test is not exercising what it claims: the secret is gone")
			}
		})
	}
}
