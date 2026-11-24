// Package keys issues person keys and maps bearer keys back to their people (PLAN.md §5.3, §5.6).
//
// A key is "ghk_" plus base64url (no padding) of 32 random bytes. Only sha256(key) in hex is ever stored; matching
// hashes the presented key and compares it with every stored hash in constant time.
package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/devXpro/homie/internal/hub"
)

// Prefix starts every person key.
const Prefix = "ghk_"

// Generate returns a new key and its sha256 in hex. Show the key once; store only the hash.
func Generate() (key, sha256hex string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error since Go 1.24; it aborts the program instead.
	key = Prefix + base64.RawURLEncoding.EncodeToString(b)
	return key, Hash(key)
}

// Hash returns sha256(key) in lowercase hex, the form stored in homed.yaml.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Entry is one stored key: the hash and the person it belongs to. A person with several keys has several entries.
type Entry struct {
	Person hub.Person
	SHA256 string // hex sha256 of the key, as stored
}

type entry struct {
	person hub.Person
	sum    []byte
}

// Resolver is a hub.KeyResolver over a set of entries. It is safe for concurrent use; Update swaps the set atomically.
type Resolver struct {
	entries atomic.Pointer[[]entry]
}

var _ hub.KeyResolver = (*Resolver)(nil)

// New returns a Resolver over entries; see Update for the validation.
func New(entries []Entry) (*Resolver, error) {
	r := &Resolver{}
	if err := r.Update(entries); err != nil {
		return nil, err
	}
	return r, nil
}

// Update replaces the set. Every entry needs a person id and a 64-character hex hash, and no hash may appear twice.
// On error the previous set stays in place.
func (r *Resolver) Update(entries []Entry) error {
	next := make([]entry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for i, e := range entries {
		if e.Person.ID == "" {
			return fmt.Errorf("key %d: person id is empty", i)
		}
		sum, err := hex.DecodeString(e.SHA256)
		if err != nil || len(sum) != sha256.Size {
			return fmt.Errorf("key %d of %q: sha256 must be 64 hex characters", i, e.Person.ID)
		}
		if seen[string(sum)] {
			return fmt.Errorf("key %d of %q: the same key is listed twice", i, e.Person.ID)
		}
		seen[string(sum)] = true
		p := e.Person
		p.Plugins = slices.Clone(p.Plugins)
		next = append(next, entry{person: p, sum: sum})
	}
	r.entries.Store(&next)
	return nil
}

// ResolveKey returns the person the key belongs to, or hub.ErrUnknownKey. It compares against every stored hash
// without stopping early, so the time taken does not depend on which entry matched.
func (r *Resolver) ResolveKey(_ context.Context, key string) (hub.Person, error) {
	if key == "" {
		return hub.Person{}, hub.ErrUnknownKey
	}
	list := r.entries.Load()
	if list == nil {
		return hub.Person{}, hub.ErrUnknownKey
	}
	sum := sha256.Sum256([]byte(key))
	found := -1
	for i, e := range *list {
		if subtle.ConstantTimeCompare(e.sum, sum[:]) == 1 {
			found = i
		}
	}
	if found < 0 {
		return hub.Person{}, hub.ErrUnknownKey
	}
	p := (*list)[found].person
	p.Plugins = slices.Clone(p.Plugins)
	return p, nil
}
