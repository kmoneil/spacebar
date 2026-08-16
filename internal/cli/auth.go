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
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmoneil/spacebar/internal/auth"
	"github.com/kmoneil/spacebar/internal/chat"
	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

func newAuthCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authorize this machine to act as you",
		Long: `Authorize this machine to act as you.

This is the transport that can read. A webhook posts as a bot to one space and
cannot read anything; authorizing acts as your own account, with the narrow set
of permissions the commands you use actually need.

The credential never touches the configuration file. It goes to the OS keyring,
or to a mode 0600 file beside the configuration on a machine that has no
keyring, which says so every time it is used.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newAuthSetupCmd(opts),
		newAuthLoginCmd(opts),
		newAuthStatusCmd(opts),
		newAuthLogoutCmd(opts),
	)
	return cmd
}

// loginResult is the --json shape of a completed authorization.
//
// The token is not in it and never will be. What a caller can act on is which
// profile now has one, what it is allowed to do, and when it might stop
// working.
type loginResult struct {
	Profile   string   `json:"profile"`
	Transport string   `json:"transport"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at,omitempty"`
}

func newAuthLoginCmd(opts *Options) *cobra.Command {
	var sendOnly bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authorize a profile through a browser",
		Long: `Authorize a profile through a browser.

A browser opens, you consent, and the authorization comes back to a listener on
127.0.0.1. Nothing is sent anywhere else: the listener is bound to the loopback
address as a literal, never by the name "localhost", which resolves through
whatever the machine's resolver says.

If the browser cannot be opened, which is the normal case over SSH and in a
container, the URL is printed for you to open yourself. The flow waits three
minutes for an answer and then gives up, changing nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthLogin(cmd, opts, sendOnly)
		},
	}

	// A real supported mode rather than a curiosity, per SPEC.md §6.4. A
	// narrower scope materially improves the odds that an administrator
	// approves the application at all, and somebody who only needs to post
	// alerts should not have to ask for permission to read everything in every
	// space they are a member of.
	cmd.Flags().BoolVar(&sendOnly, "send-only", false,
		"ask for permission to post and nothing else")

	return cmd
}

func runAuthLogin(cmd *cobra.Command, opts *Options, sendOnly bool) error {
	r := renderer(cmd, opts)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name, err := loginProfileName(cfg, opts.Profile)
	if err != nil {
		return err
	}
	if err := checkAuthorizable(cfg, name, "auth login"); err != nil {
		return err
	}

	store, err := auth.New()
	if err != nil {
		return err
	}
	clientID, clientSecret, err := store.ClientCredentials(cfg.Profiles[name])
	r.Warnings(store.Warnings())
	if err != nil {
		return err
	}
	if clientID == "" {
		return noClientErr()
	}

	flow := &auth.Flow{
		ClientID:     clientID,
		ClientSecret: clientSecret,

		// The narrow set, per SPEC.md §6.4. A blanket scope is never requested,
		// and --send-only narrows it further to the one permission that can
		// still post.
		Scopes:     scopesFor(sendOnly),
		Report:     r,
		HTTPClient: chat.TokenHTTPClient(opts.Timeout),
	}

	if opts.DryRun {
		return dryRunLogin(r, name, flow)
	}

	token, err := flow.Login(cmd.Context())
	if err != nil {
		return err
	}
	return storeAuthorization(r, cfg, store, name, clientID, token)
}

// loginProfileName is which profile is being authorized.
//
// Different from every other command's resolution, and deliberately: this is the
// one that may be creating the profile, so a name that is not configured yet is
// not an error. What is an error is having no name at all, because a default
// invented here would authorize something the caller did not name.
func loginProfileName(cfg *config.Config, flagValue string) (string, error) {
	if flagValue != "" {
		if err := config.CheckProfileName(flagValue); err != nil {
			return "", err
		}
		return flagValue, nil
	}
	if cfg.DefaultProfile != "" {
		return cfg.DefaultProfile, nil
	}
	return "", output.Usagef("which profile should this authorize?\n"+
		"  %s auth login --profile work", meta.AppName)
}

// checkAuthorizable refuses a profile whose transport cannot hold an
// authorization.
//
// Every command in this group assumed one that could, and none of them said so,
// which the Milestone 3 exit sweep found by running them against a webhook
// profile. Each was wrong in its own way and none of them failed:
// `auth setup` filed an OAuth client and secret against a profile whose
// transport is webhook and then printed "now authorize it";
// `auth login` reported "no OAuth client is configured", which sent somebody to
// spend five minutes in the Cloud console on a reason that was not the reason;
// and `auth logout` reported "logged out" for a profile that held nothing to
// forget, while the credential a person means when they say that, the webhook
// URL, stayed exactly where it was.
//
// A name that is not configured is not refused. That is the ordinary way a
// user-OAuth profile comes into existence: `auth setup --profile work` on a
// fresh machine creates it, and demanding it exist first would refuse the
// invocation the documentation tells people to type.
func checkAuthorizable(cfg *config.Config, name, command string) error {
	profile, configured := cfg.Profiles[name]
	if !configured || profile.Transport == config.TransportUserOAuth {
		return nil
	}

	return output.Errorf("UNSUPPORTED", output.ExitUnsupported,
		"%q needs a profile that can hold an authorization, and profile %q is an incoming webhook.\n"+
			"A webhook is a URL rather than a token: it is issued for one space, it authenticates by "+
			"being secret, and there is nothing about it to log in to or out of.\n"+
			"To authorize as yourself, use a new profile:\n"+
			"  %s auth setup --profile NAME < client_secret.json\n"+
			"To remove this one and the URL behind it: %s profile rm %s",
		command, name, meta.AppName, meta.AppName, name)
}

// storeAuthorization writes the token and records the profile it belongs to.
//
// The token goes first, for the reason m2-11 gave for a webhook: a failure
// between the two leaves a credential nothing refers to, which is invisible,
// rather than a profile that looks configured and fails at the next command.
func storeAuthorization(r *output.Renderer, cfg *config.Config, store *auth.Store,
	name, clientID string, token *auth.Token,
) error {
	if err := store.SaveToken(name, token); err != nil {
		r.Warnings(store.Warnings())
		return err
	}

	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	profile := cfg.Profiles[name]
	profile.Transport = config.TransportUserOAuth
	profile.Scopes = token.Scopes

	// The client ID is recorded so that a later refresh uses the same one it was
	// issued for. It is not a secret: it is in the browser's address bar during
	// consent, which is why SPEC.md §6.1 calls the client a quota and reputation
	// boundary rather than a confidentiality one.
	profile.ClientID = clientID
	cfg.Profiles[name] = profile

	madeDefault := cfg.DefaultProfile == ""
	if madeDefault {
		cfg.DefaultProfile = name
	}
	if err := cfg.Save(); err != nil {
		r.Warnings(store.Warnings())
		return err
	}
	r.Warnings(store.Warnings())

	if madeDefault {
		r.Note("%q is now the default profile.", name)
	}

	result := loginResult{
		Profile:   name,
		Transport: string(config.TransportUserOAuth),
		Scopes:    token.Scopes,
	}
	if !token.Expiry.IsZero() {
		result.ExpiresAt = token.Expiry.Format(time.RFC3339)
	}

	return r.Result(result, output.Fields{
		{Label: "profile", Value: result.Profile},
		{Label: "transport", Value: result.Transport},
		{Label: "scopes", Value: joinScopes(token.Scopes)},
	})
}

// dryRunLogin shows what would happen and does none of it.
//
// A dry run must not consent. Issuing a token is a side effect at Google's end
// and on this machine, and somebody who typed --dry-run to find out what would
// happen would have had it happen. So no browser opens, no listener binds, and
// nothing is stored.
func dryRunLogin(r *output.Renderer, name string, flow *auth.Flow) error {
	r.Note("nothing was authorized and nothing was stored.")

	return r.Result(struct {
		DryRun    bool     `json:"dry_run"`
		Profile   string   `json:"profile"`
		Transport string   `json:"transport"`
		ClientID  string   `json:"client_id"`
		Scopes    []string `json:"scopes"`
		Endpoint  string   `json:"endpoint"`
	}{
		DryRun:    true,
		Profile:   name,
		Transport: string(config.TransportUserOAuth),

		// Not a secret, and the useful half of what a dry run is asked here:
		// somebody debugging a consent failure needs to know which client it
		// would have used.
		ClientID: flow.ClientID,
		Scopes:   flow.Scopes,
		Endpoint: auth.AuthEndpoint,
	}, output.Fields{
		{Label: "profile", Value: name},
		{Label: "transport", Value: string(config.TransportUserOAuth)},
		{Label: "client-id", Value: flow.ClientID},
		{Label: "scopes", Value: joinScopes(flow.Scopes)},
		{Label: "consent", Value: auth.AuthEndpoint},
	})
}

func newAuthStatusCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report what a profile is authorized to do",
		Long: `Report what a profile is authorized to do.

Nothing here touches the network or refreshes anything. It is the command to run
when something is wrong, so it answers on a machine whose keyring is readable
and whose connection is not.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthStatus(cmd, opts)
		},
	}
}

