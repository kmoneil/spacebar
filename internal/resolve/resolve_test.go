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

package resolve

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
)

// fake is a Reader that answers from a slice and counts what it was asked.
//
// Counting matters more than the answers here. Half of what this package
// promises is about what it does not do: an exact resource name makes no call,
// an alias makes no call, and a fresh cache makes no call. None of that is
// visible in a return value.
type fake struct {
	spaces []chat.Space
	dm     *chat.Space
	dmErr  error
	kind   config.Transport
	caps   *transport.Capabilities

	listed int
	looked int
}

func (f *fake) Kind() config.Transport { return f.kind }
func (f *fake) Profile() string        { return "work" }

func (f *fake) Capabilities() transport.Capabilities {
	if f.caps != nil {
		return *f.caps
	}
	return transport.ScopedCapabilities(f.kind, []string{
		"https://www.googleapis.com/auth/chat.messages",
		"https://www.googleapis.com/auth/chat.spaces.readonly",
		"https://www.googleapis.com/auth/chat.memberships.readonly",
	})
}

func (f *fake) Spaces(context.Context, chat.ListSpacesRequest) iter.Seq2[chat.Space, error] {
	f.listed++
	return func(yield func(chat.Space, error) bool) {
		for _, space := range f.spaces {
			if !yield(space, nil) {
				return
			}
		}
	}
}

func (f *fake) FindDirectMessage(context.Context, string) (*chat.Space, error) {
	f.looked++
	return f.dm, f.dmErr
}

func reader(spaces ...chat.Space) *fake {
	return &fake{spaces: spaces, kind: config.TransportUserOAuth}
}

// fixedNow is the clock the cache tests run on, so that a TTL boundary is a
// property of the test rather than of when it ran.
var fixedNow = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

var (
	ops    = chat.Space{Name: "spaces/AAA", DisplayName: "Ops", SpaceType: "SPACE"}
	opsEsc = chat.Space{Name: "spaces/BBB", DisplayName: "Ops Escalation", SpaceType: "SPACE"}
	dm     = chat.Space{Name: "spaces/CCC", SpaceType: "DIRECT_MESSAGE"}
)

