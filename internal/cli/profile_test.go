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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// The credentials a test webhook URL carries, distinctive enough that a test
// can grep a file or a stream for them.
const (
	testKey     = "AIzaSyTestKeyNotARealOne0123456789"
	testToken   = "sQ7testTokenNotARealOne0123456789"
	testWebhook = "https://chat.googleapis.com/v1/spaces/AAAATestSpace/messages" +
		"?key=" + testKey + "&token=" + testToken
)

// isolate gives one test its own configuration directory and its own keyring.
//
// The keyring is mocked because the real one is the machine's: a test that
// wrote to it would leave an entry in somebody's login keychain, which is a
// test with a side effect outside the repository and no way to notice.
// The cache directory is redirected as well as the configuration one. They are
// separate roots, so isolating one leaves the other pointing at the machine's,
// and a test that resolved a display name would write this account's space list
// into the home directory of whoever ran it. Nothing does that today, which is
// exactly why it is worth setting before something does.
func isolate(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv(config.Env("WEBHOOK_URL"), "")
	keyring.MockInit()

	return filepath.Join(dir, meta.AppName, config.FileName)
}

// TestTheWholeSetupIsOneCommand is the falsifiable claim on the card, run.
//
// Start from an empty configuration directory, use only documented commands,
// and land at a configured profile with no hand-editing of any file. The card
// says the transcript is wrong if it is longer than four commands. It is one,
// and this counts them rather than trusting the prose.
//
// The last step of the claim, a successful send, arrives with the command that
// sends. What is settled here is everything up to it.
func TestTheWholeSetupIsOneCommand(t *testing.T) {
	path := isolate(t)

	transcript := [][]string{
		{"profile", "set-webhook", "alerts"},
	}
	for _, args := range transcript {
		got := runCLIIn(t, testWebhook+"\n", args...)
		if got.exit != output.ExitOK {
			t.Fatalf("%v: exit %d\n%s", args, got.exit, got.stderr)
		}
	}
	if len(transcript) > 4 {
		t.Errorf("setup takes %d commands, and the design is wrong past four", len(transcript))
	}

	// Configured, and configured as the default, so that nothing downstream
	// needs --profile on every invocation.
	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("the configuration this wrote does not load: %v", err)
	}
	if cfg.DefaultProfile != "alerts" {
		t.Errorf("default_profile = %q, want alerts", cfg.DefaultProfile)
	}
	if cfg.Profiles["alerts"].Transport != config.TransportWebhook {
		t.Errorf("transport = %q", cfg.Profiles["alerts"].Transport)
	}
}

// TestTheCredentialReachesNoStreamAndNoFile.
//
// A webhook URL is a bearer credential wearing the costume of a URL, and this
// is the one command in Milestone 2 that is handed one. Three places it must
// not appear: stdout, stderr, and the configuration file that somebody keeps in
// a dotfiles repository.
func TestTheCredentialReachesNoStreamAndNoFile(t *testing.T) {
	path := isolate(t)

	got := runCLIIn(t, testWebhook+"\n", "profile", "set-webhook", "alerts")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the configuration: %v", err)
	}

	for what, text := range map[string]string{
		"stdout":                 got.stdout,
		"stderr":                 got.stderr,
		"the configuration file": string(body),
	} {
		for _, secret := range []string{testWebhook, testKey, testToken} {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds a credential:\n%s", what, text)
			}
		}
	}

	// The reference is printed, because it is the part that is safe and is what
	// somebody needs in order to find the secret again.
	if !strings.Contains(got.stdout, config.RefScheme) {
		t.Errorf("nothing said where the credential went:\n%s", got.stdout)
	}
}

// TestSetWebhookTakesTheURLFromTheEnvironmentWithoutReadingStdin.
//
// The two paths belong to different callers: a CI runner exports the variable,
// a person pipes the value in. Reading stdin anyway would leave a command that
// has everything it needs waiting on input that is never coming.
func TestSetWebhookTakesTheURLFromTheEnvironmentWithoutReadingStdin(t *testing.T) {
	path := isolate(t)
	t.Setenv(config.Env("WEBHOOK_URL"), testWebhook)

	got := runCLIIn(t, "", "profile", "set-webhook", "ci")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if cfg.Profiles["ci"].WebhookURLRef == "" {
		t.Error("the URL in the environment was not stored")
	}
}

