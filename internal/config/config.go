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

// Package config owns one file and one resolution order.
//
// The file is config.json, described in SPEC.md §5.1. The resolution order is
// §5.2, and its second half is the part that is easy to get wrong: fields
// resolve independently of each other, so a flag beats an environment variable
// beats the profile beats the value linked into the binary, per field. A
// profile is not a bundle that wins or loses as a unit.
//
// Nothing here is a secret. Every field that could hold one holds a reference
// to where it lives instead, and Load refuses a file where that is not true.
// internal/auth turns a reference into a value; this package never sees one.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kmoneil/spacebar/internal/output"
)

// RefScheme prefixes a value that names where a secret lives rather than being
// one. SPEC.md §5.3: the secret itself is in the OS keyring, or in the mode
// 0600 fallback that internal/auth falls back to when there is no keyring, and
// either way this file only points at it.
const RefScheme = "keyring:"

// Transport names the way a profile reaches Chat. There are two, they have very
// different capabilities, and SPEC.md §8.1 is the matrix.
type Transport string

const (
	// TransportWebhook is an incoming webhook: write-only, fixed to one space,
	// and posting as a bot. It is what a locked-down Workspace org leaves
	// somebody with, and it needs no OAuth at all.
	TransportWebhook Transport = "webhook"

	// TransportUserOAuth is the full API, acting as the user.
	TransportUserOAuth Transport = "useroauth"
)

// Profile is one way of reaching Chat, named by the user.
type Profile struct {
	Transport Transport `json:"transport"`

	// ClientID is not a secret. It travels in the browser's address bar during
	// consent, so anyone who has authorized this tool has already seen it, and
	// SPEC.md §6.1 is explicit that a native-app client is a quota and
	// reputation boundary rather than a confidentiality one.
	ClientID string `json:"client_id,omitempty"`

	// ClientSecretRef names where the client secret lives. SPEC.md §5.1 shows a
	// client_secret field holding the value; this holds a reference instead.
	// RFC 8252 is right that a native-app secret is not confidential and the
	// tool's security does not rest on it, but a user who created a client in
	// their own Cloud project still did not agree to keep the secret in a file
	// they might paste into an issue, sync to a dotfiles repository, or share
	// on a screen. The indirection costs nothing and the field is inert until
	// Milestone 3.
	ClientSecretRef string `json:"client_secret_ref,omitempty"`

	// Scopes is the narrow set this profile was authorized for, per §6.4. A
	// blanket scope is not requested, because a narrower one materially
	// improves the odds of an administrator approving the app at all.
	Scopes []string `json:"scopes,omitempty"`

	// WebhookURLRef names where the webhook URL lives. The URL carries key and
	// token query parameters that are the entire authentication for that space,
	// which makes it a bearer credential wearing the costume of a URL. It is
	// the most likely way this tool leaks one, precisely because every instinct
	// says a URL is safe to write down.
	WebhookURLRef string `json:"webhook_url_ref,omitempty"`

	// Aliases map a human name onto spaces/XXXX. Resolved at the time the alias
	// is set, so that what is stored is a space and not a display name that
	// will drift out from under it.
	Aliases map[string]string `json:"aliases,omitempty"`
}

// Config is the whole file.
type Config struct {
	DefaultProfile string             `json:"default_profile,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"`

	// path is where this was read from, so that a failure can name the file the
	// user has to edit. Not serialized: it describes the file rather than
	// living in it.
	path string
}

// configErr is a failure the caller fixes by editing a file or passing a
// different flag, which is exit 2 whether or not an argument was involved:
// nothing was sent, and no retry will change the outcome.
func configErr(format string, a ...any) error {
	return output.Errorf("CONFIG", output.ExitUsage, format, a...)
}

// Load reads the configuration from its usual place.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

// LoadFrom reads the configuration from path.
//
// A missing file is not an error. It returns an empty configuration, because a
// command that needs no profile has to work on a machine that has never been
// set up: `spacebar version` on a fresh checkout must not fail because nobody
// has authenticated yet.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{path: path}, nil
	}
	if err != nil {
		return nil, configErr("cannot read %s: %v", path, err)
	}

	cfg := &Config{path: path}
	dec := json.NewDecoder(bytes.NewReader(data))
	// A key nobody reads is a setting its author believes is applied. Refusing
	// the file names the key instead, which is the difference between a typo
	// somebody fixes in ten seconds and a webhook that silently never sends.
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, describeJSONError(path, data, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save writes the configuration back where it was read from.
func (c *Config) Save() error {
	if c.path == "" {
		path, err := Path()
		if err != nil {
			return err
		}
		c.path = path
	}
	return c.SaveTo(c.path)
}

// SaveTo writes the configuration to path, atomically.
//
// Written to a temporary file in the same directory and renamed over the
// target, because the alternative is a window in which config.json is a
// truncated file. A crash inside that window leaves somebody locked out of
// their own tool by a half-written line, with nothing to tell them why. The
// temporary file is in the same directory so that the rename is on one
// filesystem and therefore atomic.
func (c *Config) SaveTo(path string) error {
	if err := c.validate(); err != nil {
		return err
	}

	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return configErr("cannot encode the configuration: %v", err)
	}
	body = append(body, '\n')

	if err := WriteFileAtomic(path, body); err != nil {
		return err
	}

	c.path = path
	return nil
}

