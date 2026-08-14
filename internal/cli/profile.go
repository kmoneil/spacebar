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
	"errors"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/transport"
	"github.com/kmoneil/spacebar/internal/transport/webhook"
)

// The profile command group is a departure from SPEC.md §10, which lists only
// OAuth verbs under `auth` and no way at all to give a profile a webhook.
//
// Taken with §5.3, which says a secret never lands in config.json, that left a
// Milestone 2 user with no supported way to configure the only transport
// Milestone 2 ships: they would have to hand-edit config.json to add a keyring
// reference and then populate the keyring with some other tool. That is not an
// onboarding path for the population this milestone exists to serve, so the
// gap is filled rather than worked around, and the group is named for what it
// does so that Milestone 3's `auth login` does not have to pretend a webhook is
// a login.
//
// `profile add` is deliberately absent. For the only transport that exists, a
// profile is its webhook, so a profile created without one is an entry that
// cannot do anything. set-webhook creates or replaces, which makes the whole of
// setup one command. `add` earns its place in Milestone 3, when there is a
// second transport to choose between.

// webhookURLEnv is the other way the URL arrives.
func webhookURLEnv() string { return config.Env("WEBHOOK_URL") }

func newProfileCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Configure the profiles this tool sends through",
		Long: `Configure the profiles this tool sends through.

A profile is one way of reaching Chat: which transport, and which credential.
The credential itself is never in the configuration file. It lives in the OS
keyring, and the file holds a reference to it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newProfileListCmd(opts),
		newProfileSetWebhookCmd(opts),
		newProfileRemoveCmd(opts),
	)
	return cmd
}

// profileInfo is the --json shape of one row of `profile list`.
type profileInfo struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Default   bool   `json:"default"`

	// Configured says a credential reference is recorded, and deliberately not
	// that the credential is there. Answering the second question means reading
	// the keyring, which can prompt, fail, or be locked, and a command whose
	// job is to say what is configured must work when none of that is
	// available.
	Configured bool `json:"configured"`
}

func newProfileListCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the configured profiles",
		Long: `List the configured profiles.

Reports what is configured, not what works. Nothing here reads a credential or
touches the network, so it answers on a machine whose keyring is locked and for
a profile whose secret has been deleted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			r := renderer(cmd, opts)

			names := cfg.Names()
			if len(names) == 0 {
				// An empty list writes nothing to stdout, because zero results
				// is what a caller parsing this has to see. The sentence goes
				// to stderr, where it cannot corrupt that.
				r.Note("no profiles are configured. Add one with: %s profile set-webhook NAME", meta.AppName)
				return nil
			}

			for _, name := range names {
				profile := cfg.Profiles[name]
				info := profileInfo{
					Name:       name,
					Transport:  string(profile.Transport),
					Default:    name == cfg.DefaultProfile,
					Configured: profile.WebhookURLRef != "" || profile.ClientSecretRef != "",
				}
				if err := r.Item(info, info.Name, info.Transport, marker(info.Default, "default"), marker(info.Configured, "configured")); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// marker renders a boolean as a word or as nothing, so that a tab-separated row
// stays greppable. A column reading "false" invites a script to test for the
// string.
func marker(on bool, word string) string {
	if on {
		return word
	}
	return "-"
}

// webhookResult is the --json shape of a stored webhook.
type webhookResult struct {
	Name string `json:"name"`

	// Reference is where the secret went, which is safe to print and is the
	// only part of this that is. The URL itself never appears in output, in a
	// log, or in the configuration file.
	Reference string `json:"reference"`
	Transport string `json:"transport"`
	Default   bool   `json:"default"`

	// Verified says a message was actually posted, and Space says where it
	// went. Both are absent without --verify rather than reported as false: a
	// caller has to be able to tell "it did not work" from "nobody checked".
	Verified bool   `json:"verified,omitempty"`
	Space    string `json:"space,omitempty"`
}

// defaultVerifyText is what --verify posts when the caller does not choose.
//
// It says what it is, because it arrives in a space full of people who did not
// run the command and have no idea why a bot just spoke. A test message that
// reads like a real one is a test message somebody replies to.
const defaultVerifyText = "Test message from " + meta.AppName + ". This webhook is configured and working."

func newProfileSetWebhookCmd(opts *Options) *cobra.Command {
	var verify bool
	var verifyText string

	cmd := &cobra.Command{
		Use:   "set-webhook NAME",
		Short: "Give a profile an incoming webhook URL, from stdin",
		Long: `Give a profile an incoming webhook URL, creating the profile if there is not one.

The URL arrives on stdin, or in ` + webhookURLEnv() + `, and never as an
argument: an argument lands in the shell history, where it outlives the
session, and in the process list, where every other user on the machine can
read it while the command runs. A Chat webhook URL carries key and token query
parameters that are the entire authentication for that space, so it is a
credential wearing the costume of a URL.

The URL is stored in the OS keyring, or in a mode 0600 file beside the
configuration on a machine that has no keyring, which warns every time it is
used. The configuration file gets a reference to it and never the value.

  ` + meta.AppName + ` profile set-webhook alerts < webhook.txt
  pbpaste | ` + meta.AppName + ` profile set-webhook alerts`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetWebhook(cmd, opts, args[0], verifyOptions{on: verify, text: verifyText})
		},
	}

	// Off by default, because it posts a real message into a real space that
	// other people are reading, and a setup command that did that without being
	// asked would be rude in a way somebody only finds out about afterwards.
	//
	// It is worth having because there is no other way to tell. A webhook has no
	// endpoint that reports whether it works: the only way to find out is to
	// post, so somebody who cannot tell a mistyped URL from an organizational
	// unit with Chat apps switched off has no way to learn which they have.
	cmd.Flags().BoolVar(&verify, "verify", false,
		"post a message to the space to prove the webhook works")
	cmd.Flags().StringVar(&verifyText, "verify-text", defaultVerifyText,
		"what --verify posts")

	return cmd
}

