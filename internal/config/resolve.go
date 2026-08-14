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
	"os"
	"strings"

	"github.com/kmoneil/spacebar/internal/meta"
)

// Env returns the environment variable this tool reads a setting from.
//
// Derived from the product name rather than written out, because SPEC.md
// requires a rename to be a change to meta.AppName and to nothing else. A
// hardcoded SPACEBAR_PROFILE would be one more place that survives the rename
// and stops working.
func Env(name string) string {
	return strings.ToUpper(meta.AppName) + "_" + name
}

// EnvProfile names the profile to use. SPEC.md §5.2, step 2.
func EnvProfile() string { return Env("PROFILE") }

// Active resolves which profile is in play, per SPEC.md §5.2.
//
// The order is --profile, then the environment, then default_profile, then a
// failure that names what was configured. flagValue is passed in rather than
// read here because a flag belongs to the command that declared it, and this
// package does not know about cobra.
//
// The source of the name travels into the error. "no profile named work" sends
// somebody to their config file; "no profile named work, from SPACEBAR_PROFILE"
// sends them to the environment variable they exported in another window three
// weeks ago, which is where the problem actually is.
func (c *Config) Active(flagValue string) (string, Profile, error) {
	name, source := flagValue, "--profile"
	if name == "" {
		name, source = os.Getenv(EnvProfile()), EnvProfile()
	}
	if name == "" {
		name, source = c.DefaultProfile, "default_profile"
	}

	if name == "" {
		return "", Profile{}, configErr("no profile was given and none is configured as the default in %s.\n"+
			"Configured profiles: %s\nUse --profile, set %s, or set default_profile.",
			c.sourceOrDefault(), listOrNone(c.Names()), EnvProfile())
	}

	profile, ok := c.Profiles[name]
	if !ok {
		return "", Profile{}, configErr("no profile named %q, which came from %s.\n"+
			"Configured in %s: %s",
			name, source, c.sourceOrDefault(), listOrNone(c.Names()))
	}
	return name, profile, nil
}

// sourceOrDefault names the file even when there was not one to read, so that a
// first-run failure says where the file it is missing would go.
func (c *Config) sourceOrDefault() string {
	if c.path != "" {
		return c.path
	}
	if path, err := Path(); err == nil {
		return path
	}
	return FileName
}

// Resolve returns the first value in the ladder that is set, per SPEC.md §5.2.
//
// The ladder runs per field and not per profile: --client-id beats
// SPACEBAR_CLIENT_ID beats the profile beats the value linked into the binary,
// and choosing a profile does not choose every field in it. A caller who
// overrides one setting on the command line keeps the rest of their profile.
//
// Empty means unset at every rung, including for an environment variable that
// is exported as the empty string. That is the same reading output.UseColor
// gives NO_COLOR, and for the same reason: exporting an empty value is how
// somebody un-sets a variable for one command in a shell that cannot unset it,
// and treating it as a value would leave them no way back.
func Resolve(flagValue, envName, profileValue, builtin string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv(envName); v != "" {
		return v
	}
	if profileValue != "" {
		return profileValue
	}
	return builtin
}
