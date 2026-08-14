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
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/kmoneil/spacebar/internal/output"
)

// sourceAgainst builds a Source whose refresh goes to the fake, holding a token
// that expired an hour ago so that the next request refreshes.
func sourceAgainst(t *testing.T, s *authServer, store *Store, held *Token, now time.Time) *Source {
	t.Helper()

	config := &oauth2.Config{
		ClientID:     "1234.apps.googleusercontent.example",
		ClientSecret: "notARealClientSecret",
		Endpoint: oauth2.Endpoint{
			AuthURL:   AuthEndpoint,
			TokenURL:  s.URL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient,
		&http.Client{Timeout: 5 * time.Second})

	return &Source{
		store:   store,
		profile: "work",
		src: config.TokenSource(ctx, &oauth2.Token{
			AccessToken:  held.AccessToken,
			RefreshToken: held.RefreshToken,
			Expiry:       held.Expiry,
			TokenType:    held.TokenType,
		}),
		held: held,
		now:  func() time.Time { return now },
	}
}

// expired is a token whose access half is already dead, so the next call
// refreshes.
//
// The expiry is against the real clock and the consent time is against the
// injected one, which looks inconsistent and is not. x/oauth2 decides whether
// to refresh by comparing the token's expiry with time.Now(), and there is no
// hook to change that, so a fixture meaning "this needs refreshing" has to be
// expired in real terms. Everything this package decides for itself, which is
// the seven-day arithmetic, uses the injected clock and is therefore fixed
// rather than relative to whenever the suite ran.
func expired(now time.Time, consented time.Duration) *Token {
	return &Token{
		AccessToken:  "ya29.old",
		RefreshToken: "1//old",
		Expiry:       time.Now().Add(-time.Hour),
		TokenType:    "Bearer",
		Scopes:       []string{ScopeMessages},
		ObtainedAt:   now.Add(-consented),
	}
}

// live is the same token with an expiry the library will accept, for the cases
// about not refreshing.
func live(now time.Time, consented time.Duration) *Token {
	token := expired(now, consented)
	token.Expiry = time.Now().Add(time.Hour)
	return token
}

// TestARotatedRefreshTokenIsPersisted is what this type exists for.
//
// x/oauth2 has no hook that fires when a token is refreshed: TokenSource is a
// bare Token() method, and reuseTokenSource holds the new value in memory and
// nowhere else. So without this, a rotated refresh token lives exactly as long
// as the process, the next command starts from the stale one in the keyring and
// is told to authorize again, and that failure arrives a week later looking
// exactly like the seven-day expiry it is not.
func TestARotatedRefreshTokenIsPersisted(t *testing.T) {
	s := newAuthServer(t)
	s.answer(http.StatusOK, `{"access_token":"ya29.new","refresh_token":"1//rotated",`+
		`"token_type":"Bearer","expires_in":3599}`)

	store, _ := memoryStore()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	source := sourceAgainst(t, s, store, expired(now, 2*24*time.Hour), now)

	access, err := source.AccessToken()
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if access != "ya29.new" {
		t.Errorf("access token = %q", access)
	}

	stored, err := store.LoadToken("work")
	if err != nil {
		t.Fatalf("the refreshed token was not stored at all: %v", err)
	}
	if stored.RefreshToken != "1//rotated" {
		t.Errorf("the stored refresh token is %q, so the next process starts from a dead one", stored.RefreshToken)
	}
	if stored.AccessToken != "ya29.new" {
		t.Errorf("the stored access token is %q", stored.AccessToken)
	}
	// The consent time survives a refresh. It is measured from consent and a
	// refresh is not one, so resetting it here would silence the seven-day
	// warning forever by making every token look new.
	if !stored.ObtainedAt.Equal(now.Add(-2 * 24 * time.Hour)) {
		t.Errorf("obtained_at moved on a refresh: %v", stored.ObtainedAt)
	}
}

// TestARefreshThatReturnsNoRefreshTokenKeepsTheOldOne.
//
// Google usually omits it, and x/oauth2 keeps the one it holds when that
// happens. What is asserted here is that the stored copy is not blanked, which
// is the way this could go wrong on our side rather than theirs.
func TestARefreshThatReturnsNoRefreshTokenKeepsTheOldOne(t *testing.T) {
	s := newAuthServer(t)
	s.answer(http.StatusOK, `{"access_token":"ya29.new","token_type":"Bearer","expires_in":3599}`)

	store, _ := memoryStore()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	source := sourceAgainst(t, s, store, expired(now, 2*24*time.Hour), now)

	if _, err := source.AccessToken(); err != nil {
		t.Fatalf("AccessToken: %v", err)
	}

	stored, err := store.LoadToken("work")
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if stored.RefreshToken != "1//old" {
		t.Errorf("the refresh token was blanked by a response that omitted one: %q", stored.RefreshToken)
	}
}

// TestARefreshPastTheBoundaryDisprovesTheTestingLimit.
//
// The self-correcting half of the seven-day answer. A refresh that succeeds
// more than seven days after consent is proof this client is not in testing
// mode, because a testing-mode refresh token would have been revoked. The fact
// is recorded with the token, nobody is asked, and the warning stops.
func TestARefreshPastTheBoundaryDisprovesTheTestingLimit(t *testing.T) {
	s := newAuthServer(t)
	s.answer(http.StatusOK, `{"access_token":"ya29.new","token_type":"Bearer","expires_in":3599}`)

	store, _ := memoryStore()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	source := sourceAgainst(t, s, store, expired(now, 9*24*time.Hour), now)
	if warnings := source.Warnings(); len(warnings) == 0 {
		t.Fatal("a token past the boundary warned about nothing before the refresh")
	}

	if _, err := source.AccessToken(); err != nil {
		t.Fatalf("AccessToken: %v", err)
	}

	stored, err := store.LoadToken("work")
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if !stored.BeyondTestingWindow {
		t.Error("a refresh nine days after consent did not disprove the testing limit")
	}
	if warnings := source.Warnings(); len(warnings) != 0 {
		t.Errorf("the warning survived being disproved: %v", warnings)
	}
}

// TestARefreshInsideTheWindowProvesNothing. Seven days have not passed, so a
// successful refresh says only that the token was valid, which was already
// known.
func TestARefreshInsideTheWindowProvesNothing(t *testing.T) {
	s := newAuthServer(t)
	s.answer(http.StatusOK, `{"access_token":"ya29.new","token_type":"Bearer","expires_in":3599}`)

	store, _ := memoryStore()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	source := sourceAgainst(t, s, store, expired(now, 3*24*time.Hour), now)

	if _, err := source.AccessToken(); err != nil {
		t.Fatalf("AccessToken: %v", err)
	}

	stored, err := store.LoadToken("work")
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if stored.BeyondTestingWindow {
		t.Error("a refresh three days after consent claimed to disprove the seven-day limit")
	}
}

// TestInvalidGrantOnARefreshIsTheExplainedError, sharing the mapping with the
// authorization exchange rather than writing a second copy of a rule about what
// not to print.
func TestInvalidGrantOnARefreshIsTheExplainedError(t *testing.T) {
	s := newAuthServer(t)
	s.answer(http.StatusBadRequest,
		`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)

	store, _ := memoryStore()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	source := sourceAgainst(t, s, store, expired(now, 8*24*time.Hour), now)

	_, err := source.AccessToken()
	if err == nil {
		t.Fatal("a revoked refresh token produced an access token")
	}
	if got := output.ExitCodeOf(err); got != output.ExitAuthRequired {
		t.Errorf("exit code = %d, want %d", got, output.ExitAuthRequired)
	}
	if strings.Contains(err.Error(), "oauth2:") {
		t.Errorf("the raw library error was printed:\n%v", err)
	}
	for _, want := range []string{"expired", "testing mode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not explain the likely cause, missing %q:\n%v", want, err)
		}
	}
}

// TestARefreshFailureNeverPrintsAToken.
//
// oauth2.RetrieveError carries the whole response body, and there is a path in
// the library where a 200 that also names an error returns one. On a token
// endpoint that body is an access token and a refresh token.
func TestARefreshFailureNeverPrintsAToken(t *testing.T) {
	s := newAuthServer(t)
	s.answer(http.StatusOK,
		`{"error":"invalid_scope","access_token":"ya29.leaked","refresh_token":"1//leaked"}`)

	store, _ := memoryStore()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	source := sourceAgainst(t, s, store, expired(now, 2*24*time.Hour), now)

	_, err := source.AccessToken()
	if err == nil {
		t.Fatal("a response naming an error produced an access token")
	}
	for _, secret := range []string{"ya29.leaked", "1//leaked"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("a token reached the failure message:\n%v", err)
		}
	}
}

// TestAValidTokenIsNotRefreshedAndNotRewritten.
//
// The common case is a command inside the access token's hour, and a keyring
// write per command is a keychain prompt per command on macOS.
func TestAValidTokenIsNotRefreshedAndNotRewritten(t *testing.T) {
	s := newAuthServer(t)
	store, _ := memoryStore()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	source := sourceAgainst(t, s, store, live(now, 2*24*time.Hour), now)
	access, err := source.AccessToken()
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if access != "ya29.old" {
		t.Errorf("a valid token was replaced: %q", access)
	}
	if s.sent() != nil {
		t.Error("a valid token was refreshed anyway")
	}
	if _, err := store.LoadToken("work"); err == nil {
		t.Error("a command that refreshed nothing still wrote to the keyring")
	}
}

// TestAuthorizationIsTheHeaderInternalChatAsksFor, which is what makes this a
// drop-in for the transport's Authorizer.
func TestAuthorizationIsTheHeaderInternalChatAsksFor(t *testing.T) {
	s := newAuthServer(t)
	store, _ := memoryStore()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	source := sourceAgainst(t, s, store, live(now, time.Hour), now)
	header, err := source.Authorization(context.Background())
	if err != nil {
		t.Fatalf("Authorization: %v", err)
	}
	if header != "Bearer ya29.old" {
		t.Errorf("Authorization = %q", header)
	}
}

// TestPersistNeverBlanksTheRefreshToken, tested directly rather than through a
// refresh.
//
// Going through the library cannot catch this. x/oauth2 already refuses to
// overwrite a refresh token with an empty value on a refresh, so the guard here
// and the guard there produce the same stored result and a test of the outcome
// passes with ours removed. That was found by removing it.
//
// The guard stays, because the library's behaviour is a dependency's promise
// and this one is two lines, and it gets a test that can actually see it.
func TestPersistNeverBlanksTheRefreshToken(t *testing.T) {
	store, _ := memoryStore()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	source := &Source{
		store:   store,
		profile: "work",
		held:    expired(now, 2*24*time.Hour),
		now:     func() time.Time { return now },
	}

	source.persist(&oauth2.Token{
		AccessToken: "ya29.new",
		Expiry:      now.Add(time.Hour),
		// No refresh token, which is what Google returns on a refresh.
		RefreshToken: "",
	})

	stored, err := store.LoadToken("work")
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if stored.RefreshToken != "1//old" {
		t.Errorf("the refresh token was blanked: %q", stored.RefreshToken)
	}
	if stored.AccessToken != "ya29.new" {
		t.Errorf("the access token was not updated: %q", stored.AccessToken)
	}
}
