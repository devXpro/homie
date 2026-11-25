package store

import (
	"context"

	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/people/actorname"
	gkstore "github.com/devXpro/mcp-gatekeeper/store"
)

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