// WriteFileAtomic writes body to path at FileMode, in a directory it creates at
// DirMode, without ever leaving a partial file where path is.
//
// Exported because the credential fallback file in internal/auth has the same
// two requirements and the routine is subtle enough that a second copy of it
// would be a second chance to get the mode or the ordering wrong.
func WriteFileAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return configErr("cannot create %s: %v", dir, err)
	}

	// os.CreateTemp creates at 0600 before umask, which is the mode these files
	// need, so there is no window in which one exists more widely readable than
	// it ends up. The temporary file is in the same directory so that the
	// rename stays on one filesystem and is therefore atomic.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return configErr("cannot write in %s: %v", dir, err)
	}
	tmpName := tmp.Name()

	if err := writeAndClose(tmp, body); err != nil {
		_ = os.Remove(tmpName)
		return configErr("cannot write %s: %v", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return configErr("cannot replace %s: %v", path, err)
	}
	return nil
}

// writeAndClose flushes body to f and closes it.
//
// Sync before Close because a rename that beats its own data to disk leaves a
// file that exists and is empty, which is the failure this whole dance is for.
func writeAndClose(f *os.File, body []byte) error {
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Names returns the configured profile names, sorted, for a message that has to
// tell somebody what they could have asked for.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Source names the file this configuration came from, for an error that has to
// say where to go and fix something.
func (c *Config) Source() string { return c.path }

func (c *Config) validate() error {
	for _, name := range c.Names() {
		if err := c.Profiles[name].validate(c.path, name); err != nil {
			return err
		}
	}
	if c.DefaultProfile == "" {
		return nil
	}
	if _, ok := c.Profiles[c.DefaultProfile]; !ok {
		return configErr("%s: default_profile is %q, and there is no profile by that name.\nConfigured: %s",
			c.path, c.DefaultProfile, listOrNone(c.Names()))
	}
	return nil
}

func (p Profile) validate(path, name string) error {
	switch p.Transport {
	case TransportWebhook, TransportUserOAuth:
	case "":
		return configErr("%s: profile %q has no transport.\nSet it to %q for an incoming webhook, or %q for the full API.",
			path, name, TransportWebhook, TransportUserOAuth)
	default:
		return configErr("%s: profile %q has transport %q, which is not one this tool has.\nUse %q or %q.",
			path, name, p.Transport, TransportWebhook, TransportUserOAuth)
	}

	// Ordered rather than ranged over a map, so that a file with two bad fields
	// fails the same way twice in a row and a test can assert on the message.
	refs := []struct{ field, value string }{
		{"webhook_url_ref", p.WebhookURLRef},
		{"client_secret_ref", p.ClientSecretRef},
	}
	for _, ref := range refs {
		if err := checkRef(path, name, ref.field, ref.value); err != nil {
			return err
		}
	}
	return nil
}

// checkRef holds the rule that no secret reaches this file.
//
// The value is never quoted back. A field that is not a reference is holding
// something, and the whole reason this check exists is that the something is
// likely to be a credential: printing it would put it in a terminal, a scroll
// buffer, and whatever the caller pipes stderr into.
func checkRef(path, profile, field, value string) error {
	if value == "" || strings.HasPrefix(value, RefScheme) {
		return nil
	}

	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return configErr("%s: profile %q has a URL in %s, which names where a secret is kept rather than keeping one.\n"+
			"A webhook URL carries key and token query parameters that are the entire authentication for that space, "+
			"so it is a credential and does not belong in a file meant to be read.\n"+
			"Put it in the keyring and leave a %s reference here.",
			path, profile, field, RefScheme)
	}

	return configErr("%s: profile %q has a %s that is not a reference.\nIt must be empty or begin with %q.",
		path, profile, field, RefScheme)
}

func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// describeJSONError turns what encoding/json says into something a person can
// act on.
//
// `json: cannot unmarshal number into Go struct field` names a Go type and a Go
// field to somebody who has never seen this source and is looking at a JSON
// file. Every branch here answers the two questions they actually have: which
// line, and what belongs there.
func describeJSONError(path string, data []byte, err error) error {
	// A file that stops early is not a *json.SyntaxError, it is a bare
	// io.ErrUnexpectedEOF carrying no position at all. It is also the likeliest
	// way a hand-edited file breaks: one closing brace short. Reporting the end
	// of the file is the honest position, because that is where the missing
	// character belongs.
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		line, col := lineCol(data, int64(len(data)))
		return configErr("%s:%d:%d: the file ends before the JSON does, so something is unclosed.",
			path, line, col)
	}

	if e, ok := errors.AsType[*json.SyntaxError](err); ok {
		line, col := lineCol(data, e.Offset)
		return configErr("%s:%d:%d is not valid JSON: %s", path, line, col, e.Error())
	}

	if e, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		line, col := lineCol(data, e.Offset)
		field := e.Field
		if field == "" {
			field = "the value"
		}
		return configErr("%s:%d:%d: %s is %s, and has to be %s.", path, line, col, field, e.Value, e.Type)
	}

	// DisallowUnknownFields reports a bare string with no position in it, so
	// the field is found in the file to get one. This is the branch that
	// catches webhook_url written where webhook_url_ref belongs.
	if quoted, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		if idx := bytes.Index(data, []byte(quoted)); idx >= 0 {
			line, col := lineCol(data, int64(idx))
			return configErr("%s:%d:%d: %s is not a field this tool knows.", path, line, col, quoted)
		}
		return configErr("%s: %s is not a field this tool knows.", path, quoted)
	}

	return configErr("%s: %v", path, err)
}

// lineCol converts a byte offset into a 1-based line and column, because an
// offset is not something anybody can find in an editor.
func lineCol(data []byte, offset int64) (int, int) {
	offset = max(offset, 0)
	offset = min(offset, int64(len(data)))

	line, col := 1, 1
	for _, b := range data[:offset] {
		if b == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return line, col
}
