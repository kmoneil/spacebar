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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// write puts body at path inside a fresh temporary directory and returns the
// path. The directory is deliberately created at 0755, the mode a normal umask
// would give it, so that a test asserting SaveTo tightens it is asserting
// something.
func write(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

func wantExit(t *testing.T, err error, code output.ExitCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a failure with exit %d, got none", code)
	}
	if got := output.ExitCodeOf(err); got != code {
		t.Fatalf("exit %d, want %d, for: %v", got, code, err)
	}
}

const twoProfiles = `{
  "default_profile": "alerts",
  "profiles": {
    "alerts": {
      "transport": "webhook",
      "webhook_url_ref": "keyring:spacebar/alerts/webhook"
    },
    "work": {
      "transport": "useroauth",
      "client_id": "1234.apps.googleusercontent.com",
      "scopes": ["https://www.googleapis.com/auth/chat.messages"],
      "aliases": {"eng": "spaces/AAAA1111"}
    }
  }
}
`

func TestLoadReadsAProfile(t *testing.T) {
	cfg, err := LoadFrom(write(t, twoProfiles))
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	if got := cfg.DefaultProfile; got != "alerts" {
		t.Errorf("default_profile is %q, want alerts", got)
	}
	if got := strings.Join(cfg.Names(), ","); got != "alerts,work" {
		t.Errorf("profiles are %q, want alerts,work", got)
	}
	if got := cfg.Profiles["work"].Aliases["eng"]; got != "spaces/AAAA1111" {
		t.Errorf("alias eng is %q, want spaces/AAAA1111", got)
	}
	if got := cfg.Profiles["alerts"].Transport; got != TransportWebhook {
		t.Errorf("alerts transport is %q, want %q", got, TransportWebhook)
	}
}

// TestMissingFileIsNotAnError holds the rule that a command needing no profile
// works on a machine nobody has set up. `spacebar version` on a fresh install
// must not fail because there is no config file yet.
func TestMissingFileIsNotAnError(t *testing.T) {
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing file should load as empty: %v", err)
	}
	if len(cfg.Profiles) != 0 {
		t.Errorf("got %d profiles from a file that does not exist", len(cfg.Profiles))
	}
}

// TestSaveWritesTightModes is the claim from the card: a file this tool writes
// is unreadable by anyone else, and so is the directory it is in.
func TestSaveWritesTightModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "spacebar")
	path := filepath.Join(dir, FileName)

	cfg := &Config{
		DefaultProfile: "alerts",
		Profiles: map[string]Profile{
			"alerts": {Transport: TransportWebhook, WebhookURLRef: RefScheme + "spacebar/alerts/webhook"},
		},
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("saving: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != FileMode {
		t.Errorf("the config file is mode %#o, want %#o", got, FileMode)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != DirMode {
		t.Errorf("the config directory is mode %#o, want %#o", got, DirMode)
	}
}

// TestSaveLeavesNoTemporaryFile guards the atomic write. A rename that failed
// halfway, or a temporary file nobody removed, leaves a .config-*.json sitting
// beside the real one where the next reader will wonder what it is.
func TestSaveLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	cfg := &Config{Profiles: map[string]Profile{"alerts": {Transport: TransportWebhook}}}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("saving: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	for _, e := range entries {
		if e.Name() != FileName {
			t.Errorf("%s was left behind by the save", e.Name())
		}
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	before := &Config{
		DefaultProfile: "work",
		Profiles: map[string]Profile{
			"work": {
				Transport:       TransportUserOAuth,
				ClientID:        "1234.apps.googleusercontent.com",
				ClientSecretRef: RefScheme + "spacebar/work/client-secret",
				Scopes:          []string{"https://www.googleapis.com/auth/chat.messages"},
				Aliases:         map[string]string{"eng": "spaces/AAAA1111"},
			},
		},
	}
	if err := before.SaveTo(path); err != nil {
		t.Fatalf("saving: %v", err)
	}

	after, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("loading what we just wrote: %v", err)
	}
	if after.DefaultProfile != before.DefaultProfile {
		t.Errorf("default_profile did not survive: %q", after.DefaultProfile)
	}

	got, want := after.Profiles["work"], before.Profiles["work"]
	if got.ClientID != want.ClientID || got.ClientSecretRef != want.ClientSecretRef {
		t.Errorf("the OAuth fields did not survive: %+v", got)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != want.Scopes[0] {
		t.Errorf("scopes did not survive: %v", got.Scopes)
	}
	if got.Aliases["eng"] != want.Aliases["eng"] {
		t.Errorf("aliases did not survive: %v", got.Aliases)
	}
}

// TestASecretIsRefusedInTheConfigFile is the invariant, from both ends.
//
// The webhook URL is the case that matters. It carries key and token query
// parameters that are the whole of the authentication for that space, so it is
// a bearer credential, and it is the one credential somebody will paste into a
// config file without hesitating, because it looks like a URL.
func TestASecretIsRefusedInTheConfigFile(t *testing.T) {
	const rawURL = "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?key=AIzaSyFAKE&token=FAKETOKEN"

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "a webhook URL where a reference belongs",
			body: `{"profiles":{"alerts":{"transport":"webhook","webhook_url_ref":"` + rawURL + `"}}}`,
			want: "does not belong in a file meant to be read",
		},
		{
			name: "a client secret where a reference belongs",
			body: `{"profiles":{"work":{"transport":"useroauth","client_secret_ref":"GOCSPX-notARealSecret"}}}`,
			want: "is not a reference",
		},
		{
			name: "the field name from the spec, which holds a value rather than a reference",
			body: `{"profiles":{"work":{"transport":"useroauth","client_secret":"GOCSPX-notARealSecret"}}}`,
			want: "not a field this tool knows",
		},
		{
			name: "a webhook URL under a field nobody reads",
			body: `{"profiles":{"alerts":{"transport":"webhook","webhook_url":"` + rawURL + `"}}}`,
			want: "not a field this tool knows",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFrom(write(t, tc.body))
			wantExit(t, err, output.ExitUsage)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the message does not say %q:\n%v", tc.want, err)
			}
			// Whatever else the message says, it must not repeat the secret
			// back: a message is written to a terminal, a scroll buffer, and
			// whatever the caller redirected stderr into.
			if strings.Contains(err.Error(), "FAKETOKEN") || strings.Contains(err.Error(), "notARealSecret") {
				t.Errorf("the failure quoted the secret back:\n%v", err)
			}
		})
	}
}

