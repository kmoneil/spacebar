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

package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/resolve"
)

// refreshHelp is the same sentence wherever --refresh is registered, because
// two wordings of one flag is one more than anybody needs.
const refreshHelp = "list spaces again instead of using the cached names"

// addRefreshFlag registers --refresh on a command that resolves a target.
//
// Not a global flag. It would then appear in the help of every command,
// including the ones that take no target and the ones that cannot read, which
// is a flag offering to do something it will not do.
func addRefreshFlag(cmd *cobra.Command, refresh *bool) {
	cmd.Flags().BoolVar(refresh, "refresh", false, refreshHelp)
}

// resolveTarget turns what somebody typed into a space resource name.
//
// A thin adapter, per the rule that no decision is made in this package: the
// four steps, the matching, and the refusals all live in internal/resolve, so
// that the MCP server gets the same behaviour without reimplementing it.
//
// The cache is per profile. Two profiles authorized as different accounts reach
// different spaces, and one shared file would let one account's space list
// answer the other's lookup.
func resolveTarget(ctx context.Context, opened *profile.Open, target string, refresh bool) (string, error) {
	reader, ok := opened.Transport.(resolve.Reader)
	if !ok {
		// Unreachable while every transport implements the full interface, and
		// asserted rather than assumed because that is a claim about today's
		// interface and not about tomorrow's.
		return target, nil
	}

	return resolve.Resolve(ctx, reader, target, resolve.Options{
		Aliases: opened.Aliases,
		Cache:   resolve.NewCache(opened.Name),
		Refresh: refresh,
	})
}

// forgetSpaces drops a profile's cached space list, for the two commands that
// end what produced it.
//
// A warning rather than a failure, and the reason is what has already happened
// by the time this runs. `auth logout` has deleted the token and `profile rm`
// has deleted the profile and its credential, so returning an error here would
// report a failure for a command that did the irreversible part and succeeded.
// A warning says what is still on disk, which is the only thing the caller can
// act on.
//
// Silence would be wrong for the same reason it is wrong elsewhere in this
// tool: the file holds the display name of every space that profile could
// reach, and somebody who typed logout is entitled to know it is still there.
func forgetSpaces(r *output.Renderer, profileName string) {
	if err := resolve.NewCache(profileName).Forget(); err != nil {
		r.Warn("the cached space list for profile %q is still on disk: %v", profileName, err)
	}
}
