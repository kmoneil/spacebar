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
	"net/url"
	"strings"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
)

// WebhookSecret names the webhook URL inside a profile's credentials.
//
// A profile can hold more than one secret: this, an OAuth token, and the client
// secret behind that token. Ref builds the reference from the profile and the
// name, and ProfileSecrets is the whole set, which is what removal walks.
const WebhookSecret SecretName = "webhook"

// SetWebhook stores url as profile's webhook credential and records the
// reference in cfg, creating the profile when there is not one.
//
// The order matters and is the opposite of the obvious one. The secret is
// written first and the configuration second, so that a failure between them
// leaves a credential nothing refers to rather than a reference to a credential
// that is not there. The first is invisible; the second is a profile that
// exists, looks configured, and fails at send time.
//
// cfg is saved by the caller, because a caller may have more than one change to
// make and a half-applied configuration file is the thing SaveTo exists to
// prevent.
func (s *Store) SetWebhook(cfg *config.Config, profile, rawURL string) error {
	if err := config.CheckProfileName(profile); err != nil {
		return err
	}
	if err := CheckWebhookURL(rawURL); err != nil {
		return err
	}

	s.warnUnexpectedHost(rawURL)

	ref := Ref(profile, WebhookSecret)
	if err := s.Set(ref, rawURL); err != nil {
		return err
	}

	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	existing := cfg.Profiles[profile]
	existing.Transport = config.TransportWebhook
	existing.WebhookURLRef = ref
	cfg.Profiles[profile] = existing

	return nil
}

// RemoveProfile deletes a profile and every credential behind it.
//
// Every one, from ProfileSecrets, and not the one this used to name. It deleted
// only the webhook URL, which meant `profile rm` on a user-OAuth profile
// removed the configuration entry, printed "removed", exited 0, and left the
// OAuth token and the client secret exactly where they were. The token record
// holds a refresh token, so what survived was a long-lived credential for
// somebody's Chat account, after the command whose own help says it destroys
// it.
//
// That is the failure this repository already named for `auth logout` and fixed
// there: a false report to somebody removing their access is worse than a
// refusal, because the refusal sends them to look and the report sends them
// away. The person it costs is the careful one, retiring a laptop or baking an
// image.
//
// The credential goes even when the profile was not in the configuration.
// Somebody removing a profile they half-created is exactly the person who ends
// up with a secret in their keyring that nothing points at, and a delete that
// only removed what it could see would leave it there for good.
//
// A missing credential is not a failure, and Store.Delete says so by returning
// nil for one: the profile is gone either way, which is what was asked for, and
// reporting "there was nothing to delete" as an error would make an interrupted
// removal impossible to finish.
//
// A credential that could not be removed **is** a failure, and this used to
// discard it. Every result was thrown away, so a fallback file at the wrong
// mode meant `profile rm` printed "removed", exited 0, and left the credential
// on disk. That is the same false report this function was fixed for once
// already, one level further down: the first fix made it delete all three
// secrets, and this one makes it tell the truth about whether it did.
//
// Every secret is still attempted even after one fails, and the first failure
// is what gets reported. Stopping at the first would leave the rest behind for
// no reason: they are independent, and a keyring that refuses one has nothing
// to say about the file that holds another.
func (s *Store) RemoveProfile(cfg *config.Config, profile string) error {
	if err := config.CheckProfileName(profile); err != nil {
		return err
	}

	var failed error
	for _, name := range ProfileSecrets {
		if err := s.Delete(Ref(profile, name)); err != nil && failed == nil {
			failed = err
		}
	}

	// The configuration entry goes whether or not a secret could be removed.
	// Leaving the profile behind as well would mean a command that failed
	// halfway leaves nothing removed and no way to retry the half that worked,
	// and the failure above still says the credential is still there.
	delete(cfg.Profiles, profile)
	if cfg.DefaultProfile == profile {
		// Left set, it names a profile that is not there, and Load refuses a
		// file like that. Removing one profile would break every later command
		// until somebody hand-edited the file this command group exists to
		// avoid hand-editing.
		cfg.DefaultProfile = ""
	}
	return failed
}

// ChatHost is where a Chat incoming webhook URL points.
//
// Not a rule, which is the point of it being separate from CheckWebhookURL: a
// URL on another host is stored and used. See warnUnexpectedHost.
const ChatHost = "chat.googleapis.com"

