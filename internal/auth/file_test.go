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
	"runtime"
	"strings"
	"testing"
)

// TestAWideFallbackFileIsRefused is the claim from the card, and the one place
// this tool refuses to proceed over a file mode.
//
// A warning would be the friendlier answer and the wrong one: it leaves the
// file exactly as readable as it was, prints the same line every invocation,
// and becomes something the operator learns to scroll past. The credential is
// in plain text and every other user on the machine can read it.
func TestAWideFallbackFileIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not carry the same meaning here")
	}

	for _, mode := range []os.FileMode{0o644, 0o604, 0o640, 0o666, 0o660} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), CredentialsFile)
			if err := os.WriteFile(path, []byte(`{"spacebar/alerts/webhook":"`+testWebhook+`"}`), mode); err != nil {
				t.Fatalf("writing the fixture: %v", err)
			}

			store := &Store{keyring: &fakeKeyring{err: errNotInFile}, file: &fileStore{path: path}}
			_, err := store.Get(Ref("alerts", "webhook"))
			if err == nil {
				t.Fatalf("a file at mode %#o was read", mode.Perm())
			}

			for _, want := range []string{path, "chmod 0600"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not mention %q:\n%v", want, err)
				}
			}
			if strings.Contains(err.Error(), "FAKETOKEN") {
				t.Errorf("the refusal quoted the credential back:\n%v", err)
			}
		})
	}
}

func TestAnOwnerOnlyFallbackFileIsRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), CredentialsFile)
	if err := os.WriteFile(path, []byte(`{"spacebar/alerts/webhook":"`+testWebhook+`"}`), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	store := &Store{keyring: &fakeKeyring{err: errNotInFile}, file: &fileStore{path: path}}
	got, err := store.Get(Ref("alerts", "webhook"))
	if err != nil {
		t.Fatalf("reading a 0600 file: %v", err)
	}
	if got != testWebhook {
		t.Errorf("read back %q", RedactURL(got))
	}
}

// TestACorruptFallbackFileIsNotQuotedBack keeps the file's contents out of the
// failure. encoding/json quotes the input it choked on, and the input here is a
// file of credentials.
func TestACorruptFallbackFileIsNotQuotedBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), CredentialsFile)
	if err := os.WriteFile(path, []byte(`{"spacebar/alerts/webhook": `+testWebhook), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	store := &Store{keyring: &fakeKeyring{err: errNotInFile}, file: &fileStore{path: path}}
	_, err := store.Get(Ref("alerts", "webhook"))
	if err == nil {
		t.Fatal("a file that is not JSON was accepted")
	}
	if strings.Contains(err.Error(), "FAKETOKEN") || strings.Contains(err.Error(), "AIzaSyFAKEKEY") {
		t.Errorf("the failure quoted the file back:\n%v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the failure does not name the file:\n%v", err)
	}
}

// TestTheFallbackFileKeepsOtherSecrets guards the read-modify-write. Two
// profiles share the file, and storing one must not drop the other.
func TestTheFallbackFileKeepsOtherSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), CredentialsFile)
	file := &fileStore{path: path}

	if err := file.set("spacebar", "alerts/webhook", testWebhook); err != nil {
		t.Fatalf("storing the first: %v", err)
	}
	if err := file.set("spacebar", "work/token", "ya29.FAKE"); err != nil {
		t.Fatalf("storing the second: %v", err)
	}

	got, err := file.get("spacebar", "alerts/webhook")
	if err != nil || got != testWebhook {
		t.Errorf("the first secret did not survive the second: %q, %v", RedactURL(got), err)
	}

	if err := file.remove("spacebar", "work/token"); err != nil {
		t.Fatalf("removing the second: %v", err)
	}
	if _, err := file.get("spacebar", "alerts/webhook"); err != nil {
		t.Errorf("the first secret did not survive the removal of the second: %v", err)
	}
}

func TestAMissingFallbackFileIsNotAFailure(t *testing.T) {
	file := &fileStore{path: filepath.Join(t.TempDir(), CredentialsFile)}

	secrets, err := file.load()
	if err != nil {
		t.Fatalf("a missing file should load as empty: %v", err)
	}
	if len(secrets) != 0 {
		t.Errorf("got %d secrets from a file that does not exist", len(secrets))
	}
}
