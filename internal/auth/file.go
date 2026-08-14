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
	"encoding/json"
	"errors"
	"os"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
)

// fileStore is the fallback for a machine with no OS keyring: a container, a CI
// runner, a headless server.
//
// One flat JSON object of "service/key" to secret, at mode 0600. Flat because
// the file is a lookup table and nothing else reads it, and a shape with rooms
// in it invites somebody to put something else in one.
type fileStore struct{ path string }

// String is what a warning names, so the operator can find the file.
func (f *fileStore) String() string { return f.path }

// unusableError is a fallback file that exists and must not be read.
//
// Distinct from "the secret is not here" because the two need opposite
// handling: a missing entry falls through to a useful error about the
// credential, while a file at the wrong mode has to stop everything and say so.
// Silently ignoring it would be the worst outcome, because the operator would
// keep a world-readable credential file and never learn.
type unusableError struct{ err error }

func (e *unusableError) Error() string { return e.err.Error() }

func (e *unusableError) Unwrap() error { return e.err }

func (f *fileStore) load() (map[string]string, error) {
	fi, err := os.Stat(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, &unusableError{err: credentialFileErr("cannot read %s: %v", f.path, err)}
	}

	// Any bit set outside the owner's is somebody else with read access to a
	// credential. Refused rather than warned about: a warning leaves the file
	// exactly as wide as it was, and the next invocation prints the same line.
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return nil, &unusableError{err: credentialFileErr(
			"%s is mode %#o, and it holds credentials in plain text.\nRun: chmod 0600 %s",
			f.path, perm, f.path)}
	}

	body, err := os.ReadFile(f.path)
	if err != nil {
		return nil, &unusableError{err: credentialFileErr("cannot read %s: %v", f.path, err)}
	}

	secrets := map[string]string{}
	if err := json.Unmarshal(body, &secrets); err != nil {
		// The parse error is not repeated: encoding/json quotes the input it
		// choked on, and the input here is a file of credentials.
		return nil, &unusableError{err: credentialFileErr(
			"%s is not valid JSON. It holds credentials, so it is not quoted back here.", f.path)}
	}
	return secrets, nil
}

func (f *fileStore) save(secrets map[string]string) error {
	body, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return credentialFileErr("cannot encode the credentials: %v", err)
	}
	return config.WriteFileAtomic(f.path, append(body, '\n'))
}

func (f *fileStore) get(service, key string) (string, error) {
	secrets, err := f.load()
	if err != nil {
		return "", err
	}
	value, ok := secrets[service+"/"+key]
	if !ok {
		return "", errNotInFile
	}
	return value, nil
}

func (f *fileStore) set(service, key, value string) error {
	secrets, err := f.load()
	if err != nil {
		return err
	}
	secrets[service+"/"+key] = value
	return f.save(secrets)
}

func (f *fileStore) remove(service, key string) error {
	secrets, err := f.load()
	if err != nil {
		return err
	}
	name := service + "/" + key
	if _, ok := secrets[name]; !ok {
		return errNotInFile
	}
	delete(secrets, name)
	return f.save(secrets)
}

// errNotInFile says the file was readable and did not have this entry. It never
// reaches a user: Store turns it into a message about the credential.
var errNotInFile = errors.New("no such credential in the fallback file")

func credentialFileErr(format string, a ...any) error {
	return output.Errorf("CREDENTIAL_FILE", output.ExitError, format, a...)
}
