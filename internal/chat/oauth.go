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

package chat

import (
	"net/http"
	"time"
)

// TokenHTTPClient is the client golang.org/x/oauth2 makes the token request
// with.
//
// It is here, in the package that owns every other request, because the token
// request is the one this repository does not build. x/oauth2 constructs it
// itself, in its own tree, carrying the client secret and either an
// authorization code or a refresh token, and the gate in internal/lint cannot
// see it: no import of net/http appears anywhere near internal/auth, which is
// the point of the gate and also its blind spot.
//
// What can be done about that is not a gate but a construction. x/oauth2 takes
// the client to use from the context, so it is handed one configured here, and
// internal/auth passes the value along without ever naming the type.
//
// The redirect policy is the reason this is worth a function rather than a
// zero-value client. A 3xx on a token request would resend the POST, and the
// POST body is the client secret and the code. net/http strips an Authorization
// header across origins and knows nothing about a body, so the credential would
// go wherever the redirect pointed. No 3xx is followed, exactly as on the Chat
// path.
func TokenHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &http.Client{
		CheckRedirect: refuseRedirect,

		// A whole-request timeout rather than a per-attempt one, because
		// x/oauth2 does its own retrying and there is no loop here to bound.
		Timeout: timeout,
	}
}
