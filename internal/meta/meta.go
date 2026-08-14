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

// Package meta holds the identity of the binary: what it is called, what
// version it is, and which OAuth client it was linked with.
//
// The product name is written here once and nowhere else. The naming note in
// SPEC.md requires a rename to be a change to AppName alone, so no other
// package may spell the name out in user-facing text. internal/lint holds that
// rule to the source.
package meta

import "runtime"

const (
	// AppName is the product and the binary. The only place it is written.
	AppName = "spacebar"

	// RepoURL travels in the User-Agent, so somebody looking at Chat API
	// traffic they did not expect can find out what is making it (SPEC.md
	// §7.4).
	RepoURL = "https://github.com/kmoneil/spacebar"
)

// Version and Commit are stamped at link time by `make build`:
//
//	-X github.com/kmoneil/spacebar/internal/meta.Version=$(VERSION)
//
// A plain `go build` leaves them as they are, which is the honest answer for a
// binary nobody released. The release gate refuses a tag whose binary does not
// report it.
var (
	Version = "0.0.0-dev"
	Commit  = "unknown"
)

// DefaultClientID and DefaultClientSecret are empty in source and stay that
// way.
//
// SPEC.md §6.1: this is an Apache-2.0 repository, so an OAuth client committed
// here is a client every fork uses. Forks would spend our per-project quota,
// their users would see our consent screen, which is phishing-adjacent, and one
// abusive fork could get the client suspended out from under every legitimate
// user. Official release builds inject these from CI secrets; a build from
// source falls through to bring-your-own resolution and says so in as many
// words.
//
// It is a quota and reputation measure and not a security one. RFC 8252 is
// clear that a native-app secret is not confidential, and nothing here pretends
// otherwise.
var (
	DefaultClientID     string
	DefaultClientSecret string
)

// UserAgent identifies this build on every request it makes (SPEC.md §7.4).
func UserAgent() string {
	return AppName + "/" + Version + " (+" + RepoURL + ")"
}

// GoVersion is the toolchain this binary was built with. Reported by
// `spacebar version` because the answer to "why does TLS behave differently on
// your machine" is usually here.
func GoVersion() string {
	return runtime.Version()
}