// TestNoURLAnywhereIsAUsageFailureThatSaysWhatToDo.
//
// This is the failure a first-time user hits, so it is the message most worth
// getting right. It has to name both ways in, and it has to say why the obvious
// third way is not offered.
func TestNoURLAnywhereIsAUsageFailureThatSaysWhatToDo(t *testing.T) {
	isolate(t)

	got := runCLIIn(t, "", "profile", "set-webhook", "alerts")
	if got.exit != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", got.exit, output.ExitUsage)
	}
	if got.stdout != "" {
		t.Errorf("a failing command wrote to stdout: %q", got.stdout)
	}
	for _, want := range []string{config.Env("WEBHOOK_URL"), "shell history", "process list"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the failure does not mention %q:\n%s", want, got.stderr)
		}
	}
}

// TestATruncatedURLIsRefusedAtThePasteRatherThanAtTheSend.
//
// A webhook URL is long and is copied by hand out of a dialog, so truncation is
// the common way it goes wrong, and it produces a credential that looks
// complete. Caught here it costs one paste. Caught at send time it is a 400
// whose message is about an API key.
func TestATruncatedURLIsRefusedAtThePasteRatherThanAtTheSend(t *testing.T) {
	path := isolate(t)

	truncated := "https://chat.googleapis.com/v1/spaces/AAAATestSpace/messages?key=" + testKey
	got := runCLIIn(t, truncated+"\n", "profile", "set-webhook", "alerts")
	if got.exit != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", got.exit, output.ExitUsage)
	}
	if strings.Contains(got.stderr, testKey) {
		t.Errorf("the refusal quotes the credential back:\n%s", got.stderr)
	}

	// And nothing was written. A half-configured profile that fails later is
	// worse than one that was never created.
	if _, err := os.Stat(path); err == nil {
		t.Error("a refused URL still wrote a configuration file")
	}
}

// TestProfileListReportsWhatIsConfiguredRatherThanWhatWorks.
//
// It is the command somebody runs when something is wrong, so it must answer on
// a machine whose keyring is locked and for a profile whose secret has been
// deleted. Nothing here reads a credential.
func TestProfileListReportsWhatIsConfiguredRatherThanWhatWorks(t *testing.T) {
	isolate(t)

	if got := runCLIIn(t, testWebhook+"\n", "profile", "set-webhook", "alerts"); got.exit != output.ExitOK {
		t.Fatalf("setup: exit %d\n%s", got.exit, got.stderr)
	}

	// Break the keyring underneath it, which is what a locked one looks like.
	keyring.MockInitWithError(os.ErrPermission)

	got := runCLIIn(t, "", "--json", "profile", "list")
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d with an unreadable keyring: %s", got.exit, got.stderr)
	}

	var info profileInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(got.stdout)), &info); err != nil {
		t.Fatalf("not one JSON object per line: %v\n%s", err, got.stdout)
	}
	if info.Name != "alerts" || info.Transport != string(config.TransportWebhook) {
		t.Errorf("row = %+v", info)
	}
	if !info.Default || !info.Configured {
		t.Errorf("row = %+v, want the default and configured", info)
	}
}

// TestAnEmptyListIsExitZeroAndAnEmptyStdout. Zero results is what a caller
// parsing this has to see, and it is not a failure: nothing went wrong, there
// is simply nothing there.
func TestAnEmptyListIsExitZeroAndAnEmptyStdout(t *testing.T) {
	isolate(t)

	got := runCLIIn(t, "", "profile", "list")
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d, want 0", got.exit)
	}
	if got.stdout != "" {
		t.Errorf("an empty list wrote to stdout: %q", got.stdout)
	}
	if !strings.Contains(got.stderr, "no profiles are configured") {
		t.Errorf("nothing on stderr said the list was empty: %q", got.stderr)
	}
}