// warnUnexpectedHost says so when a webhook URL does not point at Chat.
//
// A warning rather than a refusal, and the two halves of that are worth
// separating. CheckWebhookURL is deliberately loose about the host, and its
// reasoning is right: Google is free to change it, and a validator that refused
// a URL the API would have accepted is unfixable from the user's side. That
// stands, so nothing here refuses anything.
//
// What was missing is the other half. Once a URL is stored, nothing ever shows
// the operator where their messages go. The space comes out of the URL's own
// path, `spaceOf` reports that as the destination on every send because the URL
// is the fact rather than the response, and `profile list` prints a name, a
// transport and whether a credential is recorded. So a URL pasted from the
// wrong place posts every message to somebody else's host while every line this
// tool prints says `spaces/AAAA`, which is exactly what the operator expected to
// see. The only command that would show them is `--dry-run`, which they have no
// reason to run because nothing looks wrong.
//
// This fires once, when the URL is pasted, which is the moment somebody still
// has it in front of them and can compare. A warning on every send would be a
// line people learn to scroll past, and refusing would break the day Google
// moves the host.
//
// Loopback is exempt because that is what a test server is, which is the same
// exemption isSafeScheme makes and for the same reason.
//
// The URL is not quoted back, only its host. The rest of it is a credential.
func (s *Store) warnUnexpectedHost(rawURL string) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		// Unreachable: CheckWebhookURL has already parsed this and refused a
		// URL with no host. Kept because "unreachable" is a claim about the
		// order two functions are called in today.
		return
	}

	host := parsed.Hostname()
	if host == ChatHost || IsLoopbackHost(host) {
		return
	}

	s.warn("this webhook URL points at %s rather than %s, so messages sent through this profile "+
		"go there.\nThat is allowed, because Google may change the host and refusing a URL the API "+
		"would accept cannot be fixed from your side. It is worth checking now: nothing after this "+
		"prints the host, and a URL copied from the wrong place looks exactly like one copied from "+
		"the right place.", host, ChatHost)
}

// CheckWebhookURL reports why raw is not a Chat incoming webhook URL.
//
// This is worth doing at the moment the URL is pasted rather than at the moment
// it is used. The URL is long, it is copied by hand out of a dialog, and the
// most common way it goes wrong is truncation, which produces a credential that
// looks complete. Caught here it costs one paste; caught at send time it is a
// 400 whose message is about an API key, on a day somebody is trying to ship.
//
// The checks are deliberately loose about everything except what has to be
// true. Google is free to change the host or add a parameter, and a validator
// that refused a URL the API would have accepted would be worse than no
// validator at all: it would be unfixable from the user's side.
//
// Nothing here quotes the URL back. It is a credential, and an error message is
// a place credentials go to be discovered.
func CheckWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return webhookErr("no webhook URL was given.")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return webhookErr("that is not a URL: %v.", redactParse(err))
	}

	switch {
	// The loopback exception is the same one internal/chat applies when it
	// builds a client, for the same reason: a test server is http on 127.0.0.1,
	// and a rule that could not be tested would be a rule nobody had run.
	case !isSafeScheme(parsed):
		return webhookErr("a webhook URL is https, and that one is not.\n" +
			"It carries a credential in its query string, so it cannot travel in the clear.")
	case parsed.Host == "":
		return webhookErr("that URL names no host.")
	case !strings.Contains(parsed.Path, "/spaces/"):
		return webhookErr("a webhook URL has a /spaces/ path naming the space it posts to, and that one does not.\n" +
			"It may have been truncated when it was copied.")
	}

	query := parsed.Query()
	for _, name := range []string{"key", "token"} {
		if query.Get(name) == "" {
			return webhookErr("a webhook URL carries a %q query parameter and that one does not.\n"+
				"It is most likely truncated: copy the whole of it, including everything after the question mark.",
				name)
		}
	}
	return nil
}

// isSafeScheme reports whether a credential may travel to this URL.
//
// https anywhere, and http only to a loopback IP literal. A name is not enough
// for the exception: "localhost" resolves through whatever the machine's
// resolver says, which is the same reason SPEC.md §15.4 refuses it for the
// OAuth listener.
func isSafeScheme(u *url.URL) bool {
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && IsLoopbackHost(u.Hostname())
}

// webhookErr is exit 2. Nothing was sent, and no retry changes the answer: the
// value has to be corrected by whoever supplied it.
func webhookErr(format string, a ...any) error {
	return output.Errorf("WEBHOOK_URL", output.ExitUsage, format, a...)
}

// redactParse keeps the URL out of the message that says it will not parse.
// url.Parse wraps its reason in a *url.Error whose Error method quotes what it
// was given.
func redactParse(err error) string {
	if e, ok := errors.AsType[*url.Error](err); ok {
		return e.Err.Error()
	}
	return "it is not a URL"
}
