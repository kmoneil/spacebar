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
	"context"
	"errors"
	"time"

	"golang.org/x/oauth2"

	"github.com/kmoneil/spacebar/internal/output"
)

// Source hands out a live access token for one profile, refreshing and
// persisting as it goes.
//
// The persisting half is the reason this type exists rather than an
// oauth2.TokenSource used directly. x/oauth2 has no hook that fires when a
// token is refreshed: TokenSource is a bare Token() method, and reuseTokenSource
// holds the refreshed value in memory and nowhere else. So a rotated refresh
// token lives exactly as long as the process, and the next command starts from
// the stale one in the keyring and is told to authorize again. That failure
// arrives a week later, looks like the seven-day expiry, and is not.
//
// Google usually returns no refresh token on a refresh, and the library keeps
// the old one when it does, so this is quiet most of the time. "Most of the
// time" is not a property worth relying on for something whose failure is
// indistinguishable from an unrelated one.
type Source struct {
	store   *Store
	profile string
	src     oauth2.TokenSource

	// held is what is currently stored, and what a rotation is compared
	// against.
	held *Token

	now func() time.Time
}

// NewSource builds a token source over a stored token.
//
// httpClient is the value chat.TokenHTTPClient produced, passed through as an
// any for the reason on Flow.HTTPClient: naming the type would mean importing
// the package that imports this one.
func NewSource(ctx context.Context, store *Store, profile string, token *Token, clientID, clientSecret string, httpClient any) *Source {
	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       token.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   AuthEndpoint,
			TokenURL:  TokenEndpoint,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	if httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	}

	return &Source{
		store:   store,
		profile: profile,
		src: config.TokenSource(ctx, &oauth2.Token{
			AccessToken:  token.AccessToken,
			RefreshToken: token.RefreshToken,
			Expiry:       token.Expiry,
			TokenType:    token.TokenType,
		}),
		held: token,
		now:  time.Now,
	}
}

// AccessToken returns a token that is good now, refreshing if it is not.
func (s *Source) AccessToken() (string, error) {
	fresh, err := s.src.Token()
	if err != nil {
		return "", refreshErr(err)
	}

	s.persist(fresh)
	return fresh.AccessToken, nil
}

// Authorization is the header value for a request, which is what
// internal/chat's Authorizer asks for.
func (s *Source) Authorization(context.Context) (string, error) {
	access, err := s.AccessToken()
	if err != nil {
		return "", err
	}
	return "Bearer " + access, nil
}

// Refresh forces one, and reports whether the credential changed.
//
// The signature is internal/chat's, which retries once after a 401. Asking for
// a token here is enough: x/oauth2 refreshes when the one it holds has expired,
// and when it has not the 401 was about something else and a second identical
// request would be a request made to learn something already known.
func (s *Source) Refresh(context.Context) (bool, error) {
	before := s.held.AccessToken

	access, err := s.AccessToken()
	if err != nil {
		return false, err
	}
	return access != before, nil
}

// Warnings is what the caller prints, once, per SPEC.md §6.7.
func (s *Source) Warnings() []string {
	warning, _ := Assess(s.held, s.now())
	if warning == "" {
		return nil
	}
	return []string{warning}
}

// persist writes a changed token back, and records the one fact that stops the
// expiry warning.
//
// Nothing is written when nothing changed, because the common case is a command
// that runs inside the access token's hour and refreshes nothing, and a keyring
// write per command is a keychain prompt per command on macOS.
func (s *Source) persist(fresh *oauth2.Token) {
	changed := fresh.AccessToken != s.held.AccessToken ||
		(fresh.RefreshToken != "" && fresh.RefreshToken != s.held.RefreshToken)

	// A refresh that succeeded more than seven days after consent proves this
	// client is not in testing mode: a testing-mode refresh token would have
	// been dead. Recorded once, and it stops the warning for good.
	proven := !s.held.BeyondTestingWindow &&
		changed &&
		!s.held.ObtainedAt.IsZero() &&
		s.now().Sub(s.held.ObtainedAt) > TestingWindow

	if !changed && !proven {
		return
	}

	s.held.AccessToken = fresh.AccessToken
	s.held.Expiry = fresh.Expiry
	if fresh.TokenType != "" {
		s.held.TokenType = fresh.TokenType
	}
	if fresh.RefreshToken != "" {
		s.held.RefreshToken = fresh.RefreshToken
	}
	if proven {
		s.held.BeyondTestingWindow = true
	}

	// A failure to write is not a failure to have a token. The command in hand
	// works either way, and the cost is that the next one refreshes again, so
	// this is not worth turning a working send into an error over. It is worth
	// saying, which is what the warning is for.
	if err := s.store.SaveToken(s.profile, s.held); err != nil {
		s.store.warn("could not store the refreshed token for %q, so the next command will refresh again: %v",
			s.profile, err)
	}
}

// Token is what is currently held, for a caller that wants to report on it
// rather than use it.
func (s *Source) Token() *Token { return s.held }

// refreshErr turns a refresh failure into ours.
//
// The same rule as the authorization exchange, for the same two reasons:
// "oauth2: \"invalid_grant\"" explains nothing about the seven-day expiry that
// almost certainly caused it, and oauth2.RetrieveError carries the whole
// response body beside the code. The mapping is shared with exchangeErr rather
// than written twice, because two copies of a rule about what not to print is
// one copy too many.
func refreshErr(err error) error {
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) {
		return exchangeErr(err)
	}

	// Everything else, including the library's own bare error for a token it
	// cannot refresh at all, lands here. Distinguishing that one would mean
	// matching an errors.New by its message, which is the kind of thing that
	// stops working on an upgrade without anybody noticing, and the sentence
	// below is already the right answer for it.
	return output.Errorf("UNAUTHENTICATED", output.ExitAuthRequired,
		"this profile's authorization could not be renewed. Authorize again.")
}
