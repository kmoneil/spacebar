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
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

// TestCheckProfileName. A profile name is not only a key in this file: it
// becomes part of the credential reference keyring:<app>/<profile>/<secret>,
// which is split on its slashes, so a name with one in it produces a reference
// that parses into something else entirely.
func TestCheckProfileName(t *testing.T) {
	for _, name := range []string{"alerts", "a", "A1", "1st", "team-ops", "team_ops", "team.ops"} {
		if err := CheckProfileName(name); err != nil {
			t.Errorf("CheckProfileName(%q) = %v, want nil", name, err)
		}
	}

	for _, name := range []string{
		"",
		"with/slash",
		"with space",
		"with\nnewline",
		"with\x1b[2Jescape",
		"-leading",
		"_leading",
		".leading",
		"emoji\U0001F600",
	} {
		if err := CheckProfileName(name); err == nil {
			t.Errorf("CheckProfileName(%q) accepted it", name)
		}
	}

	if err := CheckProfileName(strings.Repeat("x", 64)); err != nil {
		t.Errorf("a 64 character name was refused: %v", err)
	}
	if err := CheckProfileName(strings.Repeat("x", 65)); err == nil {
		t.Error("a 65 character name was accepted")
	}
}

// TestABadProfileNameIsRefusedOnLoad, so that the rule holds for a file
// somebody hand-edited as well as for one this tool wrote.
func TestABadProfileNameIsRefusedOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	body := `{"profiles":{"has/slash":{"transport":"webhook"}}}`
	if err := os.WriteFile(path, []byte(body), FileMode); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if _, err := LoadFrom(path); err == nil {
		t.Error("a profile name with a slash in it loaded")
	}
}

// refFields are the JSON names on Profile whose values name a secret rather
// than being one, found by reflection rather than listed.
//
// Listing them here would put a second copy of validate's own list beside it,
// and two lists of the same thing is the drift every gate in this repository
// exists to prevent. Reflection means a field added tomorrow is covered the
// day it is added: the rule is "a key whose name ends in _ref", which is the
// naming convention the file already has, so the gate holds a field nobody has
// written yet.
func refFields(t testing.TB) []string {
	t.Helper()

	var found []string
	profile := reflect.TypeOf(Profile{})
	for i := range profile.NumField() {
		name, _, _ := strings.Cut(profile.Field(i).Tag.Get("json"), ",")
		if strings.HasSuffix(name, "_ref") {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		t.Fatal("no _ref field was found on Profile, so this gate would pass by having nothing to check")
	}
	return found
}

// refValue reads one _ref field off a profile by its JSON name.
func refValue(t testing.TB, p Profile, jsonName string) string {
	t.Helper()

	v := reflect.ValueOf(p)
	for i := range v.NumField() {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		if name == jsonName {
			return v.Field(i).String()
		}
	}
	t.Fatalf("no field is tagged %q", jsonName)
	return ""
}

// FuzzAConfigThatLoadsHoldsNoSecret states the rule
// TestASecretIsRefusedInTheConfigFile samples, over any file body rather than
// over the handful somebody wrote out.
//
// The rule is worth a property rather than a table because of how it is
// implemented. validate walks a hand-written list of two fields, and a third
// _ref field added to Profile without a line in that list would be a
// credential this tool writes to a file it tells people is safe to read, with
// nothing failing. refFields is what makes this catch that: the target does
// not carry a list of its own, it asks the struct, so a field added tomorrow
// is covered the day it is added rather than the day somebody remembers.
//
// The second claim is the round trip, and it belongs in the same target
// because the first one is only worth having if the file survives being
// rewritten. SaveTo runs the same validate, so a config that loads and then
// cannot be saved is a profile somebody can read and never change again.
func FuzzAConfigThatLoadsHoldsNoSecret(f *testing.F) {
	fields := refFields(f)

	for _, seed := range []string{
		`{}`,

		// Found by this target within two seconds of being written, and kept
		// as a seed as well as under testdata/fuzz so that the case reads in
		// the source. An empty profiles object is not the same Go value as an
		// absent one and is the same configuration. See sameProfiles.
		`{"profiles":{}}`,

		`{"profiles":{"work":{"transport":"webhook","webhook_url_ref":"keyring:spacebar/work/webhook-url"}}}`,
		`{"profiles":{"work":{"transport":"webhook","webhook_url_ref":"https://chat.googleapis.com/v1/spaces/A/messages?key=K&token=T"}}}`,
		`{"profiles":{"work":{"transport":"useroauth","client_secret_ref":"GOCSPX-plaintext"}}}`,
		`{"profiles":{"work":{"transport":"webhook","webhook_url":"https://leak.example"}}}`,
		`{"profiles":{"work":{"transport":"webhook","webhook_url_ref":"keyring:"}}}`,
		`{"profiles":{"work":{"transport":"webhook","webhook_url_ref":" keyring:x"}}}`,
		`{"profiles":{"work":{"transport":"webhook","webhook_url_ref":"KEYRING:x"}}}`,
		`{"default_profile":"missing"}`,
		`{"profiles":{"":{"transport":"webhook"}}}`,
		`{"profiles":{"../escape":{"transport":"webhook"}}}`,
		`{"profiles":{"work":{"transport":"nonsense"}}}`,

		// Found by this target, and the reason the secret is planted in a
		// known field rather than searched for anywhere in the body: a
		// refusal that names an invalid transport is doing its job.
		`{"profiles":{"0":{"trAnsport":"GOCSPX-plaintext"}}}`,

		// And the same nil-against-empty confusion one level down, found
		// after the map case was fixed and the slice case was not. See
		// document.
		`{"profiles":{"0":{"trAnsport":"useroauth","sCopes":[]}}}`,
		`{"profiles":{"work":{"transport":"webhook","aliases":{"deploys":"spaces/AAA"}}}}`,
		`{"profiles":{"work":{"transport":"useroauth","scopes":["https://www.googleapis.com/auth/chat.messages"]}}}`,
		`[]`, `null`, ``, `{`, "\xff",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		dir := t.TempDir()
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing the fixture: %v", err)
		}

		// A secret planted where one would be, with the fuzzed text around it,
		// so that the second claim can be exact about where it looked.
		//
		// The first draft asserted that no refusal ever quotes the sentinel
		// back, over the whole fuzzed body, and this target refuted it in
		// twenty-four seconds by putting the sentinel in `transport`, where
		// naming the bad value is the message doing its job. What must not be
		// quoted is the contents of a field that holds a credential, and that
		// is a claim about a known field rather than about a string.
		for _, field := range fields {
			refused := loadWithRef(t, field, secretSentinel+body)
			if refused == nil {
				t.Fatalf("%s = %q was accepted, and it is not a %s reference", field, secretSentinel+body, RefScheme)
			}
			if strings.Contains(refused.Error(), secretSentinel) {
				t.Fatalf("the refusal quoted the secret back: %v", refused)
			}
		}

		cfg, err := LoadFrom(path)
		if err != nil {
			// A refusal is the common outcome for an arbitrary body and is a
			// valid one. The claim below is about what loads.
			return
		}

		for name, profile := range cfg.Profiles {
			for _, field := range fields {
				value := refValue(t, profile, field)
				if value != "" && !strings.HasPrefix(value, RefScheme) {
					t.Fatalf("profile %q loaded with %s = something that is not a reference, "+
						"so a credential is sitting in a file this tool says is safe to read\n  body: %q",
						name, field, body)
				}
			}
		}

		// It loaded, so it has to be writable. Anything else is a file
		// somebody can read and never change.
		//
		// The snapshot is taken before the write and not after, which is the
		// difference between this holding and this being decoration. Written
		// after, it compares whatever SaveTo left behind against itself, so a
		// SaveTo that dropped a field from the value it was handed would pass:
		// that mutation was tried, and it did.
		wrote := document(t, cfg)

		out := filepath.Join(dir, "written.json")
		if err := cfg.SaveTo(out); err != nil {
			t.Fatalf("a configuration that loaded could not be saved: %v\n  body: %q", err, body)
		}
		again, err := LoadFrom(out)
		if err != nil {
			t.Fatalf("a configuration this tool wrote could not be read back: %v\n  body: %q", err, body)
		}
		if read := document(t, again); wrote != read {
			t.Fatalf("a round trip through the file changed the configuration\n  body: %q\n   was %s\n   now %s",
				body, wrote, read)
		}
	})
}

