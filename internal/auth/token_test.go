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
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
)

// consentedAt is a fixed point, so that every window in this file is arithmetic
// rather than relative to whenever the suite ran.
var consentedAt = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func tokenAged(d time.Duration) *Token {
	return &Token{
		AccessToken:  "ya29.access",
		RefreshToken: "1//refresh",
		Expiry:       consentedAt.Add(d).Add(time.Hour),
		TokenType:    "Bearer",
		Scopes:       []string{ScopeMessages},
		ObtainedAt:   consentedAt,
	}
}

// TestATokenRoundTrips through the store, with every field that matters.
//
// obtained_at is the one worth naming. It is written rather than derived
// because the seven-day death is measured from consent, and losing it on a
// round trip would silence the warning for every profile.
func TestATokenRoundTrips(t *testing.T) {
	store, _ := memoryStore()
	original := tokenAged(0)
	original.BeyondTestingWindow = true

	if err := store.SaveToken("work", original); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	got, err := store.LoadToken("work")
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}

	if got.AccessToken != original.AccessToken || got.RefreshToken != original.RefreshToken {
		t.Errorf("the tokens did not survive: %+v", got)
	}
	if !got.ObtainedAt.Equal(original.ObtainedAt) {
		t.Errorf("obtained_at = %v, want %v", got.ObtainedAt, original.ObtainedAt)
	}
	if !got.BeyondTestingWindow {
		t.Error("the proof that this client is not in testing mode was lost")
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != ScopeMessages {
		t.Errorf("scopes = %v", got.Scopes)
	}
}

// TestAMissingTokenIsExitFour, because the fix is to authorize and a script has
// to tell that apart from a space that does not exist.
func TestAMissingTokenIsExitFour(t *testing.T) {
	store, _ := memoryStore()

	_, err := store.LoadToken("work")
	if err == nil {
		t.Fatal("a profile with no token returned one")
	}
	if got := output.ExitCodeOf(err); got != output.ExitAuthRequired {
		t.Errorf("exit code = %d, want %d", got, output.ExitAuthRequired)
	}
}

// TestAnUnreadableTokenIsNotQuotedBack. What is stored is a token, so a failure
// to parse it must not print it.
func TestAnUnreadableTokenIsNotQuotedBack(t *testing.T) {
	store, _ := memoryStore()
	if err := store.Set(Ref("work", TokenSecret), `{"access_token":"ya29.secret", not json`); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, err := store.LoadToken("work")
	if err == nil {
		t.Fatal("unreadable JSON was accepted as a token")
	}
	if strings.Contains(err.Error(), "ya29.secret") {
		t.Errorf("the failure quotes the token back:\n%v", err)
	}
}

// TestDeleteTokenIsIdempotent, because a logout that had already happened is a
// logout that succeeded.
func TestDeleteTokenIsIdempotent(t *testing.T) {
	store, _ := memoryStore()
	if err := store.SaveToken("work", tokenAged(0)); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	if err := store.DeleteToken("work"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if _, err := store.LoadToken("work"); err == nil {
		t.Error("the token survived a delete")
	}
}

// TestTheWarningWindow is the claim from the card, arithmetically.
//
// The seven-day boundary cannot be known: nothing in the API says whether an
// OAuth client is in testing mode. So the warning is worded as a possibility
// and fires only inside the last day, and this is where that window is fixed.
func TestTheWarningWindow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		age   time.Duration
		warns bool
	}{
		// The card's own two cases.
		{"six days and one hour", 6*24*time.Hour + time.Hour, true},
		{"three days", 3 * 24 * time.Hour, false},

		{"fresh", 0, false},
		{"just outside the window", 6*24*time.Hour - time.Minute, false},
		{"exactly at the window", 6 * 24 * time.Hour, true},
		{"past the boundary", 8 * 24 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warning, needsReauth := Assess(tokenAged(tc.age), consentedAt.Add(tc.age))

			if (warning != "") != tc.warns {
				t.Errorf("at %v the warning is %q, want warns=%v", tc.age, warning, tc.warns)
			}
			// A warning is not a refusal. The token may well be fine, and a
			// command that stopped working because of a guess would be worse
			// than the guess.
			if needsReauth {
				t.Errorf("at %v the warning was treated as needing a re-authorization", tc.age)
			}
			// Both wordings hedge, and they have to: nothing in the API says
			// whether a client is in testing mode, so a warning that stated it
			// as fact would be confidently wrong for everybody on an Internal
			// client. Compared without case because one of them opens a
			// sentence and the other does not.
			if warning != "" && !strings.Contains(strings.ToLower(warning), "if this oauth client is still in testing") {
				t.Errorf("the warning states as fact something that cannot be known:\n%s", warning)
			}
		})
	}
}

