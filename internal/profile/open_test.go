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
			name: "a transport this build does not have yet",
			file: `{"default_profile":"work","profiles":{"work":{"transport":"useroauth"}}}`,
			says: "Milestone 3",
			exit: output.ExitUnsupported,
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
