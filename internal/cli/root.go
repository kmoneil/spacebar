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

// Package cli defines the command tree and nothing else.
//
// SPEC.md §4 makes this an architectural rule rather than a preference: this
// package and internal/mcpsrv are both thin adapters over the same internal
// packages, and no business logic may live in either. A feature that works in
// the CLI but not over MCP is a bug, and the only way to keep that true is for
// neither adapter to be the place where a decision gets made.
package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// Options are the global flags from SPEC.md §10. Every command sees them, so
// they are parsed once, here, and passed down rather than re-declared.
type Options struct {
	Profile string
	JSON    bool
	Quiet   bool
	Verbose bool
	Timeout time.Duration
	NoColor bool
	DryRun  bool
	Yes     bool
}

// defaultTimeout is the per-request budget from SPEC.md §7.4. Uploads and
// downloads override it; nothing else should.
const defaultTimeout = 30 * time.Second

// Execute builds the command tree, runs it, and returns the code the process
// should exit with.
//
// It returns rather than calling os.Exit so that the whole tree is testable
// without a subprocess: a test can run a command, read its streams, and check
// the exit code as a value.
func Execute(args []string) output.ExitCode {
	opts := &Options{}
	root := New(opts)
	root.SetArgs(args)

	err := root.Execute()
	return output.Report(root.ErrOrStderr(), err, opts.JSON)
}

// New builds the command tree, binding the global flags into opts.
func New(opts *Options) *cobra.Command {
	root := &cobra.Command{
		Use:   meta.AppName,
		Short: "A terminal client and MCP server for Google Chat",
		Long: meta.AppName + `: a terminal client and MCP server for Google Chat.

Send a message from a script, read a space, or hand an agent a tool it can
call. Structured output goes to stdout, everything else to stderr, and the
exit code says what happened.

Google Chat is a trademark of Google LLC. This project is not affiliated
with, sponsored by, or endorsed by Google.`,

		// Both silences are deliberate. output.Report is the single place a
		// failure is written, so cobra printing its own copy would double
		// every error, and in --json mode it would put an unstructured line
		// next to the structured one a caller is parsing.
		SilenceErrors: true,
		SilenceUsage:  true,

		// Set so that cobra's Find does not classify a stray argument itself.
		// With Args nil, an unknown command reaches a non-runnable root, which
		// cobra answers by printing help and exiting 0, so a script asking for
		// something that does not exist would be told it succeeded.
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			return output.Usagef("unknown command %q\nrun '%s --help' for the command list", args[0], meta.AppName)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	// A flag parse failure is the caller's mistake and has to exit 2, not 1.
	// Cobra's default returns the bare error, which would land in the generic
	// bucket.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return output.Usagef("%v", err)
	})

	f := root.PersistentFlags()
	f.StringVar(&opts.Profile, "profile", "", "profile to use (overrides SPACEBAR_PROFILE and the configured default)")
	f.BoolVar(&opts.JSON, "json", false, "emit structured output on stdout; one object per line for lists")
	f.BoolVar(&opts.Quiet, "quiet", false, "suppress progress and non-essential messages on stderr")
	f.BoolVar(&opts.Verbose, "verbose", false, "log requests, retries, and timings to stderr")
	f.DurationVar(&opts.Timeout, "timeout", defaultTimeout, "per-request timeout")
	f.BoolVar(&opts.NoColor, "no-color", false, "never write ANSI colour, even to a terminal")
	f.BoolVar(&opts.DryRun, "dry-run", false, "print the request that would be sent and exit without sending it")
	f.BoolVar(&opts.Yes, "yes", false, "answer yes to any confirmation this command would ask for")

	root.AddCommand(
		newVersionCmd(opts),
		newLicensesCmd(),
	)

	return root
}
