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

package profile

import (
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
	"github.com/kmoneil/spacebar/internal/transport"
)

const testWebhook = "https://chat.googleapis.com/v1/spaces/AAAATestSpace/messages" +
	"?key=AIzaSyTestKeyNotARealOne0123456789&token=sQ7testTokenNotARealOne0123456789"

// isolate gives one test its own configuration directory and its own keyring,
// so that nothing here touches the machine it runs on.
func isolate(t *testing.T, file string) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	keyring.MockInit()

	if file == "" {
		return
	}
	path := filepath.Join(dir, meta.AppName, config.FileName)
	if err := os.MkdirAll(filepath.Dir(path), config.DirMode); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(file), config.FileMode); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
}

// TestAWebhookProfileOpensAWebhookTransport, which is the whole of Milestone 2.
func TestAWebhookProfileOpensAWebhookTransport(t *testing.T) {
	isolate(t, `{"default_profile":"alerts","profiles":{"alerts":{"transport":"webhook","webhook_url_ref":"keyring:spacebar/alerts/webhook"}}}`)

	store, err := auth.New()
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	if err := store.Set(auth.Ref("alerts", auth.WebhookSecret), testWebhook); err != nil {
		t.Fatalf("storing the credential: %v", err)
	}

	opened, err := For(Options{})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if opened.Name != "alerts" {
		t.Errorf("name = %q", opened.Name)
	}
	if opened.Transport.Kind() != config.TransportWebhook {
		t.Errorf("kind = %q", opened.Transport.Kind())
	}

	// It reaches one space, which is what lets `send` take one argument.
	space, fixed := transport.SpaceOf(opened.Transport)
	if !fixed || space != "spaces/AAAATestSpace" {
		t.Errorf("SpaceOf = %q, %v", space, fixed)
	}
}

// TestAUserOAuthProfileOpensATransportThatCanRead, which is the whole of m3-04.
//
// The assertion that matters is the capability, not the type. A profile with a
// stored token and a stored client has to come back able to read, because that
// is the thing this milestone exists to make possible and the thing every read
// command checks before it does anything.
func TestAUserOAuthProfileOpensATransportThatCanRead(t *testing.T) {
	isolate(t, `{"default_profile":"work","profiles":{"work":{"transport":"useroauth","client_id":"test.apps.googleusercontent.com"}}}`)

	store, err := auth.New()
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	if err := store.SaveToken("work", &auth.Token{
		AccessToken:  "test-access",
		RefreshToken: "test-refresh",
		TokenType:    "Bearer",
		Scopes:       auth.DefaultScopes,

		// Far enough out that nothing here tries to refresh, which would be a
		// network call. Opening a transport must not make one.
		Expiry:     time.Now().Add(time.Hour),
		ObtainedAt: time.Now(),
	}); err != nil {
		t.Fatalf("storing the token: %v", err)
	}

	opened, err := For(Options{})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if opened.Transport.Kind() != config.TransportUserOAuth {
		t.Errorf("kind = %q", opened.Transport.Kind())
	}
	if !opened.Transport.Capabilities().Has(transport.CanRead) {
		t.Error("a user-OAuth profile opened without the ability to read, which is the point of the transport")
	}

	// It reaches every space the account can, so there is no fixed one and
	// `send` needs a target. This is the half of the webhook's behaviour that
	// must not be inherited.
	if space, fixed := transport.SpaceOf(opened.Transport); fixed {
		t.Errorf("SpaceOf = %q, %v, but this transport is not fixed to one space", space, fixed)
	}
}

