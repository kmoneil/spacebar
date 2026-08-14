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
	"reflect"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
)

// consoleFile is what the Cloud console downloads for a desktop client, with
// the fields it actually writes.
const consoleFile = `{"installed":{
	"client_id":"1234567890-abcdefg.apps.googleusercontent.example",
	"project_id":"a-project",
	"auth_uri":"https://accounts.google.example/o/oauth2/auth",
	"token_uri":"https://oauth2.googleapis.example/token",
	"auth_provider_x509_cert_url":"https://www.googleapis.example/oauth2/v1/certs",
	"client_secret":"GOCSPX-notARealSecret",
	"redirect_uris":["http://localhost"]}}`

// TestParseClientReadsTwoFieldsAndIgnoresTheRest.
//
// The ignoring is the point rather than laziness. The file carries auth_uri and
// token_uri, and honouring them would let a doctored client file send the
// consent screen and the client secret somewhere else. The endpoints are
// constants in this repository, and this is the test that says so.
func TestParseClientReadsTwoFieldsAndIgnoresTheRest(t *testing.T) {
	client, err := ParseClient([]byte(consoleFile))
	if err != nil {
		t.Fatalf("ParseClient: %v", err)
	}

	if client.ID != "1234567890-abcdefg.apps.googleusercontent.example" {
		t.Errorf("client ID = %q", client.ID)
	}
	if client.Secret != "GOCSPX-notARealSecret" {
		t.Errorf("client secret was not read")
	}
	if client.Project != "a-project" {
		t.Errorf("project = %q", client.Project)
	}

	// The file's own endpoints go nowhere, so a caller cannot be redirected by
	// a file they were sent.
	doctored := strings.ReplaceAll(consoleFile, "accounts.google.example", "evil.invalid")
	doctored = strings.ReplaceAll(doctored, "oauth2.googleapis.example", "evil.invalid")

	client, err = ParseClient([]byte(doctored))
	if err != nil {
		t.Fatalf("ParseClient: %v", err)
	}

	// Every field, by reflection rather than by name. Naming them meant a new
	// field carrying the endpoint would slip past, which is exactly the change
	// this is guarding against: adding one is easy and looks harmless.
	value := reflect.ValueOf(*client)
	for i := range value.NumField() {
		field := value.Type().Field(i)
		if field.Type.Kind() != reflect.String {
			t.Errorf("Client.%s is not a string, and this check only reads strings", field.Name)
			continue
		}
		if strings.Contains(value.Field(i).String(), "evil.invalid") {
			t.Errorf("a doctored endpoint reached Client.%s: %q", field.Name, value.Field(i).String())
		}
	}
	if AuthEndpoint != "https://accounts.google.com/o/oauth2/v2/auth" {
		t.Errorf("the authorization endpoint is not a constant any more: %q", AuthEndpoint)
	}
}

// TestAWebClientIsNamedRatherThanRefusedGenerically.
//
// Picking the wrong application type in the console is an easy mistake and the
// resulting file parses perfectly well. What is wrong with it is that a web
// client cannot redirect to a loopback address on a port chosen at runtime,
// which is the whole of how this authorizes, and nothing in a generic parse
// error would say that.
func TestAWebClientIsNamedRatherThanRefusedGenerically(t *testing.T) {
	web := strings.Replace(consoleFile, `"installed"`, `"web"`, 1)

	_, err := ParseClient([]byte(web))
	if err == nil {
		t.Fatal("a web client was accepted")
	}
	for _, want := range []string{"web application client", "Desktop app", "127.0.0.1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal is missing %q:\n%v", want, err)
		}
	}
}

func TestParseClientRefusesWhatIsNotOne(t *testing.T) {
	for _, tc := range []struct{ name, body, says string }{
		{"not JSON", `{`, "not the JSON"},
		{"neither section", `{"something":{}}`, "neither"},
		{"no client id", `{"installed":{"client_secret":"x"}}`, "no client_id"},
		{"no secret", `{"installed":{"client_id":"x"}}`, "truncated"},
		{"nothing", ``, "not the JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClient([]byte(tc.body))
			if err == nil {
				t.Fatalf("accepted, and produced %+v", got)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q:\n%v", tc.says, err)
			}
			if code := output.ExitCodeOf(err); code != output.ExitUsage {
				t.Errorf("exit %d, want %d", code, output.ExitUsage)
			}
		})
	}
}

