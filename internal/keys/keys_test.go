package keys

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/devXpro/homie/internal/hub"
)

func TestGenerate(t *testing.T) {
	key, sum := Generate()
	if !strings.HasPrefix(key, Prefix) {
		t.Fatalf("key %q lacks prefix", key[:4])
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, Prefix))
	if err != nil || len(raw) != 32 {
		t.Fatalf("key body: %d bytes, err %v", len(raw), err)
	}
	if sum != Hash(key) || len(sum) != 64 {
		t.Fatalf("hash mismatch: %q", sum)
	}
	other, _ := Generate()
	if other == key {
		t.Fatal("two keys are equal")
	}
}

func TestHashKnownValue(t *testing.T) {
	// sha256("abc")
	if got := Hash("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("Hash = %s", got)
	}
}

func TestResolveKey(t *testing.T) {
	romanKey, romanSum := Generate()
	romanKey2, romanSum2 := Generate()
	tanyaKey, tanyaSum := Generate()
	roman := hub.Person{ID: "roman", Name: "Roman", Plugins: []string{"*"}}
	tanya := hub.Person{ID: "tanya", Name: "Tanya", Plugins: []string{"tv"}}
	r, err := New([]Entry{
		{Person: roman, SHA256: romanSum},
		{Person: roman, SHA256: strings.ToUpper(romanSum2)},
		{Person: tanya, SHA256: tanyaSum},
	})
	if err != nil {
		t.Fatal(err)
	}
	unknown, _ := Generate()
	tests := []struct {
		name string
		key  string
		want string
		err  error
	}{
		{"first key", romanKey, "roman", nil},
		{"second key, upper-case hash", romanKey2, "roman", nil},
		{"other person", tanyaKey, "tanya", nil},
		{"unknown", unknown, "", hub.ErrUnknownKey},
		{"empty", "", "", hub.ErrUnknownKey},
		{"hash instead of key", romanSum, "", hub.ErrUnknownKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := r.ResolveKey(context.Background(), tt.key)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if p.ID != tt.want {
				t.Fatalf("person = %q, want %q", p.ID, tt.want)
			}
		})
	}

	// The returned person is a copy.
	p, _ := r.ResolveKey(context.Background(), tanyaKey)
	p.Plugins[0] = "kettle"
	p, _ = r.ResolveKey(context.Background(), tanyaKey)
	if p.Plugins[0] != "tv" {
		t.Fatal("caller mutated the stored person")
	}
}

func TestUpdate(t *testing.T) {
	key, sum := Generate()
	r, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveKey(context.Background(), key); !errors.Is(err, hub.ErrUnknownKey) {
		t.Fatalf("empty set: %v", err)
	}
	if err := r.Update([]Entry{{Person: hub.Person{ID: "roman"}, SHA256: sum}}); err != nil {
		t.Fatal(err)
	}
	if p, err := r.ResolveKey(context.Background(), key); err != nil || p.ID != "roman" {
		t.Fatalf("after update: %v %v", p, err)
	}

	bad := []struct {
		name    string
		entries []Entry
	}{
		{"no person id", []Entry{{SHA256: sum}}},
		{"short hash", []Entry{{Person: hub.Person{ID: "x"}, SHA256: sum[:10]}}},
		{"not hex", []Entry{{Person: hub.Person{ID: "x"}, SHA256: strings.Repeat("zz", 32)}}},
		{"duplicate", []Entry{{Person: hub.Person{ID: "x"}, SHA256: sum}, {Person: hub.Person{ID: "y"}, SHA256: sum}}},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if err := r.Update(tt.entries); err == nil {
				t.Fatal("Update accepted a bad set")
			}
			if p, err := r.ResolveKey(context.Background(), key); err != nil || p.ID != "roman" {
				t.Fatal("a failed Update changed the set")
			}
		})
	}
}