// TestOpeningAUserOAuthProfileWithNoClientSaysHowToGetOne.
//
// A build from source has no linked OAuth client on purpose, so this is the
// ordinary state for anybody who cloned the repository rather than an edge case.
// The failure has to name the command that fixes it, because the alternative is
// somebody concluding the tool is broken.
func TestOpeningAUserOAuthProfileWithNoClientSaysHowToGetOne(t *testing.T) {
	isolate(t, `{"default_profile":"work","profiles":{"work":{"transport":"useroauth"}}}`)

	store, err := auth.New()
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	if err := store.SaveToken("work", &auth.Token{
		AccessToken: "test-access",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Hour),
		ObtainedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("storing the token: %v", err)
	}

	_, err = For(Options{})
	if err == nil {
		t.Fatal("a profile with no OAuth client opened anyway")
	}
	if got := output.ExitCodeOf(err); got != output.ExitAuthRequired {
		t.Errorf("exit code = %d, want %d: %v", got, output.ExitAuthRequired, err)
	}
	if !strings.Contains(err.Error(), meta.AppName+" auth setup --profile work") {
		t.Errorf("the failure does not name the command that stores a client:\n%v", err)
	}
}

// TestEveryFailurePathStillReturnsAnOpen.
//
// Callers read the warnings off the result before deciding what to do with the
// error, because a credential that came off a disk in plain text is worth
// saying whether or not the command then worked. Returning nil on some paths
// and not on others makes that correct code panic, which is exactly what it did
// before a test went looking.
func TestEveryFailurePathStillReturnsAnOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		says string
		exit output.ExitCode
	}{
		{
			name: "no configuration at all",
			file: "",
			says: "no profile",
			exit: output.ExitUsage,
		},
		{
			name: "a profile that is not there",
			file: `{"default_profile":"alerts","profiles":{"alerts":{"transport":"webhook","webhook_url_ref":"keyring:spacebar/alerts/webhook"}}}`,
			says: "no credential is stored",
			exit: output.ExitAuthRequired,
		},
		{
			name: "a webhook with no URL",
			file: `{"default_profile":"alerts","profiles":{"alerts":{"transport":"webhook"}}}`,
			says: meta.AppName + " profile set-webhook alerts",
			exit: output.ExitAuthRequired,
		},
		{
			// Until m3-04 this case asserted a stub that said "Milestone 3 adds
			// it". The transport exists now, so the failure moved: a useroauth
			// profile that was never authorized has no token, which is exit 4
			// and a different fix. Kept rather than deleted because the path is
			// still a failure path and still has to return a usable Open.
			name: "a useroauth profile nobody has authorized",
			file: `{"default_profile":"work","profiles":{"work":{"transport":"useroauth"}}}`,
			says: "no credential is stored",
			exit: output.ExitAuthRequired,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t, tc.file)

			opened, err := For(Options{})
			if opened == nil {
				t.Fatal("For returned a nil Open alongside an error, and every caller dereferences it")
			}
			if err == nil {
				t.Fatal("this was expected to fail")
			}
			if got := output.ExitCodeOf(err); got != tc.exit {
				t.Errorf("exit code = %d, want %d: %v", got, tc.exit, err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the failure does not mention %q:\n%v", tc.says, err)
			}
		})
	}
}

// TestAProfileCanBeChosenByName, which is what --profile does and what a script
// sending through several needs.
func TestAProfileCanBeChosenByName(t *testing.T) {
	isolate(t, `{"default_profile":"alerts","profiles":{
		"alerts":{"transport":"webhook","webhook_url_ref":"keyring:spacebar/alerts/webhook"},
		"releases":{"transport":"webhook","webhook_url_ref":"keyring:spacebar/releases/webhook"}}}`)

	store, err := auth.New()
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	other := strings.Replace(testWebhook, "AAAATestSpace", "BBBBReleases", 1)
	for name, url := range map[string]string{"alerts": testWebhook, "releases": other} {
		if err := store.Set(auth.Ref(name, auth.WebhookSecret), url); err != nil {
			t.Fatalf("storing %s: %v", name, err)
		}
	}

	opened, err := For(Options{Name: "releases"})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if opened.Name != "releases" {
		t.Errorf("name = %q, want releases", opened.Name)
	}
	if space, _ := transport.SpaceOf(opened.Transport); space != "spaces/BBBBReleases" {
		t.Errorf("space = %q, so --profile chose the wrong one", space)
	}
}