// document is the configuration as it would be written, which is the level the
// round-trip claim is really about.
//
// The first two drafts compared Go values and this target refuted both of them,
// which is worth the paragraph because the wrong conclusion was available and
// cheap each time. `{"profiles":{}}` loads with an empty map and `"scopes":[]`
// loads with an empty slice; `omitempty` writes neither, so both read back as
// nil, and reflect.DeepEqual says an empty container and an absent one differ.
// A configuration does not. Names answers the same for either, ScopedCapabilities
// grants nothing either way, and SaveClient already creates the map when it is
// nil, which is the only place the difference could have mattered.
//
// The alternative was to make the encoder write an empty object, which is
// changing a file format to satisfy an assertion. Comparing the encoded document
// states what the claim actually is: writing this out, reading it back, and
// writing it again produces the same file, so every later load sees what this
// one did. It also collapses empty and absent the way the format does rather
// than the way Go does, and it keeps holding when a field is added, because
// there is no list of fields in it.
func document(t *testing.T, c *Config) string {
	t.Helper()

	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatalf("encoding the configuration: %v", err)
	}
	return string(body)
}

// secretSentinel is long and shaped like nothing a message would say on its
// own, so that finding it in an error is the planted value and not a
// coincidence in the fuzzer's own text.
const secretSentinel = "sentinelPLAINTEXTSECRET0123456789abcdef"

// loadWithRef writes a configuration whose named reference field holds value,
// and returns what reading it back said.
func loadWithRef(t testing.TB, field, value string) error {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"profiles": map[string]any{
			"work": map[string]any{"transport": "webhook", field: value},
		},
	})
	if err != nil {
		t.Fatalf("building the fixture: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	_, err = LoadFrom(path)
	return err
}

// TestEveryRefFieldOnAProfileIsValidated is the other half, and it is a
// separate test because the fuzz target above cannot state it.
//
// That target checks the fields it was given. This checks that the list it was
// given is all of them, so adding a _ref field to Profile without teaching
// validate about it fails here rather than passing there for the reason that
// nobody mentioned it.
func TestEveryRefFieldOnAProfileIsValidated(t *testing.T) {
	checked := map[string]bool{"webhook_url_ref": true, "client_secret_ref": true}

	for _, field := range refFields(t) {
		if !checked[field] {
			t.Errorf("Profile has a %q field that nothing holds to the %q rule.\n"+
				"Add it to the refs list in Profile.validate, or a credential written there "+
				"reaches the file. FuzzAConfigThatLoadsHoldsNoSecret finds the field on its "+
				"own and will start failing too.",
				field, RefScheme)
			continue
		}

		// And the rule is really enforced for it, rather than only listed.
		body := `{"profiles":{"work":{"transport":"webhook","` + field + `":"a-plain-secret"}}}`
		if _, err := LoadFrom(write(t, body)); err == nil {
			t.Errorf("a plain value in %q was accepted", field)
		}
	}
}