// verifyOptions is what --verify and --verify-text asked for.
type verifyOptions struct {
	on   bool
	text string
}

// runSetWebhook stores the URL and, if asked, proves it works.
//
// Split out of the command because the command was over the complexity ceiling
// with it inline, which is the gate doing its job: this is four steps that each
// have a failure worth handling separately, not one operation.
func runSetWebhook(cmd *cobra.Command, opts *Options, name string, verify verifyOptions) error {
	r := renderer(cmd, opts)

	rawURL, err := readWebhookURL(cmd.InOrStdin(), r)
	if err != nil {
		return err
	}

	// A dry run of this command changes nothing at all, not the keyring and not
	// the configuration file. The other reading, where it stores the credential
	// and only declines to send, would be a --dry-run that wrote to disk, and
	// somebody who typed it to find out what would happen would have had half
	// of it happen.
	if opts.DryRun {
		return dryRunSetWebhook(cmd, opts, r, name, rawURL, verify)
	}

	result, err := storeWebhook(r, name, rawURL)
	if err != nil {
		return err
	}
	if result.Default {
		r.Note("%q is now the default profile.", name)
	}

	fields := output.Fields{
		{Label: "profile", Value: result.Name},
		{Label: "transport", Value: result.Transport},
		{Label: "credential", Value: result.Reference},
	}

	if verify.on {
		space, err := verifySend(cmd.Context(), name, rawURL, verify.text, opts, r)
		if err != nil {
			return err
		}
		result.Verified, result.Space = true, space
		fields = append(fields, output.Field{Label: "verified", Value: space})
	}

	return r.Result(result, fields)
}

// dryRunSetWebhook shows what the command would do and does none of it.
//
// With --verify there is a request to show, and it is the real one: the same
// transport builds it and internal/chat stops it at the moment before sending.
// Without --verify there is no request at all, so what is reported is the
// change that would have been made to the configuration.
func dryRunSetWebhook(cmd *cobra.Command, opts *Options, r *output.Renderer, name, rawURL string, verify verifyOptions) error {
	if err := auth.CheckWebhookURL(rawURL); err != nil {
		return err
	}

	if verify.on {
		_, err := verifySend(cmd.Context(), name, rawURL, verify.text, opts, r)
		if dry, ok := errors.AsType[*chat.DryRun](err); ok {
			r.Note("nothing was stored and nothing was sent.")
			return r.Block(dry.Request, dry.Request.Text())
		}
		if err != nil {
			return err
		}
	}

	result := webhookResult{
		Name:      name,
		Reference: auth.Ref(name, auth.WebhookSecret),
		Transport: string(config.TransportWebhook),
	}
	return r.Result(result, output.Fields{
		{Label: "profile", Value: result.Name},
		{Label: "transport", Value: result.Transport},
		{Label: "credential", Value: result.Reference},
		{Label: "dry-run", Value: "nothing was stored"},
	})
}

// storeWebhook puts the credential where it belongs and records the reference.
func storeWebhook(r *output.Renderer, name, rawURL string) (webhookResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return webhookResult{}, err
	}
	store, err := auth.New()
	if err != nil {
		return webhookResult{}, err
	}

	if err := store.SetWebhook(cfg, name, rawURL); err != nil {
		r.Warnings(store.Warnings())
		return webhookResult{}, err
	}

	// The first profile becomes the default. Somebody who has exactly one has
	// no use for choosing it on every command, and the alternative is a setup
	// that succeeds and then fails at the next step for a reason nobody
	// explained.
	madeDefault := cfg.DefaultProfile == ""
	if madeDefault {
		cfg.DefaultProfile = name
	}
	if err := cfg.Save(); err != nil {
		r.Warnings(store.Warnings())
		return webhookResult{}, err
	}

	// Printed before anything else: this is the moment a credential lands on
	// disk in plain text, and a warning lost here is the one that mattered most.
	r.Warnings(store.Warnings())

	return webhookResult{
		Name:      name,
		Reference: auth.Ref(name, auth.WebhookSecret),
		Transport: string(config.TransportWebhook),
		Default:   madeDefault,
	}, nil
}