func runAuthStatus(cmd *cobra.Command, opts *Options) error {
	r := renderer(cmd, opts)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name, profile, err := cfg.Active(opts.Profile)
	if err != nil {
		return err
	}

	store, err := auth.New()
	if err != nil {
		return err
	}

	// A missing token is a status, not a failure. "not authorized" is the
	// answer to the question that was asked, and exiting non-zero for it would
	// make a script unable to ask.
	token, _ := store.LoadToken(name)
	r.Warnings(store.Warnings())

	status := auth.StatusOf(name, string(profile.Transport), token, time.Now())
	if status.Warning != "" {
		r.Warn("%s", status.Warning)
	}

	fields := output.Fields{
		{Label: "profile", Value: status.Profile},
		{Label: "transport", Value: status.Transport},
		{Label: "authorized", Value: yesNo(!status.NeedsReauth)},
	}
	if len(status.Scopes) > 0 {
		fields = append(fields, output.Field{Label: "scopes", Value: joinScopes(status.Scopes)})
	}
	if status.DaysRemaining != nil {
		fields = append(fields, output.Field{
			Label: "days-remaining",
			Value: formatDays(*status.DaysRemaining),
		})
	}
	return r.Result(status, fields)
}

func newAuthLogoutCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget a profile's authorization",
		Long: `Forget a profile's authorization.

The token is deleted from the keyring and from the fallback file, and so is the
cached list of spaces this profile could reach. The profile stays, so
authorizing again needs no other setup.

There is no confirmation, unlike removing a profile: an authorization is
recoverable by consenting again, and a webhook URL is not.

This does not tell Google to forget anything. Revoking the grant is done from
your Google account's security settings, and it is the thing to do if a machine
is lost rather than retired.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthLogout(cmd, opts)
		},
	}
}

func runAuthLogout(cmd *cobra.Command, opts *Options) error {
	r := renderer(cmd, opts)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name, _, err := cfg.Active(opts.Profile)
	if err != nil {
		return err
	}
	if err := checkAuthorizable(cfg, name, "auth logout"); err != nil {
		return err
	}

	store, err := auth.New()
	if err != nil {
		return err
	}

	// A token that was not there is not a failure. The profile is unauthorized
	// either way, which is what was asked for, and reporting "there was nothing
	// to delete" would make an interrupted logout impossible to finish.
	//
	// That reasoning holds for a profile that could have held one, which is why
	// the transport is checked above rather than folded in here. "There was no
	// token" and "this kind of profile never has one" look identical from this
	// line and mean different things to the person who typed it.
	_ = store.DeleteToken(name)
	r.Warnings(store.Warnings())
	forgetSpaces(r, name)

	return r.Result(map[string]any{"profile": name, "authorized": false},
		output.Fields{{Label: "logged out", Value: name}})
}

// noClientErr is the message SPEC.md §6.1 specifies for a build with no client.
//
// It named no command until the Milestone 3 exit sweep, on the grounds that
// `auth setup` did not exist and sending somebody from one dead end to another
// is worse than the first dead end. Milestone 3 added it, and the comment
// saying otherwise outlived the condition it described, which is how a refusal
// goes on giving advice from a version of the tool that no longer exists.
//
// It names the same two commands, in the same order, as the message
// internal/profile raises for the same condition, because there were two
// wordings of one fact and they had already drifted: one sent somebody to the
// Cloud console with two environment variables, the other to `auth setup`. The
// environment variables still work and are still documented; they are not the
// first thing to offer somebody who has just been told there is nothing to
// authorize against.
func noClientErr() error {
	return output.Errorf("NO_CLIENT", output.ExitUsage,
		"no OAuth client is configured, so there is nothing to authorize against.\n"+
			"A build from source has none on purpose: a client committed to an open repository is a "+
			"client every fork uses, spending one quota and showing one consent screen.\n"+
			"Create one in your own Cloud project, with an Internal user type if your organization "+
			"allows it, and store it with:\n"+
			"  %s auth setup --profile NAME < client_secret.json\n"+
			"Run '%s auth setup' on its own to see how to create it, or set %s and %s.",
		meta.AppName, meta.AppName, config.Env("CLIENT_ID"), config.Env("CLIENT_SECRET"))
}

// scopesFor is what this authorization will ask permission for.
func scopesFor(sendOnly bool) []string {
	if sendOnly {
		return auth.SendOnlyScopes
	}
	return auth.DefaultScopes
}

func joinScopes(scopes []string) string {
	short := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		// The prefix is the same on every one of them and says nothing.
		short = append(short, scope[strings.LastIndex(scope, "/")+1:])
	}
	return strings.Join(short, " ")
}

func yesNo(ok bool) string {
	if ok {
		return "yes"
	}
	return "no"
}

func formatDays(days float64) string {
	return strconv.FormatFloat(days, 'f', 1, 64)
}

// newAuthSetupCmd walks somebody through creating their own OAuth client.
//
// The shape of this command is the answer to the question the card left open:
// what does setup do when it cannot open a browser, or the caller is on a
// remote shell. The answer is that it must be fully useful in exactly that
// case, because that is not an edge case here. The people who need their own
// OAuth client are the ones whose organization blocks third-party
// applications, and they are disproportionately on managed laptops, jump hosts
// and CI runners.
//
// So it is not a wizard. It prompts for nothing, blocks on nothing, and needs
// no terminal: it prints what to do and then accepts the file the console gives
// you. That also happens to be the only honest design, because nothing here can
// create an OAuth client for you. A desktop client is made in the Cloud console
// and there is no API and no gcloud command for it, which was checked rather
// than assumed.
func newAuthSetupCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Print how to create your own OAuth client, and store one",
		Long: `Print how to create your own OAuth client, and store one.

