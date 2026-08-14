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
	"fmt"
	"time"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// TokenSecret names the OAuth token inside a profile's credentials, per
// SPEC.md §6.6.
const TokenSecret = "token"

// TestingWindow is how long a refresh token lives for an application that is
// still in testing.
//
// An External OAuth client in Testing has its refresh tokens revoked seven days
// after consent, with a hundred-test-user cap besides. SPEC.md §6.7 treats that
// as routine rather than exceptional, and it is: a large share of the people
// this tool is for will be in exactly that state permanently, because getting
// an application verified is not something they can do.
const TestingWindow = 7 * 24 * time.Hour

// WarnWithin is how close to that boundary the warning starts.
const WarnWithin = 24 * time.Hour

// tokenRecord is the JSON that goes in the keyring.
//
// Field names are snake_case and stable: this is written by one version of the
// binary and read by another, on a machine somebody does not want to reconfigure
// because they upgraded.
type tokenRecord struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	TokenType    string    `json:"token_type"`
	Scopes       []string  `json:"scopes,omitempty"`

	// ObtainedAt is when consent was given. Stored rather than derived because
	// the seven-day death is measured from consent, and the access token's
	// expiry says nothing about it: that one is an hour away at all times.
	ObtainedAt time.Time `json:"obtained_at"`

	// BeyondTestingWindow records that a refresh succeeded more than
	// TestingWindow after consent, which proves this client is not subject to
	// the testing-mode limit. See Assess.
	BeyondTestingWindow bool `json:"beyond_testing_window,omitempty"`
}

// SaveToken stores a profile's OAuth token.
func (s *Store) SaveToken(profile string, token *Token) error {
	body, err := json.Marshal(tokenRecord{
		AccessToken:         token.AccessToken,
		RefreshToken:        token.RefreshToken,
		Expiry:              token.Expiry,
		TokenType:           token.TokenType,
		Scopes:              token.Scopes,
		ObtainedAt:          token.ObtainedAt,
		BeyondTestingWindow: token.BeyondTestingWindow,
	})
	if err != nil {
		// Nothing in the record can fail to marshal, so this is unreachable.
		// It is handled rather than ignored because "unreachable" is a claim
		// about the shape of a struct somebody may change.
		return tokenErr("cannot encode the token for %q: %v", profile, err)
	}
	return s.Set(Ref(profile, TokenSecret), string(body))
}

// LoadToken reads a profile's OAuth token.
//
// A missing token is ExitAuthRequired rather than a generic failure, because
// the fix is to authorize and a script needs to tell that apart from a space
// that does not exist.
func (s *Store) LoadToken(profile string) (*Token, error) {
	body, err := s.Get(Ref(profile, TokenSecret))
	if err != nil {
		return nil, err
	}

	var record tokenRecord
	if err := json.Unmarshal([]byte(body), &record); err != nil {
		// The value is not quoted back. It is a token.
		return nil, tokenErr("the stored token for %q is not readable. Authorize again to replace it.", profile)
	}

	return &Token{
		AccessToken:         record.AccessToken,
		RefreshToken:        record.RefreshToken,
		Expiry:              record.Expiry,
		TokenType:           record.TokenType,
		Scopes:              record.Scopes,
		ObtainedAt:          record.ObtainedAt,
		BeyondTestingWindow: record.BeyondTestingWindow,
	}, nil
}

// DeleteToken removes a profile's OAuth token from both places it could be.
func (s *Store) DeleteToken(profile string) error {
	return s.Delete(Ref(profile, TokenSecret))
}

// Status is what `auth status` reports, per SPEC.md §6.7.
type Status struct {
	Profile   string   `json:"profile"`
	Transport string   `json:"transport"`
	Scopes    []string `json:"scopes,omitempty"`

	// ExpiresAt is when the access token expires, which is an hour away at all
	// times and is not the number anybody is worried about.
	ExpiresAt time.Time `json:"expires_at,omitzero"`

	// DaysRemaining is against the seven-day boundary, and is the number that
	// matters. Absent when this authorization has already outlived it, because
	// then there is no boundary left to count towards.
	DaysRemaining *float64 `json:"days_remaining,omitempty"`

	NeedsReauth bool `json:"needs_reauth"`

	// Warning is what would be printed on stderr, repeated here so that a
	// caller reading --json is not the only one who does not get it.
	Warning string `json:"warning,omitempty"`
}

