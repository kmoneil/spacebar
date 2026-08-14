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

package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
)

// realWebhook is the shape a Chat incoming webhook URL has: the messages
// endpoint for one space, with the credential in the query.
const realWebhook = "https://chat.googleapis.com/v1/spaces/AAAATestSpace/messages" +
	"?key=AIzaSyTestKeyNotARealOne0123456789&token=sQ7testTokenNotARealOne0123456789"

// memoryBackend stands in for the keyring. The real one would leave entries on
// whichever machine ran the test.
type memoryBackend map[string]string

func (m memoryBackend) get(service, key string) (string, error) {
	value, ok := m[service+"/"+key]
	if !ok {
		return "", errNotInFile
	}
	return value, nil
}

func (m memoryBackend) set(service, key, value string) error {
	m[service+"/"+key] = value
	return nil
}

func (m memoryBackend) remove(service, key string) error {
	if _, ok := m[service+"/"+key]; !ok {
		return errNotInFile
	}
	delete(m, service+"/"+key)
	return nil
}

func memoryStore() (*Store, memoryBackend) {
	keyring := memoryBackend{}
	return &Store{keyring: keyring, file: memoryBackend{}}, keyring
}

// TestSetWebhookPutsTheURLInTheKeyringAndAReferenceInTheFile is the whole point
// of the command group this exists for.
//
// The claim in SECURITY.md is that config.json holds a reference and never a
// secret. This is the moment that claim is most at risk, because it is the one
// place in Milestone 2 where a credential is handed to this tool, and the
// obvious implementation writes it straight into the profile it belongs to.
func TestSetWebhookPutsTheURLInTheKeyringAndAReferenceInTheFile(t *testing.T) {
	store, keyring := memoryStore()
	cfg := &config.Config{}

	if err := store.SetWebhook(cfg, "alerts", realWebhook); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}

	profile, ok := cfg.Profiles["alerts"]
	if !ok {
		t.Fatal("SetWebhook did not create the profile")
	}
	if profile.Transport != config.TransportWebhook {
		t.Errorf("transport = %q, want %q", profile.Transport, config.TransportWebhook)
	}
	if want := Ref("alerts", WebhookSecret); profile.WebhookURLRef != want {
		t.Errorf("webhook_url_ref = %q, want %q", profile.WebhookURLRef, want)
	}

	if got := keyring["spacebarish"]; got != "" {
		t.Errorf("unexpected keyring entry: %q", got)
	}
	stored, err := store.Get(profile.WebhookURLRef)
	if err != nil {
		t.Fatalf("reading the stored credential: %v", err)
	}
	if stored != realWebhook {
		t.Errorf("the stored credential is not the URL that was given")
	}
}

// TestTheConfigFileOnDiskNeverHoldsTheURL asserts the same rule against the
// bytes rather than against the struct.
//
// A field holding a reference is one thing; what somebody's dotfiles repository
// ends up with is another, and it is the second that matters. This writes the
// file, reads it back as text, and looks for the credential in it.
func TestTheConfigFileOnDiskNeverHoldsTheURL(t *testing.T) {
	store, _ := memoryStore()
	cfg := &config.Config{}

	if err := store.SetWebhook(cfg, "alerts", realWebhook); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	cfg.DefaultProfile = "alerts"

	path := filepath.Join(t.TempDir(), config.FileName)
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	for _, secret := range []string{realWebhook, "AIzaSyTestKeyNotARealOne0123456789", "sQ7testTokenNotARealOne0123456789"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("the configuration file holds a credential:\n%s", body)
		}
	}
	if !strings.Contains(string(body), config.RefScheme) {
		t.Errorf("the configuration file holds no reference at all:\n%s", body)
	}

	// And it loads again, which is the half that a reference-shaped string
	// nobody can resolve would still pass.
	reloaded, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("loading it back: %v", err)
	}
	if reloaded.Profiles["alerts"].WebhookURLRef != Ref("alerts", WebhookSecret) {
		t.Errorf("the reference did not survive the round trip")
	}
}

// TestSetWebhookReplacesRatherThanDuplicates. Rotating a webhook is the second
// thing anybody does with one, and the profile it belongs to keeps everything
// else it had.
func TestSetWebhookReplacesRatherThanDuplicates(t *testing.T) {
	store, _ := memoryStore()
	cfg := &config.Config{Profiles: map[string]config.Profile{
		"alerts": {Transport: config.TransportWebhook, Aliases: map[string]string{"ops": "spaces/AAAA"}},
	}}

	rotated := strings.Replace(realWebhook, "sQ7test", "NEWtest", 1)
	if err := store.SetWebhook(cfg, "alerts", rotated); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}

	if len(cfg.Profiles) != 1 {
		t.Errorf("there are now %d profiles, want 1", len(cfg.Profiles))
	}
	if got := cfg.Profiles["alerts"].Aliases["ops"]; got != "spaces/AAAA" {
		t.Errorf("rotating the webhook lost the profile's aliases")
	}

	stored, err := store.Get(Ref("alerts", WebhookSecret))
	if err != nil {
		t.Fatalf("reading the stored credential: %v", err)
	}
	if stored != rotated {
		t.Errorf("the old credential survived the rotation")
	}
}