Nothing here creates anything for you: a desktop OAuth client is made in the
Google Cloud console, and there is no API for it. What this does is tell you
exactly what to click, and then take the JSON the console gives you.

  ` + meta.AppName + ` auth setup                          # print the walkthrough
  ` + meta.AppName + ` auth setup < client_secret.json     # store what the console downloaded

The file is read from stdin and never from an argument, because it holds the
client secret and an argument lands in the shell history and in the process
list. Only the identifier and the secret are read out of it; the endpoints in
it are ignored, because a file that could redirect the consent screen would be
a file that could collect your authorization.

See docs/ADMIN.md for the part an administrator has to do.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthSetup(cmd, opts)
		},
	}
}

func runAuthSetup(cmd *cobra.Command, opts *Options) error {
	r := renderer(cmd, opts)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	store, err := auth.New()
	if err != nil {
		return err
	}

	body, err := clientFileFrom(cmd.InOrStdin(), r.IsInteractive())
	if err != nil {
		return err
	}

	// Printing the walkthrough needs no profile, and demanding one would refuse
	// the exact invocation somebody types when they do not yet know what any of
	// this is: `auth setup` and nothing else. Storing a client does need one,
	// because it has to be filed against something.
	if len(body) == 0 {
		name := opts.Profile
		if name == "" {
			name = cfg.DefaultProfile
		}
		state, err := store.Inspect(name, cfg.Profiles[name])
		r.Warnings(store.Warnings())
		if err != nil {
			return err
		}
		return printWalkthrough(r, state)
	}

	name, err := loginProfileName(cfg, opts.Profile)
	if err != nil {
		return err
	}
	if err := checkAuthorizable(cfg, name, "auth setup"); err != nil {
		return err
	}
	client, err := auth.ParseClient(body)
	if err != nil {
		return err
	}
	if opts.DryRun {
		r.Note("nothing was stored.")
		return reportClient(r, name, client, true)
	}

	if err := store.SaveClient(cfg, name, client); err != nil {
		r.Warnings(store.Warnings())
		return err
	}
	if err := cfg.Save(); err != nil {
		r.Warnings(store.Warnings())
		return err
	}
	r.Warnings(store.Warnings())

	r.Note("now authorize it: %s auth login --profile %s", meta.AppName, name)
	return reportClient(r, name, client, false)
}