// Assess reports what to tell somebody about a token's remaining life.
//
// This is the decision the card left open, and it turns on something that
// cannot be known: there is no signal anywhere in the API that says whether an
// OAuth client is in testing mode. The seven-day boundary is inferred from when
// consent was given, so a warning fires just as readily for somebody on an
// Internal client whose token is fine for a year.
//
// Two things follow. The warning is worded as a possibility, because it is one,
// and saying "expires in 4h" to somebody it does not apply to is a tool being
// confidently wrong in the one place it is trying to be helpful.
//
// And it stops. A refresh that succeeds more than seven days after consent is
// proof that this client is not subject to the limit, because a testing-mode
// refresh token would have been dead. That fact is recorded with the token and
// the warning never fires again for that authorization. Nobody is asked, nothing
// is configured, and the tool stops being wrong on its own. A new consent
// resets it, which is right: it could be a different client.
func Assess(token *Token, now time.Time) (warning string, needsReauth bool) {
	if token == nil {
		return "", true
	}
	if token.BeyondTestingWindow || token.ObtainedAt.IsZero() {
		// The second case is a token written before this field existed, or one
		// stored by something else. Nothing is known about it, so nothing is
		// claimed.
		return "", false
	}

	remaining := TestingWindow - now.Sub(token.ObtainedAt)
	switch {
	case remaining <= 0:
		return "this authorization is more than seven days old. If this OAuth client is still in " +
			"testing, its refresh token has already expired, and the next command will say so. " +
			"Authorize again to be sure.", false

	case remaining <= WarnWithin:
		return fmt.Sprintf("if this OAuth client is still in testing, this authorization expires in "+
			"about %dh. That limit does not apply to a client with an Internal user type, and this "+
			"warning stops once a refresh proves it does not apply here.",
			int(remaining.Hours())), false
	}
	return "", false
}

// StatusOf describes a token without touching the network.
//
// Nothing here refreshes. `auth status` is the command somebody runs when
// something is wrong, so it answers on a machine whose keyring is readable and
// whose network is not.
func StatusOf(profile, transport string, token *Token, now time.Time) Status {
	status := Status{Profile: profile, Transport: transport}
	if token == nil {
		status.NeedsReauth = true
		return status
	}

	status.Scopes = token.Scopes
	status.ExpiresAt = token.Expiry
	status.Warning, status.NeedsReauth = Assess(token, now)

	if token.BeyondTestingWindow || token.ObtainedAt.IsZero() {
		return status
	}
	if remaining := TestingWindow - now.Sub(token.ObtainedAt); remaining > 0 {
		days := remaining.Hours() / 24
		status.DaysRemaining = &days
	}
	return status
}

func tokenErr(format string, a ...any) error {
	return output.Errorf("TOKEN", output.ExitAuthRequired, format, a...)
}

// ClientCredentials resolves the OAuth client for a profile, per SPEC.md §5.2
// and §6.1.
//
// The ladder runs per field: the environment, then the profile, then whatever
// was linked into the binary. There is deliberately no flag on either, because
// a client secret as an argument lands in the shell history and in the process
// list, and internal/cli holds that rule by walking the flag tree.
//
// An empty result is not an error here. A build from source has empty defaults
// on purpose, and the command that needs a client is the one that can say what
// to do about it.
func (s *Store) ClientCredentials(profile config.Profile) (id, secret string, err error) {
	id = config.Resolve("", config.Env("CLIENT_ID"), profile.ClientID, meta.DefaultClientID)

	// The secret is behind a reference, unlike the ID: SPEC.md §5.1 shows the
	// value in the file and this repository refused that in m2-02. RFC 8252 is
	// right that a native-app secret is not confidential, and a user who made a
	// client in their own Cloud project still did not agree to keep it in a
	// file they might paste into an issue.
	stored := ""
	if profile.ClientSecretRef != "" {
		stored, err = s.Get(profile.ClientSecretRef)
		if err != nil {
			return "", "", err
		}
	}
	secret = config.Resolve("", config.Env("CLIENT_SECRET"), stored, meta.DefaultClientSecret)

	return id, secret, nil
}
