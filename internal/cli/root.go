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

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
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

// The per-attempt budget is chat.DefaultTimeout and is not restated here.
// Two declarations of the same number are two numbers, and the one that gets
// changed is never the one that gets read.

// renderer builds the writer for one command from the global flags.
//
// The streams come from cobra, which is how this package is meant to write:
// only internal/output may name a process stream directly, and a test drives
// the whole tree against buffers because of it. The two questions cobra cannot
// answer, whether stdout is a terminal and whether there is anybody on the
// other end of stdin, are asked of internal/output for the same reason.
func renderer(cmd *cobra.Command, opts *Options) *output.Renderer {
	out, in := cmd.OutOrStdout(), cmd.InOrStdin()

	return output.NewRenderer(out, cmd.ErrOrStderr(), output.Options{
		JSON:  opts.JSON,
		Quiet: opts.Quiet,

		// Both are asked of the streams this command was given rather than of
		// the process, so that what a test drives is what the rules are checked
		// against. internal/output is still the only package that names a
		// process stream, which is why these two live there.
		Color:       output.ColorFor(out, opts.NoColor),
		Interactive: output.Interactive(in),
		AssumeYes:   opts.Yes,
	})
}

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
	// The environment variable is named by config rather than written out. A
	// rename is a change to meta.AppName and to nothing else, and help text
	// that spelled the old name would survive one and go on being wrong.
	f.StringVar(&opts.Profile, "profile", "", "profile to use (overrides "+config.EnvProfile()+" and the configured default)")
	f.BoolVar(&opts.JSON, "json", false, "emit structured output on stdout; one object per line for lists")
	f.BoolVar(&opts.Quiet, "quiet", false, "suppress progress and non-essential messages on stderr")
	f.BoolVar(&opts.Verbose, "verbose", false, "log requests, retries, and timings to stderr")
	// Named as bounding an attempt rather than the command, because it does,
	// and because the difference is three minutes of somebody's afternoon. A
	// request that is retried under the policy in SPEC.md §7.4 makes up to five
	// attempts, each with this budget, with backoff between them.
	f.DurationVar(&opts.Timeout, "timeout", chat.DefaultTimeout, "timeout for one attempt; a retried request makes up to five")
	f.BoolVar(&opts.NoColor, "no-color", false, "never write ANSI colour, even to a terminal")
	f.BoolVar(&opts.DryRun, "dry-run", false, "print the request that would be sent and exit without sending it")
	f.BoolVar(&opts.Yes, "yes", false, "answer yes to any confirmation this command would ask for")

	root.AddCommand(
		newVersionCmd(opts),
		newLicensesCmd(),
		newProfileCmd(opts),
		newSendCmd(opts),
		newAuthCmd(opts),
		newSpacesCmd(opts),
		newMessagesCmd(opts),
		newAliasCmd(opts),
		newTailCmd(opts),
		newReactCmd(opts),
		newMCPCmd(opts),
	)

	return root
}
