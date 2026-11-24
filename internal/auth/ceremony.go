package auth

import (
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/devXpro/homie/internal/auth/wire"
	"github.com/devXpro/homie/internal/store"
)

// ceremonyKind tells the four passkey ceremonies apart; a ceremony can only be finished by its own endpoint.
type ceremonyKind uint8

const (
	kindRegister   ceremonyKind = iota + 1 // POST /auth/register: a setup link or an invite
	kindLogin                              // POST /auth/login: discoverable sign-in
	kindStepUp                             // POST /auth/stepup: a fresh assertion in a session
	kindAddPasskey                         // POST /me/passkeys: another passkey for the signed-in person
)

// maxCeremonies bounds the pending ceremonies of each pool: the public ones (register, login) and the ones of a
// session (step-up, another passkey), so an anonymous flood can never crowd out a signed-in owner's step-up.
const maxCeremonies = 1000

// public reports whether anybody may begin a ceremony of this kind (no session needed).
func (k ceremonyKind) public() bool { return k == kindRegister || k == kindLogin }

// ceremony is the server side of a begun passkey ceremony (ADMIN-API §8: in memory, 5 minutes, single use).
type ceremony struct {
	kind    ceremonyKind
	rp      RelyingParty // the relying party it began with; finish verifies against it
	data    webauthn.SessionData
	expires time.Time

	person    store.Person // the person registering or asserting; for a new owner, the person to create (no ID yet)
	newPerson bool         // kindRegister: person does not exist yet and is created at finish
	sessionID string       // kindStepUp, kindAddPasskey: the session that began it

	tokenHash  string           // kindRegister: the setup or invite token, consumed at finish
	inviteKind store.InviteKind // kindRegister: setup or member, for the audit of a failed finish
	personName string           // kindRegister: a new name for the person; "" keeps it
	homeName   string           // kindRegister with a setup token: a new home name; "" keeps it
}

// ceremonies holds the pending ceremonies by id.
type ceremonies struct {
	mu sync.Mutex
	m  map[string]*ceremony
}

func newCeremonies() *ceremonies { return &ceremonies{m: map[string]*ceremony{}} }

// add stores c and returns its random id. When c's pool is full, a public ceremony takes the place of the oldest
// public one (a flood of begins cannot keep a real sign-in from starting), and a session's ceremony is refused (ok
// false; the per-session limits keep that pool far from full).
func (cs *ceremonies) add(c *ceremony, now time.Time) (id string, ok bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var (
		pool      int
		oldest    string
		oldestExp time.Time
	)
	for k, v := range cs.m {
		switch {
		case !now.Before(v.expires):
			delete(cs.m, k)
		case v.kind.public() == c.kind.public():
			pool++
			if oldest == "" || v.expires.Before(oldestExp) {
				oldest, oldestExp = k, v.expires
			}
		}
	}
	if pool >= maxCeremonies {
		if !c.kind.public() {
			return "", false
		}
		delete(cs.m, oldest)
	}
	id = wire.Random(18)
	cs.m[id] = c
	return id, true
}

// take removes and returns the ceremony with id when it is unexpired and match accepts it. A ceremony that match
// refuses (another kind or session) stays, so a stray request cannot burn somebody else's ceremony.
func (cs *ceremonies) take(id string, now time.Time, match func(*ceremony) bool) (*ceremony, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	c, ok := cs.m[id]
	if !ok || !match(c) {
		return nil, false
	}
	delete(cs.m, id)
	if !now.Before(c.expires) {
		return nil, false
	}
	return c, true
}

// clear drops every pending ceremony (reset-access).
func (cs *ceremonies) clear() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	clear(cs.m)
}
