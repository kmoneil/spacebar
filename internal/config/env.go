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

// EnvVar is one environment variable this process reads.
type EnvVar struct {
	// Name is the variable as a shell spells it.
	//
	// Built by Env for the ones this project owns, so that a rename carries
	// through here the same way it carries through everywhere else, and written
	// out for the ones somebody else named.
	Name string

	// Purpose is one line, and it is the line a document shows and a failing
	// gate quotes. Written for somebody deciding whether to set it.
	Purpose string
}

// EnvVars is every environment variable this process reads.
//
// It exists because the alternative was a variable that worked and that nobody
// could find. SPACEBAR_PROFILE was read from the first milestone and appeared
// in exactly one place in the tree, the parenthesis at the end of --profile's
// help string, which is not where somebody setting up a CI job looks. Two of
// the others were in no hand-written document at all.
//
// So the list is the pivot for two gates that did not exist before. One walks
// the source and fails on a read of anything not listed here, which is what
// stops the list going stale. The other walks the hand-written documents and
// fails on an entry none of them mention, which is what the list is for. What
// neither can check is whether Purpose is worth reading.
//
// XDG_CONFIG_HOME, XDG_CACHE_HOME and XDG_DATA_HOME are honoured on every
// platform, Windows included, because somebody who sets one has said where they
// want their files. See Dir, CacheDir and DataDir for what each falls back to.
var EnvVars = []EnvVar{
	{
		Name: Env("PROFILE"),
		Purpose: "Profile to use when --profile is not given. " +
			"Overridden by the flag, and overrides default_profile in the configuration file.",
	},
	{
		Name: Env("CLIENT_ID"),
		Purpose: "OAuth client ID for `auth login`. " +
			"Overrides the profile's own and whatever this build was compiled with.",
	},
	{
		Name: Env("CLIENT_SECRET"),
		Purpose: "OAuth client secret for `auth login`. " +
			"Read here so that it reaches neither the configuration file nor the process list.",
	},
	{
		Name: Env("WEBHOOK_URL"),
		Purpose: "Incoming webhook URL for `profile set-webhook`, which takes it here or on stdin. " +
			"It has no flag, for the same reason: an argument lands in the shell history.",
	},
	{
		Name:    "NO_COLOR",
		Purpose: "Any non-empty value turns ANSI colour off everywhere, the same as --no-color.",
	},
	{
		Name:    "TERM",
		Purpose: "A value of `dumb` turns ANSI colour off. Nothing else about it is read.",
	},
	{
		Name: "XDG_CONFIG_HOME",
		Purpose: "Directory holding the configuration file and the credential fallback. " +
			"An absolute path, or the run fails rather than writing somewhere unexpected.",
	},
	{
		Name: "XDG_CACHE_HOME",
		Purpose: "Directory holding the cached space list, which can be deleted at any moment " +
			"and is rebuilt from the API. Absolute, on the same terms.",
	},
	{
		Name: "XDG_DATA_HOME",
		Purpose: "Directory holding the local message index, which cannot be rebuilt: " +
			"it has messages the API will not answer for twice. Absolute, on the same terms.",
	},
}