// TestAResourceNameIsNotLookedUp.
//
// The first step exists to be free. Somebody who typed the exact name, or a
// script that stored one, must not pay a request against a per-space quota
// shared with every other app in that space.
func TestAResourceNameIsNotLookedUp(t *testing.T) {
	r := reader(ops)

	got, err := Resolve(context.Background(), r, "spaces/AAA", Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "spaces/AAA" {
		t.Errorf("resolved to %q", got)
	}
	if r.listed != 0 || r.looked != 0 {
		t.Errorf("a literal resource name cost %d lists and %d lookups", r.listed, r.looked)
	}
}

// TestAnEmptyTargetStaysEmpty, because a command with an optional target uses
// it to mean "the profile's own space", and a webhook has one. Turning it into
// a lookup would break `send 'text'` on the transport that needs it most.
func TestAnEmptyTargetStaysEmpty(t *testing.T) {
	r := reader(ops)

	got, err := Resolve(context.Background(), r, "", Options{})
	if err != nil || got != "" {
		t.Fatalf("Resolve(\"\") = %q, %v", got, err)
	}
	if r.listed != 0 {
		t.Errorf("an empty target listed spaces")
	}
}

// TestAnAliasNeedsNoCapabilityAndNoNetwork.
//
// The step the card did not say works on a webhook. An alias is a local map, so
// there is nothing to refuse: a profile that cannot read can still be told that
// "alerts" means the space its own URL points at.
func TestAnAliasNeedsNoCapabilityAndNoNetwork(t *testing.T) {
	r := &fake{kind: config.TransportWebhook}

	got, err := Resolve(context.Background(), r, "alerts", Options{
		Aliases: map[string]string{"alerts": "spaces/AAA"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "spaces/AAA" {
		t.Errorf("resolved to %q", got)
	}
	if r.listed != 0 || r.looked != 0 {
		t.Errorf("an alias cost %d lists and %d lookups", r.listed, r.looked)
	}
}

// TestAnAliasIsCheckedRatherThanTrusted.
//
// config.json is a file somebody may have been sent, and this value is on its
// way into a request path. The refusal names the alias as well as the value,
// because what has to be fixed is the entry and not what was typed.
func TestAnAliasIsCheckedRatherThanTrusted(t *testing.T) {
	r := reader(ops)

	_, err := Resolve(context.Background(), r, "evil", Options{
		Aliases: map[string]string{"evil": "spaces/../../admin"},
	})
	if err == nil {
		t.Fatal("an alias pointing outside the space namespace was accepted")
	}
	if got := output.ExitCodeOf(err); got != output.ExitUsage {
		t.Errorf("exit = %d, want %d", got, output.ExitUsage)
	}
	if !strings.Contains(err.Error(), "evil") {
		t.Errorf("the refusal does not name the alias:\n%v", err)
	}
	if r.listed != 0 {
		t.Error("a bad alias fell through to the display-name matcher")
	}
}

// TestAnExactNameBeatsASubstringOfALongerOne.
//
// The refinement the card did not have, and the reason the exact pass exists.
// With "Ops" and "Ops Escalation", a substring-only rule makes "Ops" ambiguous
// forever, so the space whose name it exactly is becomes the one space you
// cannot reach by name.
func TestAnExactNameBeatsASubstringOfALongerOne(t *testing.T) {
	got, err := Resolve(context.Background(), reader(ops, opsEsc), "Ops", Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "spaces/AAA" {
		t.Errorf("resolved to %q, want the space actually called Ops", got)
	}
}

// TestMatchingIgnoresCaseAndSurroundingSpace, which is the rule somebody has to
// be able to learn: it matches if the name contains what you typed.
func TestMatchingIgnoresCaseAndSurroundingSpace(t *testing.T) {
	for _, target := range []string{"ops", "OPS", "  Ops  ", "escalation", "ops esc"} {
		t.Run(target, func(t *testing.T) {
			got, err := Resolve(context.Background(), reader(ops, opsEsc), target, Options{})
			if err != nil {
				t.Fatalf("Resolve(%q): %v", target, err)
			}
			if got == "" {
				t.Errorf("Resolve(%q) found nothing", target)
			}
		})
	}
}

// TestTwoMatchesAreListedAndRefused. SPEC.md §10.1: never guess. Two spaces
// matching is a question only the person who typed it can answer, and the cost
// of answering it wrongly is a message in front of people who should not see it.
func TestTwoMatchesAreListedAndRefused(t *testing.T) {
	_, err := Resolve(context.Background(), reader(ops, opsEsc), "op", Options{})
	if err == nil {
		t.Fatal("an ambiguous target was resolved")
	}
	if got := output.ExitCodeOf(err); got != output.ExitUsage {
		t.Errorf("exit = %d, want %d", got, output.ExitUsage)
	}
	for _, want := range []string{"spaces/AAA", "spaces/BBB", "Ops", "Ops Escalation"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not list %q:\n%v", want, err)
		}
	}
}

// TestAnUnnamedSpaceIsNeverAMatch.
//
// A direct message has no display name. Folding it in would make every unnamed
// space a candidate the moment anything matched the empty string, which is the
// kind of bug that turns "never guess" into "guessed once".
func TestAnUnnamedSpaceIsNeverAMatch(t *testing.T) {
	_, err := Resolve(context.Background(), reader(dm), "anything", Options{})
	if err == nil {
		t.Fatal("a space with no display name was matched")
	}
	if !strings.Contains(err.Error(), "nothing this profile can reach") {
		t.Errorf("the refusal is not the not-found one:\n%v", err)
	}
}

// TestAWebhookRefusesTheStepsThatNeedToRead, at exit 5, before any call. The
// two cheap steps still work on one; the two that read do not.
func TestAWebhookRefusesTheStepsThatNeedToRead(t *testing.T) {
	for _, target := range []string{"Ops", "someone@example.test"} {
		t.Run(target, func(t *testing.T) {
			r := &fake{kind: config.TransportWebhook, spaces: []chat.Space{ops}}

			_, err := Resolve(context.Background(), r, target, Options{})
			if err == nil {
				t.Fatal("a webhook resolved a name it cannot look up")
			}
			if got := output.ExitCodeOf(err); got != output.ExitUnsupported {
				t.Errorf("exit = %d, want %d\n%v", got, output.ExitUnsupported, err)
			}
			if r.listed != 0 || r.looked != 0 {
				t.Errorf("the refusal made %d lists and %d lookups", r.listed, r.looked)
			}
		})
	}
}

// TestAnAddressGoesToTheDirectMessageLookup, and never to the name matcher. A
// mistyped address falling through would either find nothing or, worse, find
// something.
func TestAnAddressGoesToTheDirectMessageLookup(t *testing.T) {
	r := reader(ops)
	r.dm = &chat.Space{Name: "spaces/CCC"}

	got, err := Resolve(context.Background(), r, "someone@example.test", Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "spaces/CCC" {
		t.Errorf("resolved to %q", got)
	}
	if r.looked != 1 {
		t.Errorf("made %d direct message lookups, want 1", r.looked)
	}
	if r.listed != 0 {
		t.Error("an address also listed spaces, so a typo could match a display name")
	}
}

// TestA404AndA400ReadDifferently.
//
// The m4-01 recon measured the distinction and it is not what the reference
// suggests: a user the directory cannot resolve is 400, and a real person with
// no direct message is 404. Both are exit 3, so the sentence is the only thing
// that tells somebody whether to check their spelling.
func TestA404AndA400ReadDifferently(t *testing.T) {
	notFound := reader(ops)
	notFound.dmErr = &output.Error{Code: "NOT_FOUND", Exit: output.ExitAPI, Message: "x", Err: chat.ErrNotFound}

	_, err := Resolve(context.Background(), notFound, "real@example.test", Options{})
	if err == nil || !strings.Contains(err.Error(), "no direct message") {
		t.Errorf("a 404 does not say the conversation has not started:\n%v", err)
	}

	bad := reader(ops)
	bad.dmErr = &output.Error{Code: "API_ERROR", Exit: output.ExitAPI, Message: "x", Err: chat.ErrInvalidRequest}

	_, err = Resolve(context.Background(), bad, "ghost@example.test", Options{})
	if err == nil || !strings.Contains(err.Error(), "not a user this account can look up") {
		t.Errorf("a 400 does not say the address could not be resolved:\n%v", err)
	}
}

// TestEverythingItReturnsIsASpaceName is the (M4) marker this card owns, stated
// over every path rather than asserted once.
//
// The check is on the way out, not the way in, because resolution is where a
// value changes: an alias came from a file, and a display name match came from
// the API. Both are values from elsewhere heading for a request path.
func TestEverythingItReturnsIsASpaceName(t *testing.T) {
	hostile := chat.Space{Name: "spaces/../../etc", DisplayName: "Ops"}

	for _, tc := range []struct {
		name  string
		setup func() (*fake, string, Options)
	}{
		{"a display name match", func() (*fake, string, Options) {
			return reader(hostile), "Ops", Options{}
		}},
		{"an alias", func() (*fake, string, Options) {
			return reader(ops), "a", Options{Aliases: map[string]string{"a": "spaces/../../etc"}}
		}},
		{"a direct message", func() (*fake, string, Options) {
			r := reader(ops)
			r.dm = &chat.Space{Name: "spaces/../../etc"}
			return r, "someone@example.test", Options{}
		}},
		{"a literal", func() (*fake, string, Options) {
			return reader(ops), "spaces/../../etc", Options{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, target, opts := tc.setup()

			got, err := Resolve(context.Background(), r, target, opts)
			if err == nil {
				t.Fatalf("resolved to %q, which is not a space name", got)
			}
			if got != "" {
				t.Errorf("a refusal also returned %q", got)
			}
		})
	}
}

// TestAnAddressThatWouldChangeARequestIsRefusedBeforeTheLookup.
//
// The address becomes a query value on a request path. The encoder would escape
// it, and that is the second layer rather than the only one.
func TestAnAddressThatWouldChangeARequestIsRefusedBeforeTheLookup(t *testing.T) {
	for _, address := range []string{
		"a/b@example.test",
		"a%2f@example.test",
		"@example.test",
		"someone@",
		"someone@@example.test",
	} {
		t.Run(address, func(t *testing.T) {
			r := reader(ops)

			if _, err := Resolve(context.Background(), r, address, Options{}); err == nil {
				t.Errorf("%q was accepted as an address", address)
			}
			if r.looked != 0 {
				t.Errorf("%q reached the network", address)
			}
		})
	}
}

// TestTheCacheIsUsedAndRefreshBustsIt, which is what keeps a resolver off a
// shared per-space quota.
func TestTheCacheIsUsedAndRefreshBustsIt(t *testing.T) {
	cache := &Cache{path: t.TempDir() + "/spaces-work.json", now: func() time.Time { return fixedNow }}
	r := reader(ops)

	for range 3 {
		if _, err := Resolve(context.Background(), r, "Ops", Options{Cache: cache}); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}
	if r.listed != 1 {
		t.Errorf("three resolutions listed spaces %d times, want 1", r.listed)
	}

	if _, err := Resolve(context.Background(), r, "Ops", Options{Cache: cache, Refresh: true}); err != nil {
		t.Fatalf("Resolve with --refresh: %v", err)
	}
	if r.listed != 2 {
		t.Errorf("--refresh listed spaces %d times in total, want 2", r.listed)
	}
}

// TestAnUnwritableCacheIsNotAFailure. A read-only home is a reason to make one
// extra request, not a reason to be unable to find a space.
func TestAnUnwritableCacheIsNotAFailure(t *testing.T) {
	cache := &Cache{path: "/nonexistent-directory-for-a-test/spaces-work.json", now: func() time.Time { return fixedNow }}

	got, err := Resolve(context.Background(), reader(ops), "Ops", Options{Cache: cache})
	if err != nil {
		t.Fatalf("an unwritable cache broke resolution: %v", err)
	}
	if got != "spaces/AAA" {
		t.Errorf("resolved to %q", got)
	}
}

// TestANilCacheResolvesEveryTime, which is what NewCache returns when the cache
// directory cannot even be located.
func TestANilCacheResolvesEveryTime(t *testing.T) {
	r := reader(ops)

	for range 2 {
		if _, err := Resolve(context.Background(), r, "Ops", Options{Cache: nil}); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}
	if r.listed != 2 {
		t.Errorf("listed %d times with no cache, want 2", r.listed)
	}
}

// TestTheRefusalsCarryTheRightSentinel, so that a caller can branch on the kind
// of failure rather than on its wording.
func TestTheRefusalsCarryTheRightSentinel(t *testing.T) {
	r := &fake{kind: config.TransportWebhook}

	_, err := Resolve(context.Background(), r, "Ops", Options{})
	if !errors.Is(err, transport.ErrUnsupported) {
		t.Errorf("a capability refusal is not ErrUnsupported:\n%v", err)
	}
}

// FuzzWhateverItReturnsIsASpaceName is the (M4) marker stated as a property.
//
// The same shape as m3-05's targets, and for the same reason: a list of cases
// is what somebody thought of, and this package's whole job is to take a string
// from a person, a file, or the API and hand back something safe to put in a
// request path. What has to be true for every input there is is that the result
// either fails or passes chat.CheckSpaceName.
//
// The API's own answers are fuzzed too, not just the target. A display name and
// a direct message space both come back from a server this tool does not
// control, and SECURITY.md assumes a hostile space can put arbitrary bytes in
// anything it names.
func FuzzWhateverItReturnsIsASpaceName(f *testing.F) {
	for _, seed := range []string{
		"spaces/AAA", "", "Ops", "ops", "someone@example.test", "alerts",
		"spaces/../../etc", "spaces/AAA?key=x", "..", "@", "a@b@c",
		"users/1", "spaces/", "  ", "Ops Escalation", "%2f",
	} {
		f.Add(seed, "Ops", "spaces/BBB")
	}

	f.Fuzz(func(t *testing.T, target, displayName, apiName string) {
		r := &fake{
			kind:   config.TransportUserOAuth,
			spaces: []chat.Space{{Name: apiName, DisplayName: displayName}},
			dm:     &chat.Space{Name: apiName},
		}

		got, err := Resolve(context.Background(), r, target, Options{
			Aliases: map[string]string{"alerts": apiName},
		})
		if err != nil {
			if got != "" {
				t.Fatalf("Resolve(%q) failed and still returned %q", target, got)
			}
			return
		}

		// The empty target is the documented pass-through: a command with an
		// optional target uses it to mean the profile's own space.
		if got == "" {
			if target != "" {
				t.Fatalf("Resolve(%q) succeeded and returned nothing", target)
			}
			return
		}

		if err := chat.CheckSpaceName(got); err != nil {
			t.Fatalf("Resolve(%q) returned %q, which is not a space name: %v", target, got, err)
		}
	})
}

// The four tests below are one rule looked at from four sides: a profile name
// is never a space, and saying so is the whole of what Options.Profiles does.
//
// What was there before was true and useless. `send live "test"` with a webhook
// profile called `live` answered "resolving live needs the ability to list
// spaces, and profile live is an incoming webhook", followed by instructions
// for creating an OAuth client in a Cloud project. Every word of that is
// correct. None of it is the problem, which is that a profile was typed where a
// space goes, and the fix is four characters of flag.

// TestAProfileNameInTheSpaceSlotSaysSoRatherThanNamingTheTransport, on the
// transport where the old answer was worst.
//
// A webhook cannot list spaces at all, so this lookup was never going to
// succeed and nothing is given up by answering the likelier question. Exit 2
// rather than 5, because what has to change is the command line.
func TestAProfileNameInTheSpaceSlotSaysSoRatherThanNamingTheTransport(t *testing.T) {
	r := &fake{kind: config.TransportWebhook, spaces: []chat.Space{ops}}

	_, err := Resolve(context.Background(), r, "live", Options{Profiles: []string{"live", "work"}})
	if err == nil {
		t.Fatal("a profile name resolved to a space")
	}
	if got := output.ExitCodeOf(err); got != output.ExitUsage {
		t.Errorf("exit = %d, want %d\n%v", got, output.ExitUsage, err)
	}
	for _, want := range []string{`"live" is a profile`, "--profile live"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}

	// The old answer, and the reason this test exists. Sending somebody to
	// create an OAuth client to fix an argument in the wrong position is a long
	// walk to the wrong place.
	for _, unwanted := range []string{"incoming webhook", "auth setup"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("the refusal still talks about %q:\n%v", unwanted, err)
		}
	}
	if r.listed != 0 || r.looked != 0 {
		t.Errorf("the refusal made %d lists and %d lookups", r.listed, r.looked)
	}
}

// TestAProfileNameThatMatchesNoSpaceIsExplainedAfterTheLookup.
//
// The list is consulted first and the count is asserted, because that ordering
// is the whole safety argument: the profile is only ever offered as an
// explanation for a target that did not resolve. A check that ran before the
// match would be the ambiguity this refuses to introduce, arriving through the
// back door.
func TestAProfileNameThatMatchesNoSpaceIsExplainedAfterTheLookup(t *testing.T) {
	r := reader(ops, opsEsc)

	_, err := Resolve(context.Background(), r, "live", Options{Profiles: []string{"live"}})
	if err == nil {
		t.Fatal("a profile name resolved to a space")
	}
	if got := output.ExitCodeOf(err); got != output.ExitUsage {
		t.Errorf("exit = %d, want %d\n%v", got, output.ExitUsage, err)
	}
	if r.listed != 1 {
		t.Errorf("the spaces were listed %d times, want 1: the profile is the explanation for a "+
			"failed lookup and not a substitute for making one", r.listed)
	}

	// Both halves. It says what was wrong, and it still points at the command
	// that answers "then what is there", because a profile and a space may
	// genuinely share a word and the reader may have meant either.
	for _, want := range []string{`"live" is a profile`, "--profile live", "spaces list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// TestASpaceReallyCalledThatBeatsAProfileOfTheSameName is the invariant that
// makes the rest of this safe.
//
// Two namespaces can share a word without either becoming unreachable, but only
// because resolution is unchanged: the space wins, every time, and the profile
// is never consulted while an answer is still possible. An alias is checked as
// well, because it resolves two steps earlier and a hint that had moved ahead
// of resolution would take that one too.
func TestASpaceReallyCalledThatBeatsAProfileOfTheSameName(t *testing.T) {
	t.Run("display name", func(t *testing.T) {
		got, err := Resolve(context.Background(), reader(ops, opsEsc), "Ops", Options{
			Profiles: []string{"Ops"},
		})
		if err != nil {
			t.Fatalf("a space called Ops stopped resolving because a profile shares its name: %v", err)
		}
		if got != "spaces/AAA" {
			t.Errorf("resolved to %q, want the space actually called Ops", got)
		}
	})

	t.Run("alias", func(t *testing.T) {
		r := &fake{kind: config.TransportWebhook}
		got, err := Resolve(context.Background(), r, "live", Options{
			Aliases:  map[string]string{"live": "spaces/CCC"},
			Profiles: []string{"live"},
		})
		if err != nil {
			t.Fatalf("an alias stopped resolving because a profile shares its name: %v", err)
		}
		if got != "spaces/CCC" {
			t.Errorf("resolved to %q, want the alias target", got)
		}
	})
}

// TestTheSuggestedProfileIsSpelledTheWayItIsConfigured.
//
// The match is case-folded, like every other match in this package, but the
// name in the message is the configured one. --profile is exact, so telling
// somebody to run `--profile LIVE` for a profile called `live` would be a
// second dead end handed out by the refusal for the first.
func TestTheSuggestedProfileIsSpelledTheWayItIsConfigured(t *testing.T) {
	r := &fake{kind: config.TransportWebhook}

	_, err := Resolve(context.Background(), r, "  LIVE  ", Options{Profiles: []string{"live"}})
	if err == nil {
		t.Fatal("a profile name resolved to a space")
	}
	if !strings.Contains(err.Error(), "--profile live") {
		t.Errorf("the refusal does not suggest the configured spelling:\n%v", err)
	}
	if strings.Contains(err.Error(), "--profile LIVE") {
		t.Errorf("the refusal suggests a spelling --profile will not accept:\n%v", err)
	}
}
