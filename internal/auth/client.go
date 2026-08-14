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

package auth

import (
	"encoding/json"
	"strings"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/meta"
	"github.com/kmoneil/spacebar/internal/output"
)

// ClientSecretRef names the OAuth client secret inside a profile's credentials.
const ClientSecretRef = "client-secret"

// Client is an OAuth client somebody created in their own Cloud project.
type Client struct {
	ID     string
	Secret string

	// Project is reported back so that somebody with several can see which one
	// this came from. It is not used for anything.
	Project string
}

// consoleClient is the file the Cloud console hands you.
//
// Only two fields of it are read, and that is the point rather than laziness.
// The file also carries auth_uri and token_uri, and honouring those would let a
// doctored file send the consent screen and the client secret somewhere else:
// the endpoints are constants in this repository for exactly that reason. Every
// other key is ignored, including redirect_uris, which describes what the
// console registered rather than what this tool will ask for.
type consoleClient struct {
	Installed *consoleClientBody `json:"installed"`
	Web       *consoleClientBody `json:"web"`
}

type consoleClientBody struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	ProjectID    string `json:"project_id"`
}

// ParseClient reads the JSON the Cloud console downloads for an OAuth client.
//
// Accepting that file directly is worth the parser. The alternative is asking
// somebody to copy two long opaque strings out of a browser and into a
// terminal, which is where a truncated paste comes from, and the file is a real
// artifact they already have rather than a format invented here.
func ParseClient(raw []byte) (*Client, error) {
	var file consoleClient
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, clientErr("that is not the JSON the console downloads for an OAuth client: %v", err)
	}

	switch {
	case file.Installed != nil:
		return clientFrom(file.Installed)

	case file.Web != nil:
		// Worth naming rather than refusing generically. A web client cannot
		// redirect to a loopback address with a port chosen at runtime, which
		// is the whole of how this authorizes, and somebody who picked the
		// wrong type in the console has no way to know that from a parse error.
		return nil, clientErr("that is a web application client, and this needs a desktop one.\n" +
			"A desktop client is the type that may redirect to 127.0.0.1 on a port chosen when the " +
			"command runs, which is how the authorization comes back.\n" +
			"Create another in the console, choosing Desktop app as the application type.")
	}

	return nil, clientErr("that JSON has neither an %q nor a %q section, so it is not an OAuth client file.",
		"installed", "web")
}

func clientFrom(body *consoleClientBody) (*Client, error) {
	if body.ClientID == "" {
		return nil, clientErr("that file has no client_id in it.")
	}
	if body.ClientSecret == "" {
		// Not fatal in principle, since RFC 8252 says a native-app secret is
		// not confidential, but Google issues one for every desktop client and
		// its absence means a truncated or hand-edited file.
		return nil, clientErr("that file has a client_id and no client_secret, which a desktop client always has.\n" +
			"It may have been truncated. Download it again.")
	}
	return &Client{ID: body.ClientID, Secret: body.ClientSecret, Project: body.ProjectID}, nil
}

// SaveClient records an OAuth client against a profile.
//
// The secret goes to the keyring and the identifier goes in the configuration
// file, which looks inconsistent and is the rule from SPEC.md §5.3 applied
// exactly. A client ID is in the browser's address bar during consent, so
// anybody who has ever authorized has seen it. A secret is not, and while RFC
// 8252 is right that a native-app secret is not confidential and none of this
// tool's security rests on it, somebody who created a client in their own Cloud
// project did not agree to keep it in a file they might paste into an issue.
//
// The secret is written before the configuration, so that a failure between
// them leaves a credential nothing refers to rather than a profile that looks
// configured and fails at the next command.
func (s *Store) SaveClient(cfg *config.Config, profileName string, client *Client) error {
	if err := config.CheckProfileName(profileName); err != nil {
		return err
	}

	ref := Ref(profileName, ClientSecretRef)
	if err := s.Set(ref, client.Secret); err != nil {
		return err
	}

	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	profile := cfg.Profiles[profileName]
	profile.ClientID = client.ID
	profile.ClientSecretRef = ref
	if profile.Transport == "" {
		// A profile that only has a client has not been authorized yet, and
		// user OAuth is the only thing a client is for.
		profile.Transport = config.TransportUserOAuth
	}
	cfg.Profiles[profileName] = profile

	return nil
}

// SetupState is what `auth setup` found before it said anything.
type SetupState struct {
	// Profile is the profile being set up.
	Profile string

	// HasClient is whether a client is already resolvable for it, from any rung
	// of the ladder.
	HasClient bool

	// ClientID is the one that would be used. Not a secret: it is in the
	// browser's address bar during consent.
	ClientID string

	// FromBuild is whether that client came from the binary rather than from
	// this machine's configuration, which is the difference between an official
	// release and a build from source.
	FromBuild bool
}

// Inspect reports what is already configured, without changing anything.
func (s *Store) Inspect(profileName string, profile config.Profile) (SetupState, error) {
	id, _, err := s.ClientCredentials(profile)
	if err != nil {
		return SetupState{Profile: profileName}, err
	}

	return SetupState{
		Profile:   profileName,
		HasClient: id != "",
		ClientID:  id,
		FromBuild: id != "" && id == builtInClientID() && profile.ClientID == "",
	}, nil
}

// builtInClientID is what a release build was linked with, or the empty string.
// Wrapped so that the comparison above reads as a question rather than as a
// reach into another package.
func builtInClientID() string { return meta.DefaultClientID }

// SendOnlyScopes is the narrowest set that can still post a message.
//
// SPEC.md §6.4 makes this a real supported mode rather than a curiosity: a
// narrower scope materially improves the odds that an administrator approves
// the application at all, and for the population this tool exists for that is
// the difference between working and not. Somebody who only needs to post
// alerts should not have to ask for permission to read everything.
var SendOnlyScopes = []string{ScopeSendOnly}

// DefaultScopes are what an unqualified authorization asks for.
//
// Send, edit and delete messages, and list spaces so that a target can be
// resolved by name. Deliberately not chat.spaces, which permits creating spaces
// and looking up direct messages, because neither is something this tool does
// yet and a scope requested before it is needed is a scope an administrator has
// to approve for no reason.
var DefaultScopes = []string{ScopeMessages, ScopeSpacesRO}

// ScopeNames shortens scopes for display. The prefix is the same on every one
// of them and says nothing.
func ScopeNames(scopes []string) string {
	short := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		short = append(short, scope[strings.LastIndex(scope, "/")+1:])
	}
	return strings.Join(short, " ")
}

func clientErr(format string, a ...any) error {
	return output.Errorf("OAUTH_CLIENT", output.ExitUsage, format, a...)
}
