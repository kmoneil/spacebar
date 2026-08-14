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
// A profile can hold more than one secret: this today, and an OAuth refresh
// token at Milestone 3. Ref builds the reference from the two.
const WebhookSecret = "webhook"

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

// RemoveProfile deletes a profile and the credentials behind it.
//
// The credential goes even when the profile was not in the configuration.
// Somebody removing a profile they half-created is exactly the person who ends
// up with a secret in their keyring that nothing points at, and a delete that
// only removed what it could see would leave it there for good.
//
// A missing credential is not a failure. The profile is gone either way, which
// is what was asked for, and reporting "there was nothing to delete" as an
// error would make an interrupted removal impossible to finish.
func (s *Store) RemoveProfile(cfg *config.Config, profile string) error {
	if err := config.CheckProfileName(profile); err != nil {
		return err
	}

	_ = s.Delete(Ref(profile, WebhookSecret))

	delete(cfg.Profiles, profile)
	if cfg.DefaultProfile == profile {
		// Left set, it names a profile that is not there, and Load refuses a
		// file like that. Removing one profile would break every later command
		// until somebody hand-edited the file this command group exists to
		// avoid hand-editing.
		cfg.DefaultProfile = ""
	}
	return nil
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
	case parsed.Scheme != "https":
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
