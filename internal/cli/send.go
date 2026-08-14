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
	"errors"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/format"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
	"github.com/kmoneil/spacebar/internal/profile"
	"github.com/kmoneil/spacebar/internal/transport"
)

// stdinArg is the text argument that means "read the body from stdin".
//
// Explicit on purpose. A missing argument is a usage failure and never a silent
// read, so there is no path where somebody who forgot to type a message ends up
// looking at a command that appears to have hung.
const stdinArg = "-"

// maxBodyBytes bounds what is read from stdin.
//
// A Chat message is capped at 32,000 bytes by the API, so anything past this is
// a message that cannot be sent whatever we do with it, and reading an
// unbounded stream into memory because somebody piped the wrong file in is a
// failure with no upside.
const maxBodyBytes = 1 << 20

// sendFlags are what `send` was asked for.
type sendFlags struct {
	md         bool
	threadKey  string
	messageID  string
	idempotent bool
	cardFile   string

	// replyTo and file are registered and not implemented. See the comment on
	// requireCapabilities.
	replyTo string
	file    string
}

// sendResult is the --json shape of a successful send.
//
// A public contract the moment a golden records it, so it holds what a caller
// would branch on and nothing that is merely nice to look at. The message name
// is the whole of it for a retrying script: it is what makes a second send
// recognisable as a duplicate.
type sendResult struct {
	Message    string `json:"message,omitempty"`
	Space      string `json:"space"`
	Thread     string `json:"thread,omitempty"`
	CreateTime string `json:"create_time,omitempty"`

	// Profile is here because a script that sends through several needs to know
	// which one this went through, and the answer is not derivable from the
	// rest.
	Profile string `json:"profile"`
}

func newSendCmd(opts *Options) *cobra.Command {
	flags := &sendFlags{}

	cmd := &cobra.Command{
		Use:   "send [TARGET] TEXT",
		Short: "Post a message to a space",
		Long: `Post a message to a space.

  ` + meta.AppName + ` send 'deploy done'                  # a webhook profile knows its space
  ` + meta.AppName + ` send spaces/AAAAAAA 'deploy done'   # name the space
  ` + meta.AppName + ` send --md 'deploy **done**'         # translate CommonMark
  echo 'deploy done' | ` + meta.AppName + ` send -         # body from stdin

The target is the first argument when there are two. A webhook profile posts
to one space and is the only thing that authenticates the request, so on one of
those the target may be left off, and naming a different space is refused
rather than sent somewhere else.

Text is sent exactly as typed. Chat markup is not CommonMark: bold is one
asterisk, so ` + "`**bold**`" + ` arrives with the asterisks showing. --md translates,
and the translation is one way: its output read back as CommonMark means
something else, so a body that has been through --md must not go through it
again.`,

		// Two arguments at most, and how they are read depends on how many
		// there are rather than on what they look like. Guessing whether an
		// argument is a target or a message by inspecting it is how a message
		// gets sent as a space name.
		//
		// Checked here rather than with cobra.RangeArgs because that returns a
		// plain error, which lands in the generic bucket at exit 1. The number
		// of arguments is the caller's mistake and every one of those is exit 2.
		Args: sendArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSend(cmd, opts, args, flags)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&flags.md, "md", false, "translate the body from CommonMark into Chat markup")
	f.StringVar(&flags.threadKey, "thread-key", "", "group this message into the thread with this key")
	f.StringVar(&flags.messageID, "message-id", "", "the message's own name, which makes a resend a duplicate the API refuses")
	f.BoolVar(&flags.idempotent, "idempotent", false, "derive --message-id from the space, the body, and the thread key")
	f.StringVar(&flags.cardFile, "card", "", "a JSON file holding cardsV2, sent alongside the text")

	// Registered rather than left out, and this is a deliberate choice about
	// what a person learns from a failure. An unregistered flag is exit 2,
	// "unknown flag --file", which says this tool cannot do attachments at all.
	// A registered one is exit 5 naming the capability and the profile, which
	// says this profile cannot, and that is both true and actionable.
	f.StringVar(&flags.file, "file", "", "attach a file (requires a profile that can upload)")
	f.StringVar(&flags.replyTo, "reply-to", "", "reply to a message by name (requires a profile that can read)")

	return cmd
}

