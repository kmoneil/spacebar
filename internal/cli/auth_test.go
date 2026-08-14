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
	"time"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/config"
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
// deliberately empty. The message deliberately does not name `auth setup`,
// which SPEC.md §6.1 words it with, because that command does not exist in this
// build and sending somebody from one dead end to another is worse than the
// first.
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
	for _, want := range []string{config.Env("CLIENT_ID"), config.Env("CLIENT_SECRET"), "Internal"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the failure does not mention %q:\n%s", want, got.stderr)
		}
	}
	if strings.Contains(got.stderr, "auth setup") {
		t.Errorf("the failure names a command this build does not have:\n%s", got.stderr)
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
