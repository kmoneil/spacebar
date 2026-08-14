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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"golang.org/x/oauth2"

	"github.com/kmoneil/spacebar/internal/auth/loopback"
	"github.com/kmoneil/spacebar/internal/output"
)

// The OAuth endpoints, written out rather than imported.
//
// golang.org/x/oauth2/google is forbidden by SPEC.md §3.2 because it drags in
// cloud.google.com/go/compute/metadata for Application Default Credentials this
// tool never uses.
//
// The authorization endpoint is the v2 path, which is what Google's own
// discovery document at accounts.google.com/.well-known/openid-configuration
// publishes. SPEC.md §6.3 names the pre-v2 path; it still answers, but a
// constant that disagrees with the published one is a constant that will be
// wrong later rather than now, and the discovery document is the authority.
const (
	AuthEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	TokenEndpoint = "https://oauth2.googleapis.com/token"

	// RevokeEndpoint is what a logout that actually revokes would use. Nothing
	// calls it yet; it is here because it came from the same discovery document
	// and looking it up twice is how the three constants drift apart.
	RevokeEndpoint = "https://oauth2.googleapis.com/revoke"
)

// The scopes from SPEC.md §6.4, narrowest first.
//
// A blanket scope is not requested. The reason is practical rather than
// tasteful: a narrower scope materially improves the odds that an administrator
// approves the application at all, and for the population this tool is built
// for that is the difference between working and not.
const (
	ScopeSendOnly  = "https://www.googleapis.com/auth/chat.messages.create"
	ScopeReadOnly  = "https://www.googleapis.com/auth/chat.messages.readonly"
	ScopeMessages  = "https://www.googleapis.com/auth/chat.messages"
	ScopeSpacesRO  = "https://www.googleapis.com/auth/chat.spaces.readonly"
	ScopeSpaces    = "https://www.googleapis.com/auth/chat.spaces"
	ScopeReactions = "https://www.googleapis.com/auth/chat.messages.reactions.create"
	ScopeMembers   = "https://www.googleapis.com/auth/chat.memberships.readonly"
)

// DefaultFlowTimeout bounds how long the flow waits for a person.
//
// It is not --timeout and neither bounds the other, which is worth stating
// because they are both durations in the same command. --timeout is the budget
// for one HTTP attempt, measured in seconds because a request that takes longer
// has failed. This is the budget for somebody to find the browser window, read
// a consent screen, and decide, measured in minutes because that is how long
// reading takes. A flow that inherited --timeout would give them thirty
// seconds.
const DefaultFlowTimeout = 180 * time.Second

// verifierBytes is the length of the PKCE code verifier before encoding.
//
// RFC 7636 §4.1 permits 43 to 128 characters after encoding; 64 random bytes
// encode to 86, comfortably inside that, and SPEC.md §6.3 asks for 64.
const verifierBytes = 64

// stateBytes is the length of the state value before encoding (SPEC.md §6.3).
const stateBytes = 32

// Reporter receives what the flow has to tell a person while it runs.
//
// An interface rather than a writer, because only internal/output may name a
// process stream. It is the same method set as the logger internal/chat takes,
// so one renderer satisfies both, and this package does not have to import the
// one that would close a cycle.
type Reporter interface {
	Logf(format string, a ...any)
}