// TestRemovingAProfileNeedsAnAnswer.
//
// A webhook URL is only recoverable from the space it was created in, so
// removing one is not undoable from here. In a pipeline there is nobody to ask,
// and that is exit 7 rather than a default: SPEC.md §11.3 refuses rather than
// assuming, because assuming is how a script deletes something nobody meant to.
func TestRemovingAProfileNeedsAnAnswer(t *testing.T) {
	path := isolate(t)

	if got := runCLIIn(t, testWebhook+"\n", "profile", "set-webhook", "alerts"); got.exit != output.ExitOK {
		t.Fatalf("setup: exit %d\n%s", got.exit, got.stderr)
	}

	// runCLIIn drives buffers rather than a terminal, so this is the pipeline
	// case, which is the one with the rule attached.
	got := runCLIIn(t, "y\n", "profile", "rm", "alerts")
	if got.exit != output.ExitRefused {
		t.Fatalf("exit = %d, want %d", got.exit, output.ExitRefused)
	}
	if cfg, err := config.LoadFrom(path); err != nil || cfg.Profiles["alerts"].WebhookURLRef == "" {
		t.Error("the profile was removed without an answer")
	}

	// With --yes there is no question left.
	got = runCLIIn(t, "", "--yes", "profile", "rm", "alerts")
	if got.exit != output.ExitOK {
		t.Fatalf("exit = %d: %s", got.exit, got.stderr)
	}

	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("the configuration no longer loads after a removal: %v", err)
	}
	if _, ok := cfg.Profiles["alerts"]; ok {
		t.Error("the profile is still there")
	}
	if cfg.DefaultProfile != "" {
		t.Errorf("default_profile still names %q, so the file will refuse to load", cfg.DefaultProfile)
	}
}

// TestRemovingAProfileThatIsNotThereIsAUsageFailure, naming what is configured,
// because the likely cause is a typo and the answer is on the next line.
func TestRemovingAProfileThatIsNotThereIsAUsageFailure(t *testing.T) {
	isolate(t)

	if got := runCLIIn(t, testWebhook+"\n", "profile", "set-webhook", "alerts"); got.exit != output.ExitOK {
		t.Fatalf("setup: exit %d\n%s", got.exit, got.stderr)
	}

	got := runCLIIn(t, "", "--yes", "profile", "rm", "alertz")
	if got.exit != output.ExitUsage {
		t.Errorf("exit = %d, want %d", got.exit, output.ExitUsage)
	}
	if !strings.Contains(got.stderr, "alerts") {
		t.Errorf("the failure does not say what is configured:\n%s", got.stderr)
	}
}

// TestVerifyPostsToTheSpaceAndSaysWhere.
//
// A webhook has no endpoint that reports whether it works: the only way to find
// out is to post. So somebody who cannot tell a mistyped URL from an
// organizational unit with Chat apps switched off has no way to learn which
// they have, and this is what gives them one.
func TestVerifyPostsToTheSpaceAndSaysWhere(t *testing.T) {
	isolate(t)

	var posted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		posted = append(posted, string(body))
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	}))
	t.Cleanup(server.Close)

	url := server.URL + "/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
	got := runCLIIn(t, url+"\n", "--json", "profile", "set-webhook", "alerts", "--verify")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	if len(posted) != 1 {
		t.Fatalf("posted %d messages, want 1", len(posted))
	}
	if !strings.Contains(posted[0], meta.AppName) {
		t.Errorf("the verification message does not say what it is: %s", posted[0])
	}

	var result webhookResult
	if err := json.Unmarshal([]byte(got.stdout), &result); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, got.stdout)
	}
	if !result.Verified || result.Space != "spaces/AAAATestSpace" {
		t.Errorf("result = %+v, want verified and the space it reached", result)
	}
}

