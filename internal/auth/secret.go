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

// Package auth keeps credentials out of the configuration file.
//
// config.json holds a keyring: reference; the value behind it lives in the OS
// keyring, or in a fallback file at mode 0600 on a machine that has no keyring
// (SPEC.md §5.3, §6.6). Nothing else in this tool reads a secret from anywhere.
//
// The fallback exists because refusing to run without a keyring would exclude
// exactly the population this project is built for: a container, a CI runner,
// and a headless server all lack one, and all three are where a script that
// posts to Chat actually runs. It warns every invocation instead, because a
// warning printed once is a warning nobody sees.
package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// CredentialsFile is the fallback beside config.json.
const CredentialsFile = "credentials.json"

// SecretName is which of a profile's secrets a reference points at.
//
// A named type rather than a string, so that the set of them is something the
// compiler and a gate can both see. What it is not is a guarantee on its own:
// an untyped literal converts implicitly, so Ref(profile, "made-up") still
// builds. What closes that is the pair of gates in internal/lint, which require
// every SecretName constant to appear in ProfileSecrets and every call to Ref
// to name one of those constants rather than a literal.
type SecretName string

// ProfileSecrets is every name a profile's credential can be stored under.
//
// It exists because removing a profile has to remove all of them, and the list
// was previously implicit: RemoveProfile named one of the three by hand and the
// other two outlived the command that said it had deleted them. A profile's
// OAuth token and client secret stayed in the keyring, and the command reported
// success.
//
// The constants stay beside the code that uses them rather than being gathered
// here, because that is where their reasons are. What makes the distance safe
// is TestEverySecretNameIsInProfileSecrets, which reads this package's own
// source: a SecretName declared anywhere in it and missing from this list fails
// the build.
var ProfileSecrets = []SecretName{WebhookSecret, TokenSecret, ClientSecretName}

// Ref builds the reference that goes in config.json for one secret.
//
// The service is the product name, from meta.AppName, so that a rename stays a
// change to that constant. The rest names the profile and which secret it is,
// because a profile can have more than one: a webhook URL, an OAuth token, and
// the client secret behind that token.
func Ref(profile string, name SecretName) string {
	return config.RefScheme + meta.AppName + "/" + profile + "/" + string(name)
}

// backend is one place a secret can live. Two implement it, and tests
// substitute their own, because a test that touched the real keyring would
// leave entries on the machine that ran it.
type backend interface {
	get(service, key string) (string, error)
	set(service, key, value string) error
	remove(service, key string) error
}

// Store reads and writes secrets, preferring the OS keyring.
type Store struct {
	keyring backend
	file    backend

	// mu guards warnings, which several goroutines can append to at once.
	//
	// One command in one goroutine was the only caller until the MCP server,
	// which serves tool calls concurrently over one profile, so two failed
	// keyring writes could append to this slice at the same time. The keyring
	// and file backends are the OS's problem and are safe; this slice is ours.
	mu       sync.Mutex
	warnings []string
}

// New builds a Store over the OS keyring and the fallback file beside
// config.json.
func New() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return &Store{
		keyring: osKeyring{},
		file:    &fileStore{path: filepath.Join(dir, CredentialsFile)},
	}, nil
}

// Warnings returns what the caller has to print to stderr, once per
// invocation, deduplicated.
//
// Returned rather than printed because only internal/output writes to a
// process stream: a warning built here and printed there is escaped by the one
// package that knows how, and this package cannot quietly become a second
// place that writes to a terminal.
func (s *Store) Warnings() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.warnings...)
}

// AddWarnings folds warnings from elsewhere into this store's list.
//
// It exists because the seven-day expiry warning is produced by a Source rather
// than by the store, and a caller collecting warnings from two places is a
// caller who will one day collect from one. Deduplication is the same as for a
// warning raised here, so a Source and a fallback file both reporting the same
// sentence still say it once.
func (s *Store) AddWarnings(warnings []string) {
	for _, warning := range warnings {
		if warning != "" {
			s.warn("%s", warning)
		}
	}
}

func (s *Store) warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)

	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.Contains(strings.Join(s.warnings, "\n"), msg) {
		s.warnings = append(s.warnings, msg)
	}
}

