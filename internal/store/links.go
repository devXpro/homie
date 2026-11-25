package store

import (
	"context"
	"time"

	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/people/actorname"
	gkstore "github.com/devXpro/mcp-gatekeeper/store"
)

type linkTTLKey struct{}

// WithLinkTTL returns a context under which a setup link the gatekeeper stores lives at most ttl. The first-run page
// asks auth.IssueSetupLink for its link under it: the page travels over plain HTTP, so its links live 3 minutes,
// shorter than the 15 minutes auth gives a link of the admin socket.
func WithLinkTTL(ctx context.Context, ttl time.Duration) context.Context {
	return context.WithValue(ctx, linkTTLKey{}, ttl)
}

// Invites is the gatekeeper's invite store, with the expiry of setup links clipped by WithLinkTTL.
func (s *Store) Invites() gkstore.InviteStore { return clippedInvites{s.Store.Invites()} }

type clippedInvites struct{ gkstore.InviteStore }

// Create stores inv; a setup link made under WithLinkTTL ends at most ttl after it was made.
func (c clippedInvites) Create(ctx context.Context, inv core.Invite) error {
	if ttl, ok := ctx.Value(linkTTLKey{}).(time.Duration); ok && ttl > 0 && inv.Kind == core.InviteSetup {
		if end := inv.CreatedAt.Add(ttl); end.Before(inv.ExpiresAt) {
			inv.ExpiresAt = end
		}
	}
	return c.InviteStore.Create(ctx, inv)
}

// Audit is the gatekeeper's audit store with every entry stamped with its actor's name (package actorname), whichever
// package writes it, so the audit API can name people who are gone.
func (s *Store) Audit() gkstore.AuditStore {
	return namedAudit{AuditStore: s.Store.Audit(), people: s.People()}
}

type namedAudit struct {
	gkstore.AuditStore
	people gkstore.PersonStore
}

// Append stores e with its actor's name.
func (a namedAudit) Append(ctx context.Context, e core.AuditEntry) error {
	return a.AuditStore.Append(ctx, actorname.Stamp(ctx, a.people, e))
}