// sendArgs refuses a count that cannot be read, before anything else happens.
//
// The zero case is the one worth the message. Somebody who typed `send` and
// nothing else is not asking to type a message into a terminal, and reading
// stdin here would leave them looking at a command that appears to have hung.
// Asking for stdin is what "-" is for.
func sendArgs(_ *cobra.Command, args []string) error {
	switch len(args) {
	case 1, 2:
		return nil
	case 0:
		return output.Usagef("send needs a message.\n"+
			"  %s send 'deploy done'\n"+
			"  %s send SPACE 'deploy done'\n"+
			"  echo 'deploy done' | %s send -",
			meta.AppName, meta.AppName, meta.AppName)
	}
	return output.Usagef("send takes a message, or a space and a message, and got %d arguments.\n"+
		"A message with spaces in it is one argument: quote it.", len(args))
}

// runSend is the whole command, in the order the steps have to happen.
//
// Nothing here decides anything. The profile is resolved by internal/profile,
// the capability check is internal/transport's, the translation is
// internal/format's, and the request is internal/chat's. If a decision were
// being made in this file it would be a decision internal/mcpsrv had to make
// again in Milestone 5, and make differently.
func runSend(cmd *cobra.Command, opts *Options, args []string, flags *sendFlags) error {
	if err := flags.check(); err != nil {
		return err
	}
	r := renderer(cmd, opts)

	opened, err := profile.For(profile.Options{
		Name:    opts.Profile,
		Timeout: opts.Timeout,
		Log:     verboseLog(opts, r),
		DryRun:  opts.DryRun,
	})
	r.Warnings(opened.Warnings)
	if err != nil {
		return err
	}

	target, text, err := splitArgs(opened.Transport, args)
	if err != nil {
		return err
	}
	if err := requireCapabilities(opened.Transport, flags); err != nil {
		return err
	}

	body, err := readBody(cmd.InOrStdin(), r, text)
	if err != nil {
		return err
	}
	message, err := buildMessage(body, flags, r)
	if err != nil {
		return err
	}

	req := chat.SendRequest{
		Space:     target,
		Message:   message,
		ThreadKey: flags.threadKey,
		MessageID: messageID(flags, target, message.Text),
	}

	return send(cmd, r, opened, req)
}

// check refuses the flag combinations that contradict each other, before
// anything is read or resolved.
func (f *sendFlags) check() error {
	if f.messageID != "" && f.idempotent {
		return output.Usagef("--message-id and --idempotent both name the message, and they disagree.\n" +
			"Use --message-id to choose the name, or --idempotent to have one derived.")
	}
	if f.messageID != "" && !strings.HasPrefix(f.messageID, chat.MessageIDPrefix) {
		// The API requires the prefix and refuses an ID without it with a
		// message that does not mention the prefix, so somebody who stripped it
		// while tidying up would get an error that reads like a problem with
		// their message.
		return output.Usagef("--message-id has to begin with %q, which the API requires of any name a caller chooses.",
			chat.MessageIDPrefix)
	}
	return nil
}

// splitArgs works out which argument is which.
//
// By arity and not by inspection. Deciding whether an argument is a target or a
// message by looking at it is how a message gets sent as a space name, and the
// two are not distinguishable in general: "spaces/AAAA" is a plausible thing to
// say to a colleague.
func splitArgs(t transport.Transport, args []string) (target, text string, err error) {
	if len(args) == 2 {
		return args[0], args[1], nil
	}

	if _, fixed := transport.SpaceOf(t); fixed {
		return "", args[0], nil
	}
	return "", "", output.Usagef("this profile can reach more than one space, so %q is not enough to say where to send.\n"+
		"Name the space first: %s send SPACE %q", args[0], meta.AppName, args[0])
}

// requireCapabilities refuses before anything is built or sent.
//
// SPEC.md §8.2: exit 5, naming the profile and the fix. Every flag here is one
// this build cannot honour on any profile yet, which is exactly why they are
// registered: the failure says which capability is missing rather than
// pretending the flag does not exist.
func requireCapabilities(t transport.Transport, flags *sendFlags) error {
	if flags.file != "" {
		return transport.Require(t, "send --file", transport.CanUpload)
	}
	if flags.replyTo != "" {
		// Replying to a message means finding out which thread it is in, which
		// means reading the space. A webhook cannot, which is why this is a
		// read capability rather than a threading one.
		return transport.Require(t, "send --reply-to", transport.CanRead)
	}
	if flags.cardFile != "" {
		return transport.Require(t, "send --card", transport.CanSendCards)
	}
	return transport.Require(t, "send", transport.CanSend)
}