// verifySend posts the verification message and returns the space it reached.
//
// It goes through the transport rather than through internal/chat, which is not
// a preference: internal/lint refuses a Chat client anywhere but a transport,
// so that a capability check cannot be bypassed by a command holding one of its
// own. This command is the first caller of the webhook transport and gets the
// same treatment as any other.
func verifySend(ctx context.Context, profile, rawURL, text string, opts *Options, r *output.Renderer) (string, error) {
	post, err := webhook.New(webhook.Options{
		Profile: profile,
		URL:     rawURL,
		Timeout: opts.Timeout,
		DryRun:  opts.DryRun,

		// Only under --verbose. Without this the flag would be accepted and do
		// nothing, which is worse than not having it: somebody debugging a
		// webhook would conclude no request was made.
		Log: verboseLog(opts, r),
	})
	if err != nil {
		return "", err
	}
	if err := transport.Require(post, "profile set-webhook --verify", transport.CanSend); err != nil {
		return "", err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := post.Send(ctx, chat.SendRequest{Message: chat.Message{Text: text}}); err != nil {
		return "", err
	}
	return post.Space(), nil
}

// maxWebhookURL bounds what is read from stdin.
//
// A webhook URL is a couple of hundred characters. Reading an unbounded stream
// into memory because somebody piped the wrong thing in is a failure with no
// upside, and four kilobytes is far past any URL while being small enough that
// the mistake is reported rather than swallowed.
const maxWebhookURL = 4 << 10

// readWebhookURL finds the URL, from the environment or from stdin.
//
// The environment is checked first, and when it is set stdin is not read at
// all. The two paths belong to different callers: a CI runner exports the
// variable, a person pipes the value in. Reading stdin anyway would mean a
// command that has everything it needs sitting and waiting for input.
func readWebhookURL(in io.Reader, r *output.Renderer) (string, error) {
	if value := os.Getenv(webhookURLEnv()); strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), nil
	}

	if r.IsInteractive() {
		// Only worth saying to somebody at a keyboard, who would otherwise be
		// looking at a command that appears to have hung. In a pipeline it is a
		// line in a log that explains nothing.
		r.Note("reading the webhook URL from stdin. Paste it, then press Enter and Ctrl-D.")
	}

	body, err := io.ReadAll(io.LimitReader(in, maxWebhookURL+1))
	if err != nil {
		return "", output.Errorf("WEBHOOK_URL", output.ExitUsage, "could not read the webhook URL: %v", err)
	}
	if len(body) > maxWebhookURL {
		return "", output.Errorf("WEBHOOK_URL", output.ExitUsage,
			"what arrived on stdin is longer than %d bytes, which no webhook URL is.", maxWebhookURL)
	}

	value := strings.TrimSpace(string(body))
	if value == "" {
		return "", output.Errorf("WEBHOOK_URL", output.ExitUsage,
			"no webhook URL arrived.\n"+
				"Pipe it in, or set %s. It is not an argument on purpose: an argument lands in the "+
				"shell history and in the process list, and this URL is a credential.\n"+
				"  %s profile set-webhook NAME < webhook.txt",
			webhookURLEnv(), meta.AppName)
	}
	return value, nil
}

func newProfileRemoveCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:     "rm NAME",
		Aliases: []string{"remove"},
		Short:   "Remove a profile and the credential behind it",
		Long: `Remove a profile and the credential behind it.

The credential goes too, from the keyring and from the fallback file. That
cannot be undone from here: a webhook URL is only recoverable from the space it
was created in.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			r := renderer(cmd, opts)

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if _, ok := cfg.Profiles[name]; !ok {
				return output.Usagef("no profile named %q.\nConfigured: %s",
					name, strings.Join(cfg.Names(), ", "))
			}

			if err := r.Confirm(cmd.InOrStdin(),
				"Remove profile %q and the credential behind it?", name); err != nil {
				return err
			}

			if err := removeProfile(cfg, name, r); err != nil {
				return err
			}
			return r.Result(map[string]any{"name": name, "removed": true},
				output.Fields{{Label: "removed", Value: name}})
		},
	}
}

// removeProfile performs the removal. Split out so that the command above stays
// a sequence of steps somebody can read.
func removeProfile(cfg *config.Config, name string, r *output.Renderer) error {
	secrets, err := auth.New()
	if err != nil {
		return err
	}
	if err := secrets.RemoveProfile(cfg, name); err != nil {
		r.Warnings(secrets.Warnings())
		return err
	}
	if err := cfg.Save(); err != nil {
		r.Warnings(secrets.Warnings())
		return err
	}
	r.Warnings(secrets.Warnings())
	return nil
}