// Flow is one authorization attempt.
type Flow struct {
	ClientID     string
	ClientSecret string

	// Scopes is the narrow set this profile is asking for.
	Scopes []string

	// Timeout bounds the whole flow. Zero means DefaultFlowTimeout.
	Timeout time.Duration

	// Report receives the consent URL when the browser could not be opened,
	// and nothing else. Nil is allowed and means a flow nobody is watching,
	// which is a flow that will time out if the browser does not work.
	Report Reporter

	// HTTPClient is the client the token exchange goes out through, and it is
	// an any rather than an *http.Client on purpose.
	//
	// The type belongs to net/http, which internal/lint permits only in
	// internal/chat, and this package cannot import that one without closing a
	// cycle: internal/chat already imports this one for URL redaction. So the
	// caller, which imports both, builds it with chat.TokenHTTPClient and
	// passes it through here into the context x/oauth2 reads it from.
	//
	// Nil means x/oauth2 uses http.DefaultClient, which follows redirects. A
	// 3xx on a token request resends the POST, and the POST body is the client
	// secret and the authorization code.
	HTTPClient any

	// Browser opens a URL. Nil means the real one. A test supplies its own
	// rather than launching whatever this machine considers a browser.
	Browser func(url string) error

	// randRead and tokenURL are injected so that the tests can be arithmetic
	// rather than statistical, and can exchange against a server that is not
	// Google. Unexported, so nothing outside this package can point the
	// exchange somewhere else: the endpoint is a constant for a reason.
	randRead func([]byte) (int, error)
	tokenURL string
}

// Token is what a completed flow produces.
type Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	TokenType    string
	Scopes       []string

	// ObtainedAt is when consent was given, and it is stored rather than
	// derived because SPEC.md §6.7 needs it: an External application still in
	// Testing has refresh tokens that die seven days after consent, and the
	// expiry of the access token says nothing about that.
	ObtainedAt time.Time
}

// Login runs the authorization code flow with PKCE and a loopback redirect.
//
// The order matters and is not the obvious one. The listener starts before the
// URL is built, because the redirect URI has to name the port the kernel
// chose, and a flow that picked a port first would be a flow that fails when
// something else has it.
func (f *Flow) Login(ctx context.Context) (*Token, error) {
	if f.ClientID == "" {
		return nil, output.Errorf("NO_CLIENT", output.ExitUsage,
			"no OAuth client is configured, so there is nothing to authorize against.")
	}

	verifier, err := f.random(verifierBytes)
	if err != nil {
		return nil, err
	}
	state, err := f.random(stateBytes)
	if err != nil {
		return nil, err
	}

	server, err := loopback.Listen(state)
	if err != nil {
		return nil, output.Errorf("LOOPBACK", output.ExitError, "%v", err)
	}
	defer func() { _ = server.Close() }()

	config := f.config(server.RedirectURL())
	f.open(config.AuthCodeURL(state, authParams(verifier)...))

	ctx, cancel := context.WithTimeout(ctx, f.timeout())
	defer cancel()

	result, err := server.Wait(ctx)
	if err != nil {
		return nil, waitErr(err)
	}
	if result.Err != nil {
		return nil, output.Errorf("CONSENT", output.ExitAuthRequired, "%v", result.Err)
	}

	return f.exchange(ctx, config, result.Code, verifier)
}

// config is the x/oauth2 configuration for this flow.
func (f *Flow) config(redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     f.ClientID,
		ClientSecret: f.ClientSecret,
		Scopes:       f.Scopes,
		RedirectURL:  redirectURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:  AuthEndpoint,
			TokenURL: f.tokenEndpoint(),

			// The secret goes in the body rather than in a Basic header.
			// Google accepts both; naming it removes a round of autodetection
			// that would otherwise send an unauthenticated probe first.
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// authParams are the options on the authorization URL.
func authParams(verifier string) []oauth2.AuthCodeOption {
	return []oauth2.AuthCodeOption{
		// Without offline access there is no refresh token, and every command
		// would need a browser.
		oauth2.AccessTypeOffline,

		// And without this, a second authorization for an account that has
		// already consented returns no refresh token at all: Google issues one
		// on first consent only. The failure is silent and arrives a day later
		// when the access token expires.
		oauth2.ApprovalForce,

		oauth2.S256ChallengeOption(verifier),
	}
}

// exchange trades the authorization code for a token.
func (f *Flow) exchange(ctx context.Context, config *oauth2.Config, code, verifier string) (*Token, error) {
	if f.HTTPClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, f.HTTPClient)
	}

	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, exchangeErr(err)
	}

	return &Token{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		Expiry:       token.Expiry,
		TokenType:    token.TokenType,
		Scopes:       config.Scopes,
		ObtainedAt:   time.Now(),
	}, nil
}