// maxClientFile bounds what is read from stdin. The console's file is under a
// kilobyte.
const maxClientFile = 64 << 10

// clientFileFrom reads the console's file, or reports that there is none.
//
// The interactive case returns nothing without touching the reader, and that is
// the difference between this command and every other one here that takes a
// credential. Found by running it: `auth setup` with no redirection blocked on
// stdin forever, so the command whose whole job is to print instructions showed
// a cursor and nothing else to somebody who typed it because they did not know
// what to do.
//
// `profile set-webhook` and `send -` do block at a terminal and are right to.
// Receiving a value is the whole of what they do, and both say so before they
// wait. This one's default is to print.
func clientFileFrom(in io.Reader, interactive bool) ([]byte, error) {
	if interactive {
		return nil, nil
	}
	return readClientFile(in)
}

func readClientFile(in io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(in, maxClientFile+1))
	if err != nil {
		return nil, output.Usagef("could not read the client file: %v", err)
	}
	if len(body) > maxClientFile {
		return nil, output.Usagef("what arrived on stdin is over %d bytes, which no client file is.", maxClientFile)
	}
	return []byte(strings.TrimSpace(string(body))), nil
}

func reportClient(r *output.Renderer, name string, client *auth.Client, dryRun bool) error {
	fields := output.Fields{
		{Label: "profile", Value: name},
		{Label: "client-id", Value: client.ID},
	}
	if client.Project != "" {
		fields = append(fields, output.Field{Label: "project", Value: client.Project})
	}
	if dryRun {
		fields = append(fields, output.Field{Label: "dry-run", Value: "nothing was stored"})
	}

	return r.Result(struct {
		Profile  string `json:"profile"`
		ClientID string `json:"client_id"`
		Project  string `json:"project,omitempty"`
		DryRun   bool   `json:"dry_run,omitempty"`
	}{Profile: name, ClientID: client.ID, Project: client.Project, DryRun: dryRun}, fields)
}