// TestWithoutVerifyNothingIsPosted. A setup command that put a message into a
// space full of people without being asked would be rude in a way somebody only
// finds out about afterwards.
func TestWithoutVerifyNothingIsPosted(t *testing.T) {
	isolate(t)

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("configuring a webhook posted a message nobody asked for")
	}))
	t.Cleanup(server.Close)

	url := server.URL + "/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
	if got := runCLIIn(t, url+"\n", "profile", "set-webhook", "alerts"); got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	// And the JSON says nothing was checked, rather than saying it failed. A
	// caller has to be able to tell "it did not work" from "nobody looked".
	got := runCLIIn(t, url+"\n", "--json", "profile", "set-webhook", "alerts")
	if strings.Contains(got.stdout, "verified") {
		t.Errorf("a run with no --verify reported a verification: %s", got.stdout)
	}
}

// TestVerifyTextIsTheCallersToChoose, because the message lands in a real space
// and what it says is not this tool's business.
func TestVerifyTextIsTheCallersToChoose(t *testing.T) {
	isolate(t)

	var posted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		posted = string(body)
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	}))
	t.Cleanup(server.Close)

	url := server.URL + "/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
	got := runCLIIn(t, url+"\n", "profile", "set-webhook", "alerts",
		"--verify", "--verify-text", "ignore me, testing the pipeline")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}
	if !strings.Contains(posted, "ignore me, testing the pipeline") {
		t.Errorf("the caller's text was not what was posted: %s", posted)
	}
}

// TestAFailedVerifyStillLeavesTheProfileConfigured.
//
// The credential is stored before the check, so an organizational unit with
// Chat apps switched off does not also cost somebody their configuration. The
// failure says what is wrong; the profile is there to fix or to remove.
func TestAFailedVerifyStillLeavesTheProfileConfigured(t *testing.T) {
	path := isolate(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`)
	}))
	t.Cleanup(server.Close)

	url := server.URL + "/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
	got := runCLIIn(t, url+"\n", "profile", "set-webhook", "alerts", "--verify")
	if got.exit != output.ExitAPI {
		t.Fatalf("exit = %d, want %d\n%s", got.exit, output.ExitAPI, got.stderr)
	}

	// Appendix A.10: the message names the cause somebody cannot see for
	// themselves.
	if !strings.Contains(got.stderr, "Chat apps") {
		t.Errorf("the failure does not name the likely cause:\n%s", got.stderr)
	}
	for _, secret := range []string{testKey, testToken} {
		if strings.Contains(got.stderr, secret) {
			t.Errorf("the failure holds a credential:\n%s", got.stderr)
		}
	}

	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if cfg.Profiles["alerts"].WebhookURLRef == "" {
		t.Error("a failed verification threw away the configuration as well")
	}
}

// TestVerboseLogsTheRequestWithoutTheCredential.
//
// Found by running the built binary rather than by a test failing: --verify
// built a transport with no logger, so --verbose was accepted and printed
// nothing, which is worse than not having the flag. Somebody debugging a
// webhook would have concluded no request was made.
func TestVerboseLogsTheRequestWithoutTheCredential(t *testing.T) {
	isolate(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"name":"spaces/AAAATestSpace/messages/BBB"}`)
	}))
	t.Cleanup(server.Close)

	url := server.URL + "/v1/spaces/AAAATestSpace/messages?key=" + testKey + "&token=" + testToken
	got := runCLIIn(t, url+"\n", "--verbose", "profile", "set-webhook", "alerts", "--verify")
	if got.exit != output.ExitOK {
		t.Fatalf("exit %d\n%s", got.exit, got.stderr)
	}

	for _, want := range []string{"> POST", "key=REDACTED", "token=REDACTED", "< 200 OK"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("--verbose is missing %q:\n%s", want, got.stderr)
		}
	}
	for _, secret := range []string{testKey, testToken} {
		if strings.Contains(got.stderr, secret) {
			t.Errorf("--verbose printed a credential:\n%s", got.stderr)
		}
	}

	// The log is a diagnostic, not a result.
	if strings.Contains(got.stdout, "> POST") {
		t.Errorf("a log line reached stdout:\n%s", got.stdout)
	}

	// And without --verbose there is none of it.
	quiet := runCLIIn(t, url+"\n", "profile", "set-webhook", "quiet", "--verify")
	if strings.Contains(quiet.stderr, "> POST") {
		t.Errorf("a run without --verbose logged requests:\n%s", quiet.stderr)
	}
}
