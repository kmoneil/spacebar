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

package transport_test

import (
	"testing"

	"github.com/kmoneil/spacebar/internal/config"
	"github.com/kmoneil/spacebar/internal/transport"
)

// TestTheWebhookRefusesEverythingItCannotDo is the same claim from the other
// side. A webhook can post and can do nothing else, and the list of what it
// refuses grows with the interface.
func TestTheWebhookRefusesEverythingItCannotDo(t *testing.T) {
	caps := transport.CapabilitiesFor(config.TransportWebhook)

	for _, want := range []transport.Capability{
		transport.CanRead, transport.CanReadMembers, transport.CanListSpaces,
		transport.CanEdit, transport.CanDelete, transport.CanReact,
		transport.CanUpload, transport.CanResolveDM,
	} {
		if caps.Has(want) {
			t.Errorf("a webhook claims %v, and it is a URL that posts to one space", want)
		}
	}
	for _, want := range []transport.Capability{transport.CanSend, transport.CanSendCards, transport.CanThread} {
		if !caps.Has(want) {
			t.Errorf("a webhook does not claim %v, and posting is the whole of what it is for", want)
		}
	}
}
