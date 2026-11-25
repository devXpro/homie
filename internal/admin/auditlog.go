package admin

import (
	"context"
	"maps"

	"github.com/devXpro/mcp-gatekeeper/core"
)

// recordAdmin writes an admin action to the gatekeeper's audit, in the home's account so its owners read it in
// GET /api/account/audit: who (the signed-in person, or the CLI: no actor), from where, the surface (detail via) and,
// for a failure, its error. Detail holds no secrets and no application data.
func (s *Server) recordAdmin(q *request, action, target string, detail map[string]string, failure error) {
	d := map[string]string{"via": q.via()}
	maps.Copy(d, detail)
	if failure != nil {
		d["result"], d["error"] = "failed", clip(failure.Error(), 200)
	}
	ctx := context.WithoutCancel(q.ctx)
	e := core.AuditEntry{At: s.now(), Action: action, Target: target, Detail: d, IP: q.ip}
	if q.local {
		if acc, ok, err := s.homeAccount(ctx); err == nil && ok {
			e.AccountID = acc.ID
		}
	} else {
		e.AccountID, e.Actor = q.v.Account.ID, q.v.Person.ID
	}
	if err := s.st.Audit().Append(ctx, e); err != nil {
		s.log.Error("cannot write an audit entry", "action", action, "err", err)
	}
}

// clip shortens s to n bytes at most, on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
