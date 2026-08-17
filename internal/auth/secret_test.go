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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// The webhook URL shape, which is what M2 actually stores. Kept whole so that
// a test asserting it never leaks is asserting against the real thing.
const testWebhook = "https://chat.googleapis.com/v1/spaces/AAAA1111/messages?key=AIzaSyFAKEKEY&token=FAKETOKEN"

// fakeKeyring is an in-memory keyring. The real one is never touched by a
// test: an entry written during a test run stays on the machine that ran it,
// and on a developer's laptop that is somebody's actual keychain.
type fakeKeyring struct {
	entries map[string]string
	err     error // when set, the keyring is unavailable and every call fails.
}

func newFakeKeyring() *fakeKeyring {
	return &fakeKeyring{entries: map[string]string{}}
}

func (f *fakeKeyring) get(service, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.entries[service+"/"+key]
	if !ok {
		return "", errors.New("secret not found in keyring")
	}
	return v, nil
}

func (f *fakeKeyring) set(service, key, value string) error {
	if f.err != nil {
		return f.err
	}
	f.entries[service+"/"+key] = value
	return nil
}

func (f *fakeKeyring) remove(service, key string) error {
	if f.err != nil {
		return f.err
	}
	name := service + "/" + key
	if _, ok := f.entries[name]; !ok {
		return errors.New("secret not found in keyring")
	}
	delete(f.entries, name)
	return nil
}

// newTestStore builds a Store over a fake keyring and a real fallback file in a
// temporary directory, so the file half is exercised as written.
func newTestStore(t *testing.T) (*Store, *fakeKeyring, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), CredentialsFile)
	kr := newFakeKeyring()
	return &Store{keyring: kr, file: &fileStore{path: path}}, kr, path
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

func TestRefIsBuiltFromTheProductName(t *testing.T) {
	got := Ref("alerts", "webhook")
	if want := config.RefScheme + meta.AppName + "/alerts/webhook"; got != want {
		t.Errorf("Ref is %q, want %q", got, want)
	}
	if _, _, err := parseRef(got); err != nil {
		t.Errorf("a reference this package built does not parse: %v", err)
	}
}

func TestParseRefRejectsWhatIsNotAReference(t *testing.T) {
	for _, ref := range []string{
		"",
		testWebhook,
		"spacebar/alerts/webhook",
		"keyring:",
		"keyring:onlyservice",
		"keyring:/nokey",
	} {
		if _, _, err := parseRef(ref); err == nil {
			t.Errorf("%q parsed as a reference", ref)
		}
	}
}

// TestKeyringIsPreferredAndSilent is the ordinary case: a machine with a
// keyring stores the secret there and says nothing about it.
func TestKeyringIsPreferredAndSilent(t *testing.T) {
	store, kr, path := newTestStore(t)
	ref := Ref("alerts", "webhook")

	if err := store.Set(ref, testWebhook); err != nil {
		t.Fatalf("storing: %v", err)
	}
	got, err := store.Get(ref)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got != testWebhook {
		t.Errorf("read back %q", RedactURL(got))
	}

	if len(store.Warnings()) != 0 {
		t.Errorf("a working keyring produced warnings: %v", store.Warnings())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the fallback file was written even though the keyring worked")
	}
	if len(kr.entries) != 1 {
		t.Errorf("the keyring holds %d entries, want 1", len(kr.entries))
	}
}

// TestFallbackWarnsAndRoundTrips is the machine this tool will usually run on:
// a container or a CI runner with no keyring at all.
func TestFallbackWarnsAndRoundTrips(t *testing.T) {
	store, kr, path := newTestStore(t)
	kr.err = errors.New(`exec: "dbus-launch": executable file not found in $PATH`)
	ref := Ref("alerts", "webhook")

	if err := store.Set(ref, testWebhook); err != nil {
		t.Fatalf("storing: %v", err)
	}
	got, err := store.Get(ref)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got != testWebhook {
		t.Errorf("read back %q", RedactURL(got))
	}

	warnings := strings.Join(store.Warnings(), "\n")
	for _, want := range []string{path, "plain text", "dbus-launch"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("the warnings do not mention %q:\n%s", want, warnings)
		}
	}

	// The file is the whole of the exposure, so its mode is the whole of the
	// mitigation.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != config.FileMode {
		t.Errorf("the fallback file is mode %#o, want %#o", got, config.FileMode)
	}
}