// Get resolves a reference to its secret.
//
// The keyring is asked first. Anything other than success falls through to the
// fallback file, including "no such entry": a machine that gained a keyring
// after the secret was written must not lose it, and the file is where it will
// still be.
func (s *Store) Get(ref string) (string, error) {
	service, key, err := parseRef(ref)
	if err != nil {
		return "", err
	}

	value, keyringErr := s.keyring.get(service, key)
	if keyringErr == nil {
		return value, nil
	}

	value, fileErr := s.file.get(service, key)
	if fileErr == nil {
		s.warn("read a credential from %s rather than the OS keyring, which is unavailable: %v\n"+
			"The secret is on that file in plain text, readable by anything running as this user.",
			s.file, redactError(keyringErr, ""))
		return value, nil
	}
	if unusable := (*unusableError)(nil); errors.As(fileErr, &unusable) {
		return "", fileErr
	}

	return "", missingSecret(ref, keyringErr)
}

// Set stores a secret, preferring the keyring and falling back to the file.
func (s *Store) Set(ref, value string) error {
	service, key, err := parseRef(ref)
	if err != nil {
		return err
	}

	if keyringErr := s.keyring.set(service, key, value); keyringErr != nil {
		if fileErr := s.file.set(service, key, value); fileErr != nil {
			return fileErr
		}
		s.warn("stored a credential in %s rather than the OS keyring, which is unavailable: %v\n"+
			"It is on that file in plain text, readable by anything running as this user.",
			s.file, redactError(keyringErr, value))
	}
	return nil
}

// Delete removes a secret from both places.
//
// Both, unconditionally, and a failure in either is ignored when the other
// succeeded. A logout that leaves the credential in the fallback file because
// the keyring answered first has not logged anybody out.
func (s *Store) Delete(ref string) error {
	service, key, err := parseRef(ref)
	if err != nil {
		return err
	}

	keyringErr := s.keyring.remove(service, key)
	fileErr := s.file.remove(service, key)
	if keyringErr != nil && fileErr != nil {
		return missingSecret(ref, keyringErr)
	}
	return nil
}

// parseRef splits a keyring: reference into the service and the key.
func parseRef(ref string) (string, string, error) {
	rest, ok := strings.CutPrefix(ref, config.RefScheme)
	if !ok {
		return "", "", secretErr("%q is not a credential reference; it has to begin with %q.",
			ref, config.RefScheme)
	}
	service, key, ok := strings.Cut(rest, "/")
	if !ok || service == "" || key == "" {
		// The example is built here rather than through Ref, which takes a
		// SecretName and would be storing under one if it were called with a
		// placeholder. internal/lint holds every Ref call to the names in
		// ProfileSecrets, and a message is not a call site that should have to
		// argue its way past that.
		return "", "", secretErr("%q is not a credential reference; it has to look like %q.",
			ref, config.RefScheme+meta.AppName+"/<profile>/<secret>")
	}
	return service, key, nil
}

func secretErr(format string, a ...any) error {
	return output.Errorf("CREDENTIAL", output.ExitUsage, format, a...)
}

// missingSecret is exit 4 rather than a generic failure.
//
// The profile is configured and the secret behind it is not there, which is
// the same situation as an expired token from a caller's point of view: the
// fix is to go and get a credential, and a script needs to tell that apart
// from a space that does not exist or a message that was refused.
func missingSecret(ref string, keyringErr error) error {
	return output.Errorf("CREDENTIAL_MISSING", output.ExitAuthRequired,
		"no credential is stored for %s.\nThe OS keyring did not have it (%v) and neither did the fallback file.",
		ref, redactError(keyringErr, ""))
}

// redactError makes a backend's error safe to print.
//
// A keyring error is diagnostic and worth keeping: "dbus-launch: executable
// file not found" tells somebody to start a session bus, where "the keyring is
// unavailable" tells them nothing. But the darwin backend passes the secret to
// /usr/bin/security on stdin, base64-encoded, so an error carrying that
// command's output is one upstream change away from carrying the secret. Both
// forms are scrubbed rather than trusting that today's implementation keeps
// them apart.
func redactError(err error, secret string) string {
	if err == nil {
		return "none"
	}
	msg := err.Error()
	if secret == "" {
		return msg
	}
	for _, form := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret))} {
		msg = strings.ReplaceAll(msg, form, "REDACTED")
	}
	return msg
}

// osKeyring is the real thing, and the only place this tool touches it.
type osKeyring struct{}

func (osKeyring) get(service, key string) (string, error) { return keyring.Get(service, key) }

func (osKeyring) set(service, key, value string) error { return keyring.Set(service, key, value) }

func (osKeyring) remove(service, key string) error { return keyring.Delete(service, key) }