// TestSaveClientPutsTheSecretInTheKeyringAndTheIdentifierInTheFile.
//
// The split looks inconsistent and is SPEC.md §5.3 applied exactly. A client ID
// is in the browser's address bar during consent, so anybody who has authorized
// has seen it. A secret is not, and while RFC 8252 is right that a native-app
// secret is not confidential, somebody who made a client in their own Cloud
// project did not agree to keep it in a file they might paste into an issue.
func TestSaveClientPutsTheSecretInTheKeyringAndTheIdentifierInTheFile(t *testing.T) {
	store, _ := memoryStore()
	cfg := &config.Config{}

	client, err := ParseClient([]byte(consoleFile))
	if err != nil {
		t.Fatalf("ParseClient: %v", err)
	}
	if err := store.SaveClient(cfg, "work", client); err != nil {
		t.Fatalf("SaveClient: %v", err)
	}

	profile := cfg.Profiles["work"]
	if profile.ClientID != client.ID {
		t.Errorf("client_id = %q", profile.ClientID)
	}
	if profile.ClientSecretRef != Ref("work", ClientSecretRef) {
		t.Errorf("client_secret_ref = %q", profile.ClientSecretRef)
	}
	if profile.Transport != config.TransportUserOAuth {
		t.Errorf("transport = %q, and a client is only for user OAuth", profile.Transport)
	}

	// And the bytes on disk carry no secret, which is the claim that matters.
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if strings.Contains(string(body), client.Secret) {
		t.Errorf("the configuration file holds the client secret:\n%s", body)
	}

	// The secret resolves through the reference, which is the other half.
	id, secret, err := store.ClientCredentials(profile)
	if err != nil {
		t.Fatalf("ClientCredentials: %v", err)
	}
	if id != client.ID || secret != client.Secret {
		t.Errorf("the client did not resolve back: %q / %q", id, secret)
	}
}

// TestSaveClientKeepsATransportThatIsAlreadySet, so that adding a client to a
// webhook profile does not silently convert it.
func TestSaveClientKeepsATransportThatIsAlreadySet(t *testing.T) {
	store, _ := memoryStore()
	cfg := &config.Config{Profiles: map[string]config.Profile{
		"alerts": {Transport: config.TransportWebhook, WebhookURLRef: Ref("alerts", WebhookSecret)},
	}}

	client, err := ParseClient([]byte(consoleFile))
	if err != nil {
		t.Fatalf("ParseClient: %v", err)
	}
	if err := store.SaveClient(cfg, "alerts", client); err != nil {
		t.Fatalf("SaveClient: %v", err)
	}

	if got := cfg.Profiles["alerts"].Transport; got != config.TransportWebhook {
		t.Errorf("transport became %q, and a webhook profile is not converted by gaining a client", got)
	}
	if cfg.Profiles["alerts"].WebhookURLRef == "" {
		t.Error("the webhook reference was lost")
	}
}

// TestInspectReportsWhatIsAlreadyThereWithoutChangingIt.
func TestInspectReportsWhatIsAlreadyThereWithoutChangingIt(t *testing.T) {
	store, _ := memoryStore()

	state, err := store.Inspect("work", config.Profile{})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.HasClient {
		t.Errorf("a profile with nothing configured reported a client: %+v", state)
	}

	state, err = store.Inspect("work", config.Profile{ClientID: "abc.apps.googleusercontent.example"})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !state.HasClient || state.ClientID != "abc.apps.googleusercontent.example" {
		t.Errorf("state = %+v", state)
	}
	// It came from the configuration, not from the binary, which is the
	// difference between a build from source and an official release.
	if state.FromBuild {
		t.Error("a client from the configuration was reported as coming from the build")
	}
}

// TestSendOnlyIsExactlyOneScope, per SPEC.md §6.4. A narrower scope materially
// improves the odds of an administrator approving the application, and a mode
// that quietly asked for more would defeat the point of having it.
func TestSendOnlyIsExactlyOneScope(t *testing.T) {
	if len(SendOnlyScopes) != 1 || SendOnlyScopes[0] != ScopeSendOnly {
		t.Errorf("SendOnlyScopes = %v", SendOnlyScopes)
	}
	if !strings.HasSuffix(ScopeSendOnly, "/chat.messages.create") {
		t.Errorf("the send-only scope is %q", ScopeSendOnly)
	}

	// The default set is narrow too, and deliberately excludes chat.spaces,
	// which permits creating spaces and looking up direct messages. Neither is
	// something this tool does yet, and a scope requested before it is needed
	// is a scope an administrator has to approve for no reason.
	for _, scope := range DefaultScopes {
		if scope == ScopeSpaces {
			t.Errorf("the default set asks for %q, which nothing uses yet", scope)
		}
	}
	if len(DefaultScopes) != 2 {
		t.Errorf("DefaultScopes = %v", DefaultScopes)
	}
}
