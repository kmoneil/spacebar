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
	return &cobra.Command{
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
			return runAuthLogin(cmd, opts)
		},
	}
}

func runAuthLogin(cmd *cobra.Command, opts *Options) error {
	r := renderer(cmd, opts)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name, err := loginProfileName(cfg, opts.Profile)
	if err != nil {
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

		// The narrow set, per SPEC.md §6.4. A blanket scope materially reduces
		// the odds of an administrator approving the application at all, which
		// for the people this tool is for is the difference between working and
		// not. Choosing a narrower one still is what --send-only will be.
		Scopes:     []string{auth.ScopeMessages, auth.ScopeSpacesRO},
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

The token is deleted from the keyring and from the fallback file. The profile
stays, so authorizing again needs no other setup.

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

	store, err := auth.New()
	if err != nil {
		return err
	}

	// A token that was not there is not a failure. The profile is unauthorized
	// either way, which is what was asked for, and reporting "there was nothing
	// to delete" would make an interrupted logout impossible to finish.
	_ = store.DeleteToken(name)
	r.Warnings(store.Warnings())

	return r.Result(map[string]any{"profile": name, "authorized": false},
		output.Fields{{Label: "logged out", Value: name}})
}

// noClientErr is the message SPEC.md §6.1 specifies for a build with no client.
//
// It deliberately does not name `auth setup`, which the spec's wording does,
// because that command does not exist in this build and a message sending
// somebody from one dead end to another is worse than the first dead end. The
// milestone that adds it puts the pointer back.
func noClientErr() error {
	return output.Errorf("NO_CLIENT", output.ExitUsage,
		"no OAuth client is configured, so there is nothing to authorize against.\n"+
			"A build from source has none on purpose: a client committed to an open repository is a "+
			"client every fork uses, spending one quota and showing one consent screen.\n"+
			"Create one in your own Cloud project, with an Internal user type if your organization "+
			"allows it, and set %s and %s.",
		config.Env("CLIENT_ID"), config.Env("CLIENT_SECRET"))
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
