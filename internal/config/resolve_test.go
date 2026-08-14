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

package config

import (
	"strings"
	"testing"

	"github.com/kmoneil/spacebar/internal/output"
)

func twoProfileConfig() *Config {
	return &Config{
		DefaultProfile: "alerts",
		Profiles: map[string]Profile{
			"alerts": {Transport: TransportWebhook},
			"work":   {Transport: TransportUserOAuth},
		},
		path: "/nowhere/config.json",
	}
}

// TestActiveResolutionOrder is SPEC.md §5.2, every rung, including the case
// where a lower rung is set and a higher one has to win anyway.
func TestActiveResolutionOrder(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		env     string
		dflt    string
		want    string
		wantErr bool
	}{
		{name: "the flag wins over everything", flag: "work", env: "alerts", dflt: "alerts", want: "work"},
		{name: "the environment wins over the default", env: "work", dflt: "alerts", want: "work"},
		{name: "the default is the last rung", dflt: "work", want: "work"},
		{name: "an empty flag does not count as a choice", flag: "", env: "work", dflt: "alerts", want: "work"},
		{name: "an empty environment variable does not count as a choice", env: "", dflt: "alerts", want: "alerts"},
		{name: "nothing anywhere is a failure", wantErr: true},
		{name: "a flag naming no profile is a failure", flag: "absent", dflt: "alerts", wantErr: true},
		{name: "an environment naming no profile is a failure", env: "absent", dflt: "alerts", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvProfile(), tc.env)

			cfg := twoProfileConfig()
			cfg.DefaultProfile = tc.dflt

			name, profile, err := cfg.Active(tc.flag)
			if tc.wantErr {
				wantExit(t, err, output.ExitUsage)
				return
			}
			if err != nil {
				t.Fatalf("resolving: %v", err)
			}
			if name != tc.want {
				t.Errorf("resolved %q, want %q", name, tc.want)
			}
			if profile.Transport != cfg.Profiles[tc.want].Transport {
				t.Errorf("resolved the name %q but the wrong profile: %+v", name, profile)
			}
		})
	}
}

// TestActiveNamesWhereTheNameCameFrom is the difference between somebody
// editing their config file and somebody finding the environment variable they
// exported in another window three weeks ago.
func TestActiveNamesWhereTheNameCameFrom(t *testing.T) {
	t.Setenv(EnvProfile(), "absent")

	cfg := twoProfileConfig()
	_, _, err := cfg.Active("")
	wantExit(t, err, output.ExitUsage)

	for _, want := range []string{"absent", EnvProfile(), "alerts, work", cfg.path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not contain %q:\n%v", want, err)
		}
	}
}

// TestActiveWithNoProfilesAtAll is the first run. The message has to be about
// there being nothing configured, and it must not name a command that does not
// exist yet: an example that does not work reads as the tool being broken, by
// exactly the person who cannot tell the difference.
func TestActiveWithNoProfilesAtAll(t *testing.T) {
	t.Setenv(EnvProfile(), "")

	cfg := &Config{path: "/nowhere/config.json"}
	_, _, err := cfg.Active("")
	wantExit(t, err, output.ExitUsage)

	if !strings.Contains(err.Error(), "none") {
		t.Errorf("the message does not say there are no profiles:\n%v", err)
	}
	if !strings.Contains(err.Error(), cfg.path) {
		t.Errorf("the message does not name the file:\n%v", err)
	}
}

// TestResolveLadder is the half of §5.2 that is easiest to get wrong: fields
// resolve one at a time, so overriding one setting on the command line leaves
// the rest of the profile alone.
func TestResolveLadder(t *testing.T) {
	const env = "SPACEBAR_TEST_LADDER"

	cases := []struct {
		name                             string
		flag, envValue, profile, builtin string
		want                             string
	}{
		{name: "the flag wins", flag: "f", envValue: "e", profile: "p", builtin: "b", want: "f"},
		{name: "the environment beats the profile", envValue: "e", profile: "p", builtin: "b", want: "e"},
		{name: "the profile beats the built-in", profile: "p", builtin: "b", want: "p"},
		{name: "the built-in is the floor", builtin: "b", want: "b"},
		{name: "everything unset is empty", want: ""},
		{name: "an exported empty variable is not a value", envValue: "", profile: "p", want: "p"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(env, tc.envValue)
			if got := Resolve(tc.flag, env, tc.profile, tc.builtin); got != tc.want {
				t.Errorf("resolved %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEnvIsDerivedFromTheProductName holds the rename rule from the other side:
// nothing may spell the product name out, including in an environment variable,
// or a rename leaves a variable behind that still works and points nowhere.
func TestEnvIsDerivedFromTheProductName(t *testing.T) {
	if got := EnvProfile(); got != "SPACEBAR_PROFILE" {
		t.Errorf("EnvProfile is %q, want SPACEBAR_PROFILE", got)
	}
	if got := Env("CLIENT_ID"); got != "SPACEBAR_CLIENT_ID" {
		t.Errorf("Env(CLIENT_ID) is %q, want SPACEBAR_CLIENT_ID", got)
	}
}