// readBody returns the message text, from the argument or from stdin.
func readBody(in io.Reader, r *output.Renderer, text string) (string, error) {
	if text != stdinArg {
		return text, nil
	}

	if r.IsInteractive() {
		// Only worth saying to somebody at a keyboard, who would otherwise be
		// looking at a command that appears to have hung. Asking for it is what
		// makes waiting correct: SPEC.md §11.3 forbids blocking on input nobody
		// asked to give, and "-" is asking.
		r.Note("reading the message from stdin. Type it, then press Ctrl-D.")
	}

	body, err := io.ReadAll(io.LimitReader(in, maxBodyBytes+1))
	if err != nil {
		return "", output.Usagef("could not read the message from stdin: %v", err)
	}
	if len(body) > maxBodyBytes {
		return "", output.Usagef("the message on stdin is over %d bytes, which is more than Chat will accept.", maxBodyBytes)
	}

	// Trailing newlines go, because a shell adds one and nobody means it. What
	// is inside the message is untouched: a blank line between paragraphs is
	// something somebody wrote.
	return strings.TrimRight(string(body), "\n"), nil
}

// buildMessage turns the body and the flags into the message to send.
func buildMessage(body string, flags *sendFlags, r *output.Renderer) (chat.Message, error) {
	text := body

	if flags.md {
		translated, warnings, err := format.Translate(body)
		if err != nil {
			return chat.Message{}, err
		}
		r.Warnings(warnings)
		text = translated
	} else if err := format.Validate(body); err != nil {
		return chat.Message{}, err
	}

	message := chat.Message{Text: text}
	if flags.cardFile == "" {
		return message, nil
	}

	raw, err := os.ReadFile(flags.cardFile)
	if err != nil {
		return chat.Message{}, output.Usagef("cannot read the card file: %v", err)
	}
	cards, err := format.Cards(raw)
	if err != nil {
		return chat.Message{}, err
	}
	message.CardsV2 = cards
	return message, nil
}

// messageID is the name this message will have, if the caller wanted one.
func messageID(flags *sendFlags, space, text string) string {
	if flags.messageID != "" {
		return flags.messageID
	}
	if !flags.idempotent {
		return ""
	}
	return chat.DeriveMessageID(space, text, flags.threadKey)
}

func send(cmd *cobra.Command, r *output.Renderer, opened *profile.Open, req chat.SendRequest) error {
	sent, err := opened.Transport.Send(cmd.Context(), req)

	// A dry run comes back here rather than being decided above, because the
	// decision not to send is the client's and this is where its answer
	// arrives. A command that failed to handle this would report an error and
	// send nothing, which is the right way round for a mistake to fail;
	// TestEveryWriteCommandHonoursDryRun is what stops one shipping.
	if dry, ok := errors.AsType[*chat.DryRun](err); ok {
		// stdout carries the request and nothing else, because SPEC.md §10.2
		// asks for the exact request and a profile line is not part of one.
		// Which profile it resolved to is still worth saying, and stderr is
		// where everything that is not the result goes.
		r.Note("through profile %q. Nothing was sent.", opened.Name)
		return r.Block(dry.Request, dry.Request.Text())
	}
	if err != nil {
		return err
	}

	result := sendResult{
		Message:    sent.Name,
		Space:      spaceOf(opened, req, sent),
		CreateTime: sent.CreateTime,
		Profile:    opened.Name,
	}
	if sent.Thread != nil {
		result.Thread = sent.Thread.Name
	}

	fields := output.Fields{
		{Label: "space", Value: result.Space},
		{Label: "profile", Value: result.Profile},
	}
	if result.Message != "" {
		fields = append(output.Fields{{Label: "message", Value: result.Message}}, fields...)
	}
	return r.Result(result, fields)
}

// spaceOf is where the message went, from the most reliable source available.
//
// The response is preferred because it is the far end saying where it put the
// message, which is the only account of it that cannot be wrong about the
// request having been rewritten on the way. A webhook's own space is next, then
// the target as given.
func spaceOf(opened *profile.Open, req chat.SendRequest, sent *chat.Message) string {
	if sent.Space != nil && sent.Space.Name != "" {
		return sent.Space.Name
	}
	if name := sent.Name; name != "" {
		if space, _, ok := strings.Cut(name, "/messages/"); ok {
			return space
		}
	}
	if space, fixed := transport.SpaceOf(opened.Transport); fixed {
		return space
	}
	return req.Space
}

// verboseLog is the logger the transport gets, or nil.
//
// nil is how internal/chat is told not to format lines nobody asked for, and
// passing the renderer unconditionally would mean --verbose was on for
// everybody.
func verboseLog(opts *Options, r *output.Renderer) chat.Logger {
	if !opts.Verbose {
		return nil
	}
	return r
}