// TestSaveRefusesToWriteASecret closes the other half. Refusing to read a bad
// file is worth little if the tool will write one.
func TestSaveRefusesToWriteASecret(t *testing.T) {
	cfg := &Config{Profiles: map[string]Profile{
		"alerts": {
			Transport:     TransportWebhook,
			WebhookURLRef: "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?key=k&token=t",
		},
	}}

	path := filepath.Join(t.TempDir(), FileName)
	err := cfg.SaveTo(path)
	wantExit(t, err, output.ExitUsage)

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("the file was written anyway")
	}
}

func TestBadFilesFailUsefully(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			// One closing brace short, which is how a hand-edited file
			// usually breaks. encoding/json reports this as a bare
			// io.ErrUnexpectedEOF with no position in it at all.
			name: "a truncated file says where it ran out",
			body: "{\n  \"default_profile\": \"alerts\",\n  \"profiles\": {\n}\n",
			want: []string{":5:", "ends before the JSON does"},
		},
		{
			name: "a syntax error names the line",
			body: "{\n  \"profiles\": {\n    \"alerts\" {}\n  }\n}\n",
			want: []string{":3:", "not valid JSON"},
		},
		{
			name: "the wrong type names the line and the field",
			body: "{\n  \"profiles\": {\n    \"alerts\": {\n      \"transport\": 7\n    }\n  }\n}\n",
			want: []string{":4:", "transport"},
		},
		{
			name: "an unknown transport names the ones that exist",
			body: `{"profiles":{"alerts":{"transport":"carrier-pigeon"}}}`,
			want: []string{"carrier-pigeon", "webhook", "useroauth"},
		},
		{
			name: "a missing transport names the ones that exist",
			body: `{"profiles":{"alerts":{}}}`,
			want: []string{"has no transport", "webhook", "useroauth"},
		},
		{
			name: "a default naming no profile says which profiles there are",
			body: `{"default_profile":"nope","profiles":{"alerts":{"transport":"webhook"}}}`,
			want: []string{"nope", "alerts"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := write(t, tc.body)
			_, err := LoadFrom(path)
			wantExit(t, err, output.ExitUsage)

			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message does not contain %q:\n%v", want, err)
				}
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the message does not name the file:\n%v", err)
			}
			// The Go type names in an encoding/json error mean nothing to
			// somebody looking at a JSON file, and they are what this branch
			// exists to keep out of the message.
			if strings.Contains(err.Error(), "json: cannot unmarshal") {
				t.Errorf("the raw encoding/json message reached the user:\n%v", err)
			}
		})
	}
}

// TestLoadAndSaveFindTheirOwnFile covers the two functions the rest of the tool
// actually calls. Everything else here goes through LoadFrom and SaveTo, which
// take a path and so cannot be wrong about where the file lives.
func TestLoadAndSaveFindTheirOwnFile(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	want := filepath.Join(base, meta.AppName, FileName)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("loading from an empty home: %v", err)
	}
	if got := cfg.Source(); got != want {
		t.Errorf("a missing file reports its path as %q, want %q", got, want)
	}

	cfg.DefaultProfile = "alerts"
	cfg.Profiles = map[string]Profile{"alerts": {Transport: TransportWebhook}}
	if err := cfg.Save(); err != nil {
		t.Fatalf("saving: %v", err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("nothing was written to %s: %v", want, err)
	}

	again, err := Load()
	if err != nil {
		t.Fatalf("loading what Save wrote: %v", err)
	}
	if again.DefaultProfile != "alerts" {
		t.Errorf("read back default_profile %q, want alerts", again.DefaultProfile)
	}
}

// TestSaveReportsAnUnwritableDirectory keeps the failure a usage error with a
// path in it, rather than a bare syscall message.
func TestSaveReportsAnUnwritableDirectory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the blocker: %v", err)
	}

	cfg := &Config{Profiles: map[string]Profile{"alerts": {Transport: TransportWebhook}}}
	err := cfg.SaveTo(filepath.Join(blocker, "spacebar", FileName))
	wantExit(t, err, output.ExitUsage)

	if !strings.Contains(err.Error(), blocker) {
		t.Errorf("the message does not name the path it could not create:\n%v", err)
	}
}

func TestLineCol(t *testing.T) {
	data := []byte("one\ntwo\nthree")
	cases := []struct {
		offset    int64
		line, col int
	}{
		{0, 1, 1},
		{3, 1, 4},
		{4, 2, 1},
		{8, 3, 1},
		{int64(len(data)), 3, 6},
		{-1, 1, 1},
		{9999, 3, 6},
	}
	for _, tc := range cases {
		line, col := lineCol(data, tc.offset)
		if line != tc.line || col != tc.col {
			t.Errorf("offset %d is %d:%d, want %d:%d", tc.offset, line, col, tc.line, tc.col)
		}
	}
}