// TestRemoveProfileTakesTheCredentialWithIt.
func TestRemoveProfileTakesTheCredentialWithIt(t *testing.T) {
	store, _ := memoryStore()
	cfg := &config.Config{}

	if err := store.SetWebhook(cfg, "alerts", realWebhook); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	cfg.DefaultProfile = "alerts"

	if err := store.RemoveProfile(cfg, "alerts"); err != nil {
		t.Fatalf("RemoveProfile: %v", err)
	}

	if _, ok := cfg.Profiles["alerts"]; ok {
		t.Error("the profile is still configured")
	}
	if _, err := store.Get(Ref("alerts", WebhookSecret)); err == nil {
		t.Error("the credential outlived the profile it belonged to")
	}

	// The default has to go with it. Left pointing at a profile that is not
	// there, it makes the file refuse to load, and every later command fails
	// until somebody hand-edits the file this command group exists to avoid
	// hand-editing.
	if cfg.DefaultProfile != "" {
		t.Errorf("default_profile still names %q", cfg.DefaultProfile)
	}
	if err := cfg.SaveTo(filepath.Join(t.TempDir(), config.FileName)); err != nil {
		t.Errorf("the configuration no longer validates after a removal: %v", err)
	}
}

// TestRemoveProfileDeletesACredentialNothingPointsAt.
//
// Somebody who interrupts a setup, or who edits the file by hand, ends up with
// a secret in their keyring that no profile refers to. A removal that only
// deleted what it could see in the configuration would leave it there for good,
// with no command that could reach it.
func TestRemoveProfileDeletesACredentialNothingPointsAt(t *testing.T) {
	store, _ := memoryStore()
	cfg := &config.Config{}

	if err := store.Set(Ref("orphan", WebhookSecret), realWebhook); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.RemoveProfile(cfg, "orphan"); err != nil {
		t.Fatalf("RemoveProfile: %v", err)
	}
	if _, err := store.Get(Ref("orphan", WebhookSecret)); err == nil {
		t.Error("the orphaned credential is still there")
	}
}

func TestCheckWebhookURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		ok   bool
		says string
	}{
		{"a real one", realWebhook, true, ""},
		{"padded", "  " + realWebhook + "\n", true, ""},
		{"empty", "", false, "no webhook URL"},
		{"blank", "   \n ", false, "no webhook URL"},

		// The one that matters most. A truncated paste is the common failure,
		// and it produces a URL that looks complete.
		{"cut before the query", "https://chat.googleapis.com/v1/spaces/AAAA/messages", false, "key"},
		{"cut mid-query", "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=abc", false, "token"},
		{"cut before the path", "https://chat.googleapis.com/v1?key=abc&token=def", false, "/spaces/"},

		{"plaintext", strings.Replace(realWebhook, "https", "http", 1), false, "https"},
		{"not a URL", "://nope", false, "not a URL"},
		{"no host", "https:///v1/spaces/AAAA/messages?key=a&token=b", false, "no host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckWebhookURL(tc.url)
			if tc.ok {
				if err != nil {
					t.Fatalf("CheckWebhookURL rejected a valid URL: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("CheckWebhookURL accepted it")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the failure does not mention %q:\n%v", tc.says, err)
			}
			if got := output.ExitCodeOf(err); got != output.ExitUsage {
				t.Errorf("exit code = %d, want %d", got, output.ExitUsage)
			}
		})
	}
}

// TestARejectedURLIsNotQuotedBack. The value being refused is a credential, and
// an error message is a place credentials go to be discovered: it reaches a
// terminal, a scroll buffer, a CI log, and whatever the caller pipes stderr
// into.
func TestARejectedURLIsNotQuotedBack(t *testing.T) {
	// Rejected for having no token, so the key is still in it to be leaked.
	partial := "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=AIzaSyTestKeyNotARealOne0123456789"

	err := CheckWebhookURL(partial)
	if err == nil {
		t.Fatal("a URL with no token was accepted")
	}
	if strings.Contains(err.Error(), "AIzaSyTestKeyNotARealOne0123456789") {
		t.Errorf("the refusal quotes the credential back:\n%v", err)
	}

	// The same for one that will not parse at all, where url.Parse's own error
	// quotes what it was given.
	err = CheckWebhookURL("https://chat.googleapis.com/v1/spaces/AAAA/messages?key=AIzaSyTestKeyNotARealOne0123456789\x7f")
	if err == nil {
		t.Fatal("a URL with a control character was accepted")
	}
	if strings.Contains(err.Error(), "AIzaSyTestKeyNotARealOne0123456789") {
		t.Errorf("the parse failure quotes the credential back:\n%v", err)
	}
}

// TestABadProfileNameIsRefused, because the name becomes part of the credential
// reference, which is split on its slashes.
func TestABadProfileNameIsRefused(t *testing.T) {
	store, _ := memoryStore()

	for _, name := range []string{"", "with/slash", "with space", "-leading", ".leading", "with\nnewline", strings.Repeat("x", 65)} {
		cfg := &config.Config{}
		if err := store.SetWebhook(cfg, name, realWebhook); err == nil {
			t.Errorf("profile name %q was accepted", name)
		}
		if len(cfg.Profiles) != 0 {
			t.Errorf("profile name %q created a profile anyway", name)
		}
	}

	for _, name := range []string{"alerts", "a", "A1", "team-ops", "team_ops", "team.ops", "1st"} {
		cfg := &config.Config{}
		if err := store.SetWebhook(cfg, name, realWebhook); err != nil {
			t.Errorf("profile name %q was refused: %v", name, err)
		}
	}
}