// TestTheWarningStopsOnceARefreshDisprovesIt.
//
// This is the answer to the question the card left open. There is no signal
// that says an application is in testing mode, so the warning would otherwise
// fire forever for somebody on an Internal client whose token is fine for a
// year. A refresh that succeeds past the boundary proves the limit does not
// apply, because a testing-mode refresh token would have been dead, and that
// fact is recorded rather than asked about.
func TestTheWarningStopsOnceARefreshDisprovesIt(t *testing.T) {
	token := tokenAged(8 * 24 * time.Hour)
	now := consentedAt.Add(8 * 24 * time.Hour)

	if warning, _ := Assess(token, now); warning == "" {
		t.Fatal("a token past the boundary said nothing")
	}

	token.BeyondTestingWindow = true
	if warning, _ := Assess(token, now); warning != "" {
		t.Errorf("the warning survived being disproved:\n%s", warning)
	}
}

// TestNothingIsClaimedAboutATokenWithNoConsentTime.
//
// A record written before this field existed, or by something else. Nothing is
// known about it, so nothing is said: a warning inferred from a zero time would
// claim every such token was seven days overdue.
func TestNothingIsClaimedAboutATokenWithNoConsentTime(t *testing.T) {
	token := tokenAged(0)
	token.ObtainedAt = time.Time{}

	if warning, _ := Assess(token, consentedAt.Add(30*24*time.Hour)); warning != "" {
		t.Errorf("a token with no consent time was warned about:\n%s", warning)
	}
}

// TestStatusOfReportsWhatACallerCanActOn, per SPEC.md §6.7.
func TestStatusOfReportsWhatACallerCanActOn(t *testing.T) {
	status := StatusOf("work", "useroauth", tokenAged(2*24*time.Hour), consentedAt.Add(2*24*time.Hour))

	if status.Profile != "work" || status.Transport != "useroauth" {
		t.Errorf("status = %+v", status)
	}
	if status.NeedsReauth {
		t.Error("a token two days old was reported as needing a re-authorization")
	}
	if status.DaysRemaining == nil {
		t.Fatal("no days remaining were reported")
	}
	if got := *status.DaysRemaining; got < 4.9 || got > 5.1 {
		t.Errorf("days remaining = %v, want about 5", got)
	}

	// No token at all is a status rather than a failure: "not authorized" is
	// the answer to the question that was asked.
	none := StatusOf("work", "useroauth", nil, consentedAt)
	if !none.NeedsReauth {
		t.Error("a profile with no token was reported as authorized")
	}
	if none.DaysRemaining != nil {
		t.Errorf("days remaining reported for a profile with no token: %v", *none.DaysRemaining)
	}
}

// TestClientCredentialsFollowTheLadder, per SPEC.md §5.2: the environment, then
// the profile, then whatever was linked in.
func TestClientCredentialsFollowTheLadder(t *testing.T) {
	store, _ := memoryStore()
	if err := store.Set(Ref("work", "client-secret"), "fromTheKeyring"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	profile := config.Profile{
		Transport:       config.TransportUserOAuth,
		ClientID:        "fromTheProfile.apps.googleusercontent.example",
		ClientSecretRef: Ref("work", "client-secret"),
	}

	id, secret, err := store.ClientCredentials(profile)
	if err != nil {
		t.Fatalf("ClientCredentials: %v", err)
	}
	if id != profile.ClientID || secret != "fromTheKeyring" {
		t.Errorf("got %q / %q", id, secret)
	}

	// The environment wins over the profile, per field.
	t.Setenv(config.Env("CLIENT_ID"), "fromTheEnvironment.apps.googleusercontent.example")
	id, secret, err = store.ClientCredentials(profile)
	if err != nil {
		t.Fatalf("ClientCredentials: %v", err)
	}
	if id != "fromTheEnvironment.apps.googleusercontent.example" {
		t.Errorf("the environment did not win: %q", id)
	}
	if secret != "fromTheKeyring" {
		t.Errorf("the secret changed when only the ID was overridden: %q", secret)
	}

	// A profile with nothing configured resolves to nothing, which is not an
	// error: a build from source has empty defaults on purpose, and the command
	// that needs a client is the one that can say what to do about it.
	t.Setenv(config.Env("CLIENT_ID"), "")
	id, _, err = store.ClientCredentials(config.Profile{})
	if err != nil || id != "" {
		t.Errorf("an unconfigured profile gave %q, %v", id, err)
	}
}