// TestWarningsAreDeduplicated keeps a command that resolves the same credential
// twice from printing the same paragraph twice.
func TestWarningsAreDeduplicated(t *testing.T) {
	store, kr, _ := newTestStore(t)
	kr.err = errors.New("no keyring here")
	ref := Ref("alerts", "webhook")

	if err := store.Set(ref, testWebhook); err != nil {
		t.Fatalf("storing: %v", err)
	}
	for range 3 {
		if _, err := store.Get(ref); err != nil {
			t.Fatalf("reading: %v", err)
		}
	}

	// One for the write and one for the reads, and no more however many times
	// the credential is resolved.
	if got := len(store.Warnings()); got > 2 {
		t.Errorf("got %d warnings for one write and three reads:\n%s", got, strings.Join(store.Warnings(), "\n"))
	}
}

// TestSecretWrittenBeforeAKeyringExistedIsStillFound is the migration case. A
// container gains a session bus, or somebody installs a keyring, and the
// secrets already on disk have to keep working.
func TestSecretWrittenBeforeAKeyringExistedIsStillFound(t *testing.T) {
	store, kr, _ := newTestStore(t)
	ref := Ref("alerts", "webhook")

	kr.err = errors.New("no keyring here")
	if err := store.Set(ref, testWebhook); err != nil {
		t.Fatalf("storing without a keyring: %v", err)
	}

	kr.err = nil // the keyring arrives, and does not have the entry.
	got, err := store.Get(ref)
	if err != nil {
		t.Fatalf("reading after the keyring arrived: %v", err)
	}
	if got != testWebhook {
		t.Errorf("read back %q", RedactURL(got))
	}
}

// TestAMissingCredentialIsExitFour is the code a script branches on. The
// profile is configured and the secret behind it is not there, which needs to
// be distinguishable from a space that does not exist.
func TestAMissingCredentialIsExitFour(t *testing.T) {
	store, _, _ := newTestStore(t)

	_, err := store.Get(Ref("alerts", "webhook"))
	wantExit(t, err, output.ExitAuthRequired)
	if !strings.Contains(err.Error(), Ref("alerts", "webhook")) {
		t.Errorf("the message does not name the reference:\n%v", err)
	}
}

func TestDeleteRemovesFromBothPlaces(t *testing.T) {
	store, kr, path := newTestStore(t)
	ref := Ref("alerts", "webhook")

	// The same credential in both places, which is what happens when a machine
	// gains a keyring after the fallback was written.
	kr.err = errors.New("no keyring here")
	if err := store.Set(ref, testWebhook); err != nil {
		t.Fatalf("storing in the file: %v", err)
	}
	kr.err = nil
	if err := store.Set(ref, testWebhook); err != nil {
		t.Fatalf("storing in the keyring: %v", err)
	}

	if err := store.Delete(ref); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if len(kr.entries) != 0 {
		t.Error("the keyring still holds the credential")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the fallback file: %v", err)
	}
	if strings.Contains(string(body), "FAKETOKEN") {
		t.Error("the fallback file still holds the credential after a delete")
	}
}

// TestNewPutsTheFallbackBesideTheConfig covers the constructor the rest of the
// tool calls. The three osKeyring methods it wires up are deliberately left
// uncovered: they are one-line passthroughs, and a test that exercised them
// would write to the keyring of whatever machine ran it.
func TestNewPutsTheFallbackBesideTheConfig(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	store, err := New()
	if err != nil {
		t.Fatalf("building a store: %v", err)
	}

	file, ok := store.file.(*fileStore)
	if !ok {
		t.Fatalf("the fallback is %T, not a file", store.file)
	}
	if want := filepath.Join(base, meta.AppName, CredentialsFile); file.path != want {
		t.Errorf("the fallback file is %q, want %q", file.path, want)
	}
}

func TestEveryOperationRejectsABadReference(t *testing.T) {
	store, _, _ := newTestStore(t)

	if _, err := store.Get(testWebhook); err == nil {
		t.Error("Get accepted a webhook URL as a reference")
	}
	if err := store.Set(testWebhook, "x"); err == nil {
		t.Error("Set accepted a webhook URL as a reference")
	}
	if err := store.Delete(testWebhook); err == nil {
		t.Error("Delete accepted a webhook URL as a reference")
	}
}

// TestASecretIsNeverInAMessage is the rule that makes the rest of this
// package's error handling worth anything.
func TestASecretIsNeverInAMessage(t *testing.T) {
	store, kr, _ := newTestStore(t)

	// A keyring whose failure quotes back what it was handed, which is the
	// shape the darwin backend is one upstream change away from having: it
	// passes the secret to /usr/bin/security base64-encoded on stdin.
	kr.err = errors.New("keychain refused: add-generic-password -w " +
		"aHR0cHM6Ly9jaGF0Lmdvb2dsZWFwaXMuY29tL3YxL3NwYWNlcy9BQUFBMTExMS9tZXNzYWdlcz9rZXk9QUl6YVN5RkFLRUtFWSZ0b2tlbj1GQUtFVE9LRU4=")

	if err := store.Set(Ref("alerts", "webhook"), testWebhook); err != nil {
		t.Fatalf("storing: %v", err)
	}

	warnings := strings.Join(store.Warnings(), "\n")
	if strings.Contains(warnings, "FAKETOKEN") || strings.Contains(warnings, "AIzaSyFAKEKEY") {
		t.Errorf("a warning quoted the secret back:\n%s", warnings)
	}
	if !strings.Contains(warnings, Redacted) {
		t.Errorf("the base64 form of the secret was not scrubbed:\n%s", warnings)
	}
}