// printWalkthrough is the instructional half.
//
// It goes to stderr, all of it, because it is not a result: a caller running
// this in a script is not parsing prose, and stdout carries data or nothing.
// The exit code is 0 because printing instructions is what was asked for.
func printWalkthrough(r *output.Renderer, state auth.SetupState) error {
	if state.HasClient {
		source := "this machine's configuration"
		if state.FromBuild {
			source = "this build"
		}
		r.Note("profile %q already has an OAuth client, from %s: %s",
			state.Profile, source, state.ClientID)
		r.Note("")
	}

	for _, line := range walkthrough(state.Profile) {
		r.Note("%s", line)
	}
	return nil
}

// walkthrough is the text, kept as data so that a test can assert on it and so
// that the wrapping is decided here rather than by a terminal.
//
// It describes the console in words rather than by screenshot, deliberately.
// A screenshot is wrong the first time the interface is refreshed and nobody
// can tell how long it has been wrong; a description of what to look for
// degrades gracefully.
func walkthrough(profile string) []string {
	// A placeholder rather than a guess, when nothing has been named yet. The
	// commands at the end are meant to be pasted, and one naming a profile the
	// reader did not choose is one they have to notice and edit.
	if profile == "" {
		profile = "NAME"
	}

	return []string{
		"Creating your own OAuth client, which takes about five minutes.",
		"",
		"Why: an organization that blocks third-party applications will block this one,",
		"and a client you create in your own Cloud project is not third-party to you. An",
		"Internal user type also avoids the seven-day refresh-token expiry that an",
		"External application in testing has.",
		"",
		"1. Open the Google Cloud console and choose or create a project.",
		"",
		"2. Enable the Google Chat API for it, under APIs & Services.",
		"   With the gcloud command that is: gcloud services enable chat.googleapis.com",
		"",
		"3. Configure the OAuth consent screen, under APIs & Services.",
		"   Choose Internal if your organization offers it. That is the option that",
		"   avoids both third-party app access control and the seven-day expiry.",
		"   External works and puts you in testing mode, where authorizations last",
		"   seven days and you must list yourself as a test user.",
		"",
		"4. Create the client, under APIs & Services then Credentials.",
		"   Create credentials, OAuth client ID, and choose Desktop app as the type.",
		"   Desktop is the type that may redirect to 127.0.0.1 on a port chosen when",
		"   the command runs, which is how the authorization comes back here.",
		"",
		"5. Download the JSON and give it to this command:",
		"     " + meta.AppName + " auth setup --profile " + profile + " < client_secret.json",
		"     " + meta.AppName + " auth login --profile " + profile,
		"",
		"If your organization also blocks you from creating a Cloud project, none of",
		"this will work and an incoming webhook is the path that needs nothing from an",
		"administrator: " + meta.AppName + " profile set-webhook NAME",
	}
}
