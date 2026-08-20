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
	"path/filepath"
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
// Not the message limit and never was. chat.CheckMessageText holds that, from a
// measurement, and refuses at roughly 32,000 bytes before any request is made.
// This is a bound on how much of a stream is pulled into memory when somebody
// pipes the wrong file in, which is why it sits far above the real cap rather
// than at it: reading an unbounded stream is a failure with no upside, and
// guessing at the cap here would be a second copy of a number that is measured
// somewhere else.
const maxBodyBytes = 1 << 20

// sendFlags are what `send` was asked for.
type sendFlags struct {
	md         bool
	mentions   []string
	threadKey  string
	messageID  string
	idempotent bool
	cardFile   string

	// replyTo is registered and refused. Nothing this build sends can carry it:
	// chat.SendRequest has no field a reply target reaches, so a message sent
	// with this flag would arrive as a new top-level message. See check.
	replyTo string

	// file is registered and implemented. It was in the same sentence as
	// replyTo until 2026-08-20, three milestones after uploadAttachment gave it
	// a caller.
	file string

	// contentType overrides what --file's bytes are declared as.
	contentType string

	// refresh busts the resolver's cached space list for this one invocation.
	refresh bool
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
  ` + meta.AppName + ` send --mention a@b.com 'deploy done'  # notify somebody

--mention takes an address or a users/NNN and puts it in front of the body,
repeatable, in the order given. Chat resolves the address itself, so nothing is
looked up here: this needs no extra scope and works on a webhook.

An address that matches nobody is not refused, because the API does not refuse
it. It answers 200 and posts the message with <users/> where the mention should
be, notifying nobody, so the only place the failure is visible is the body that
comes back. That is checked, and it is a warning on stderr rather than a
failure, because the message did post and a non-zero exit would say it did not.

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
	f.StringArrayVar(&flags.mentions, "mention", nil,
		"mention somebody by address or users/NNN, prepended to the body; repeatable")

	// Registered rather than left out, and this is a deliberate choice about
	// what a person learns from a failure. An unregistered flag is exit 2,
	// "unknown flag --file", which says this tool cannot do attachments at all.
	// A registered one is exit 5 naming the capability and the profile, which
	// says this profile cannot, and that is both true and actionable.
	f.StringVar(&flags.file, "file", "", "attach a file (requires a profile that can upload)")
	f.StringVar(&flags.contentType, "content-type", "",
		"declare --file as this media type instead of detecting it from its bytes")
	f.StringVar(&flags.replyTo, "reply-to", "", "not implemented in this build; use --thread-key to thread a message")
	addRefreshFlag(cmd, &flags.refresh)

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

	// Resolution runs after the capability check and before anything is built.
	// After, because "this profile cannot send" is a better first answer than
	// "no space is called that"; before, because a webhook compares the target
	// against its own space and an alias has to be a space name by then.
	target, err = opened.Resolve(cmd.Context(), target, flags.refresh)
	if err != nil {
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

	// The upload goes first and is a request of its own, so a send with an
	// attachment is two calls rather than one. That is the API's shape and not
	// a choice: the bytes are exchanged for a token, and the token is what a
	// message can carry. A failure here means nothing was posted, which is the
	// right order for it: an attachment that failed to upload should not become
	// a message with the text and no file.
	token, err := uploadAttachment(cmd, r, opened, target, flags.file, flags.contentType)
	if dry, ok := errors.AsType[*chat.DryRun](err); ok {
		return dryRunUpload(r, opened, dry)
	}
	if err != nil {
		return err
	}

	req := chat.SendRequest{
		Space:     target,
		Message:   message,
		ThreadKey: flags.threadKey,
		MessageID: messageID(flags, target, message.Text),
		Attach:    token,
	}

	return send(cmd, r, opened, req)
}

// check refuses what cannot work, before anything is read or resolved.
//
// Two contradictions and one flag this build does not have. All three are
// answered here rather than further down, because none of them needs a profile
// to decide and a refusal that costs a keyring read is a refusal that costs
// more than it has to.
func (f *sendFlags) check() error {
	if err := f.checkReplyTo(); err != nil {
		return err
	}
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

// checkReplyTo refuses the one flag this build registers and cannot honour.
//
// It is registered rather than left out, which is the reason --file and --card
// are, and the reason survives the fact that this one does nothing: an
// unregistered flag is exit 2 "unknown flag: --reply-to", which says this tool
// has no notion of replying to a message, and that is a different and wronger
// answer than "it has one and this build cannot do it".
//
// What it was doing until 2026-08-20 was worse than either. The flag's value
// was read once, to choose transport.CanRead as the capability to require, and
// then dropped: chat.SendRequest has no field a reply target reaches. On a
// webhook the capability check refused it, which is the population it was
// written for and the only one anybody tested. On any profile that can read it
// passed the check, posted a new top-level message, and exited 0 printing the
// success fields, so somebody replying in a busy space got a detached message
// and no way to tell. A false report is worse than a refusal, and this was one
// for four milestones.
//
// Exit 5 rather than exit 2, and the reason is that exit 5 is what this
// invocation already returns. A webhook caller gets it today from
// transport.Require, so refusing here at exit 2 would change what a real
// invocation answers, and an exit code never changes meaning. The message is
// where the difference lives: it does not say to use another profile, because
// no profile in this build has it.
func (f *sendFlags) checkReplyTo() error {
	if f.replyTo == "" {
		return nil
	}
	return output.Errorf("UNSUPPORTED", output.ExitUnsupported,
		"--reply-to names a message to reply to, and this build cannot do it. Nothing was sent.\n"+
			"There is nowhere for the value to go: a reply carries the thread the named message is "+
			"in, and no request this build makes has a field for one, so a message sent with this "+
			"flag would arrive as a new top-level message rather than as a reply.\n"+
			"Use --thread-key to group messages into a thread. Both transports can do that, and it "+
			"needs no read access.")
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
// SPEC.md §8.2: exit 5, naming the profile and the fix. Each flag here changes
// which capability the send needs, so the refusal can say which one is missing
// rather than reporting the generic inability to send.
//
// The original comment here said every flag was one no profile could honour
// yet, which is why registering them was worth doing: an unregistered --file is
// exit 2 "unknown flag", which says this tool cannot do attachments at all,
// where a registered one is exit 5 naming the profile. That was true in
// Milestone 2, when the only transport was a webhook. It stopped being true in
// Milestone 3 and nothing revisited it: --file is honoured on user OAuth and
// --card on a webhook, and --reply-to reached no request on any of them. The
// reasoning survives; the sentence about what this build can do did not.
func requireCapabilities(t transport.Transport, flags *sendFlags) error {
	if flags.file != "" {
		return transport.Require(t, "send --file", transport.CanUpload)
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
		// Not phrased as the message limit, because it is not one, and saying
		// so here was wrong: it named 1MB as what Chat accepts, which is
		// thirty-two times the measured cap. A body between the two is refused
		// by chat.CheckMessageText with the real number in it.
		return "", output.Usagef("the message on stdin is over %d bytes, which is more than this reads into memory.\n"+
			"It is also far past what the API accepts, so it is not a message that could have been sent.", maxBodyBytes)
	}

	// Trailing newlines go, because a shell adds one and nobody means it. What
	// is inside the message is untouched: a blank line between paragraphs is
	// something somebody wrote.
	return strings.TrimRight(string(body), "\n"), nil
}

// buildMessage turns the body and the flags into the message to send.
func buildMessage(body string, flags *sendFlags, r *output.Renderer) (chat.Message, error) {
	text, warnings, err := format.Body(body, flags.md)
	if err != nil {
		return chat.Message{}, err
	}
	r.Warnings(warnings)

	text, err = withMentions(text, flags.mentions)
	if err != nil {
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

// dryRunUpload reports the upload a send with --file would have made, and stops
// there.
//
// This is the branch that was missing, and its absence was the whole of the
// bug: uploadAttachment is the third place a *chat.DryRun can come from, and
// the only one that did not handle it. The error went back through runSend
// untouched, reached output.Report, and was rendered as a generic failure at
// exit 1 with "dry run: the request below was not sent" and nothing below it.
// Nothing was sent, so it failed safe; what was broken was the report.
//
// It stops at one request rather than going on to show the message, and that is
// the decision rather than an omission. A send with an attachment is two calls,
// and the second one carries an upload token that this API returns from the
// first. There is no way to show the real second request without making the
// first, so the choices are to print an exact request and say what follows it,
// or to print an approximation of a request that would not be sent in that
// form. This tool does not approximate, and `profile set-webhook --verify`
// already set the precedent going the other way: show the request the client
// actually stopped, and say plainly what else did not happen.
func dryRunUpload(r *output.Renderer, opened *profile.Open, dry *chat.DryRun) error {
	r.Note("through profile %q. Nothing was uploaded and nothing was sent.", opened.Name)

	// Said before the request rather than after it, because stdout is the
	// request and a reader who stops at the end of stdout has stopped reading.
	r.Note("this is the first of two requests. The message would follow, carrying the " +
		"upload token this one returns, and it cannot be shown without making this request.")

	return r.Block(dry.Request, dry.Request.Text())
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

	r.Warnings(unresolvedMentions(sent.Text))

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

// spaceOf is where the message went, and the order here is the one thing in
// this file that is a security decision rather than a formatting one.
//
// What we asked for wins over what came back. A webhook is issued for one space
// and is the only thing that authenticates the request, so the API cannot put
// the message anywhere else and the URL is the fact; a response naming a
// different space is a server saying something that cannot be true. The threat
// model says plainly that a hostile space can lie about the data, and reporting
// a destination on its word is exactly that, in the one line a person reads to
// confirm where their message went.
//
// The response is used only where we did not say: a profile that reaches many
// spaces and a caller who let it choose. Then the message name is the only
// account there is.
func spaceOf(opened *profile.Open, req chat.SendRequest, sent *chat.Message) string {
	if space, fixed := transport.SpaceOf(opened.Transport); fixed {
		return space
	}
	if req.Space != "" {
		return req.Space
	}
	if space, _, ok := strings.Cut(sent.Name, "/messages/"); ok {
		return space
	}
	if sent.Space != nil {
		return sent.Space.Name
	}
	return ""
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

// uploadAttachment sends --file's bytes and returns the token a message
// attaches by, or nothing when there is no file.
//
// The file is opened before the upload and its size is taken from the handle
// rather than from a second stat, so the number checked against the limit is
// the number that will be read.
func uploadAttachment(cmd *cobra.Command, r *output.Renderer, opened *profile.Open,
	space, path, contentType string,
) (string, error) {
	if path == "" {
		return "", nil
	}

	file, err := os.Open(path)
	if err != nil {
		return "", output.Usagef("cannot read the file to attach: %v", err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return "", output.Usagef("cannot read the file to attach: %v", err)
	}
	if info.IsDir() {
		return "", output.Usagef("%q is a directory, and an attachment is a file.", path)
	}

	ref, err := opened.Transport.Upload(cmd.Context(), chat.UploadRequest{
		Space:       space,
		Filename:    filepath.Base(path),
		Body:        file,
		Size:        info.Size(),
		ContentType: contentType,
	})
	if err != nil {
		return "", err
	}

	// The token, not the resource name. A just-uploaded attachment is attached
	// by the token the upload returned; the resource name is what a download
	// takes later, off a message that already carries it.
	r.Note("uploaded %s (%d bytes)", filepath.Base(path), info.Size())
	return ref.AttachmentUploadToken, nil
}

// withMentions prepends a mention of each address to the body.
//
// Prepended, and in the order they were given, because that is where a person
// writing this by hand would put them and because the alternative is deciding
// where in somebody's sentence they meant. The body is not searched for a place
// to insert them: a message is never rewritten to make a flag fit.
//
// Every one goes through format.Mention, which is the only place a mention is
// built, so an address that cannot sit inside the wrapper is refused at exit 2
// before anything is sent rather than posted as literal text.
//
// A bare address is accepted as well as a resource name, because that is what
// somebody will type. Chat resolves it server side: `<users/a@b.com>` comes
// back as a USER_MENTION annotation naming the numeric id, measured against a
// real space on 2026-08-17. There is no lookup here, which is why this needs no
// scope and works on a webhook.
func withMentions(text string, addresses []string) (string, error) {
	if len(addresses) == 0 {
		return text, nil
	}

	var b strings.Builder
	for _, address := range addresses {
		name := address
		if !strings.HasPrefix(name, "users/") {
			name = "users/" + name
		}
		mention, err := format.Mention(name)
		if err != nil {
			return "", err
		}
		b.WriteString(mention)
		b.WriteString(" ")
	}
	b.WriteString(text)
	return b.String(), nil
}

// emptyMention is what Chat leaves behind when a mention names nobody.
//
// Measured on 2026-08-17. `--mention nobody@example.com` is accepted, answered
// 200, and posted: the address is dropped and the body arrives reading
// "<users/> the rest of the message", with no USER_MENTION annotation and
// nobody notified.
const emptyMention = "<users/>"

// unresolvedMentions warns when a mention was asked for and did not land.
//
// There is no way to refuse this before sending. Nothing in the Chat API says
// whether an address is a person, and the only endpoint that would answer
// needs a scope this does not require and has a side effect of its own. So the
// failure is detected afterwards, from the body the API echoes back, which is
// the one place it is visible.
//
// A warning and exit 0 rather than a failure, which is the same answer `--md`
// gives when a table cannot be represented: the message was posted, so a
// non-zero exit would say it was not, and this tool may not write the result to
// stdout on a failure. What must not happen is silence. Somebody who asked to
// notify a colleague and did not is entitled to know before they walk away.
func unresolvedMentions(text string) []string {
	if !strings.Contains(text, emptyMention) {
		return nil
	}
	return []string{
		"a --mention address did not match anybody, so it was dropped: the message posted " +
			"with " + emptyMention + " in it and nobody was notified. Chat accepts an unknown " +
			"address and answers 200, so this cannot be refused before sending. Check the address.",
	}
}