// TestARemovalThatCouldNotWriteSaysSo is the half of sec-01's fix that sec-01
// did not have.
//
// Store.Delete used to answer missingSecret whenever neither backend removed
// anything, and that sentence, "no credential is stored for X", was returned
// just as readily when neither backend could be *read*. RemoveProfile discarded
// it, so a fallback file at the wrong mode meant `profile rm` printed "removed",
// exited 0, and left the credential in a world-readable file.
//
// The asymmetry is what makes it worth a test rather than a comment: reading
// that same file refuses loudly, and removing from it succeeded silently. The
// silent one is what somebody runs when they want the credential gone.
func TestARemovalThatCouldNotWriteSaysSo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CredentialsFile)

	// A real fallback file holding a real secret, at a mode this package
	// refuses to read.
	if err := os.WriteFile(path, []byte(`{"spacebar/work/token":"1//a-refresh-token"}`), 0o644); err != nil {
		t.Fatalf("writing the fallback file: %v", err)
	}

	// No keyring, which is the machine the fallback exists for.
	store := &Store{keyring: &fakeKeyring{err: errors.New("no keyring here")}, file: &fileStore{path: path}}

	err := store.Delete(Ref("work", TokenSecret))
	if err == nil {
		t.Fatal("a credential that could not be removed was reported as removed")
	}
	for _, want := range []string{path, "0644", "chmod"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not mention %q, so it does not say what to do:\n%v", want, err)
		}
	}

	// And it is still there, which is what makes the failure true rather than
	// merely cautious.
	body, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("reading back: %v", readErr)
	}
	if !strings.Contains(string(body), "a-refresh-token") {
		t.Error("the test is not exercising what it claims: the secret is gone")
	}
}

// TestARemovalOfSomethingAbsentIsNotAFailure holds the other side, and it is
// the one that would make a careless fix worse than the bug.
//
// A machine with no keyring answers every keyring call with an error, and that
// machine is a container, a CI runner or a headless server. Treating "the
// keyring could not be asked" as a failure would make `profile rm` fail on
// every one of them, including for a secret that was never there: a webhook
// profile has no token and no client secret, and removal walks all three.
func TestARemovalOfSomethingAbsentIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CredentialsFile)

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
	}{
		{
			// Nothing has ever been written. load answers with an empty map, so
			// the file is conclusive: the secret is not there.
			name: "no fallback file at all",
			setup: func(t *testing.T) {
				t.Helper()
				_ = os.Remove(path)
			},
		},
		{
			name: "a readable fallback file without this entry",
			setup: func(t *testing.T) {
				t.Helper()
				if err := os.WriteFile(path, []byte(`{"spacebar/other/token":"x"}`), 0o600); err != nil {
					t.Fatalf("writing: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			store := &Store{keyring: &fakeKeyring{err: errors.New("no keyring here")}, file: &fileStore{path: path}}

			if err := store.Delete(Ref("work", TokenSecret)); err != nil {
				t.Fatalf("removing a secret that was never there failed: %v", err)
			}

			// Said rather than returned. Nothing here knows whether the keyring
			// held it, and on this machine nothing can, so the operator is told
			// once instead of the command failing every time.
			warnings := store.Warnings()
			if len(warnings) != 1 {
				t.Fatalf("got %d warnings, want one saying the keyring could not be asked:\n%v",
					len(warnings), warnings)
			}
			if !strings.Contains(warnings[0], "still does") {
				t.Errorf("the warning does not say what it means:\n%s", warnings[0])
			}
		})
	}
}

// TestAKeyringThatHasNothingIsNotWorthAWarning.
//
// A keyring that answers ErrNotFound has been asked and has told us. Warning
// about that would put a line on every removal on every machine that has a
// working keyring, which is how a warning becomes something people stop
// reading.
func TestAKeyringThatHasNothingIsNotWorthAWarning(t *testing.T) {
	store := &Store{
		keyring: &fakeKeyring{err: keyring.ErrNotFound},
		file:    &fileStore{path: filepath.Join(t.TempDir(), CredentialsFile)},
	}

	if err := store.Delete(Ref("work", TokenSecret)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if warnings := store.Warnings(); len(warnings) != 0 {
		t.Errorf("a keyring that said it has nothing produced a warning:\n%v", warnings)
	}
}