// open launches a browser, and carries on when it cannot.
//
// SPEC.md §6.5 is explicit that a launch failure never fails the flow. The
// machines where this matters are the ones this tool is built for: a container,
// a CI runner, an SSH session. On this development box there is no xdg-open at
// all, so the failure is exec.ErrNotFound rather than a non-zero exit, and both
// end here.
func (f *Flow) open(url string) {
	browser := f.Browser
	if browser == nil {
		browser = OpenBrowser
	}

	if err := browser(url); err == nil {
		f.report("Opened a browser to authorize. If nothing happened, go to:\n%s", url)
		return
	}
	f.report("Could not open a browser. Go to this URL to authorize:\n%s", url)
}

func (f *Flow) report(format string, a ...any) {
	if f.Report == nil {
		return
	}
	f.Report.Logf(format, a...)
}

func (f *Flow) tokenEndpoint() string {
	if f.tokenURL != "" {
		return f.tokenURL
	}
	return TokenEndpoint
}

func (f *Flow) timeout() time.Duration {
	if f.Timeout > 0 {
		return f.Timeout
	}
	return DefaultFlowTimeout
}

// random returns n bytes of randomness, base64 RawURL encoded.
//
// RawURLEncoding because both the verifier and the state travel in a query
// string, and padding would have to be escaped there.
func (f *Flow) random(n int) (string, error) {
	read := f.randRead
	if read == nil {
		read = rand.Read
	}

	buf := make([]byte, n)
	if _, err := read(buf); err != nil {
		// crypto/rand failing is the machine having no entropy source, which
		// is not something to carry on through: every value this flow depends
		// on for its security comes from here.
		return "", output.Errorf("RANDOM", output.ExitError,
			"cannot read random bytes, so this flow cannot be made secure: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Challenge is the S256 code challenge for a verifier (RFC 7636 §4.2).
//
// Exported so that a test can compute it independently and compare, which is
// the difference between checking the arithmetic and checking that the same
// function agrees with itself.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func waitErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return output.Errorf("TIMEOUT", output.ExitAuthRequired,
			"no authorization arrived within the time allowed.\n"+
				"Nothing was changed. Run the command again when the browser is ready.")
	}
	return output.Errorf("CANCELLED", output.ExitAuthRequired,
		"authorization was cancelled before it finished.")
}

// exchangeErr turns an x/oauth2 failure into ours, and this is the one place
// that has to be careful about it.
//
// SPEC.md §6.7 says never print the raw OAuth error, and there are two reasons
// rather than one. The first is that "oauth2: \"invalid_grant\"" tells somebody
// nothing about the seven-day expiry that almost certainly caused it. The
// second is what the library carries: oauth2.RetrieveError holds the whole
// response body and the http.Response beside it, and while its Error method is
// careful to print only the error code when there is one, anything that
// formatted the value with %+v would print the body. On a token response that
// body is an access token and a refresh token.
//
// So the value is read for its code and then dropped, and nothing downstream is
// handed the error itself.
func exchangeErr(err error) error {
	var retrieve *oauth2.RetrieveError
	if !errors.As(err, &retrieve) {
		return output.Errorf("EXCHANGE", output.ExitAPI,
			"the authorization could not be completed: %v", err)
	}

	if retrieve.ErrorCode == "invalid_grant" {
		return output.Errorf("UNAUTHENTICATED", output.ExitAuthRequired,
			"your authorization has expired.\n"+
				"This is normal for an application in testing mode, whose refresh tokens die seven days "+
				"after consent. Authorize again to continue.")
	}

	code := retrieve.ErrorCode
	if code == "" {
		code = "the authorization server refused without saying why"
	}
	return output.Errorf("EXCHANGE", output.ExitAuthRequired,
		"the authorization could not be completed: %s", code)
}
