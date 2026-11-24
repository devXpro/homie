// Package auth signs people into the admin with passkeys (PLAN §5.8, docs/ADMIN-API.md §1.3–§1.5, §3.2, §6).
//
// WebAuthn runs through github.com/go-webauthn/webauthn. The relying party ID is the relay domain and the only
// origin is https://<domain> (RelyingParty, read on every request). Every ceremony asks for a discoverable
// credential and user verification (Face ID, Touch ID) and no attestation. A ceremony lives in memory for five
// minutes and is finished at most once, by the endpoint of its own kind and, for step-up and new passkeys, by the
// session that began it.
//
// # Sessions
//
// A sign-in or a registration creates a store.Session and sets the cookie __Host-homie_session (Path=/; Secure;
// HttpOnly; SameSite=Strict) to 32 random bytes; only their sha256 is stored. A session ends 12 hours after its last
// request or 7 days after sign-in. Sign-in counts as step-up; a step-up assertion is fresh for 5 minutes.
//
// # What the rest of homed uses (T2.3, T2.6)
//
//	a, err := auth.New(auth.Options{Store: st, RelyingParty: rpFunc, Audit: auditWriter,
//		OnSessionEnd: adminServer.SessionRevoked, OnSetup: refreshHomeNameAndKeys, Version: version})
//	go a.Run(ctx)                              // hourly sweep of expired sessions
//
//	a.Handler()        // admin.Options.AuthRoutes: POST /api/admin/auth/* and /api/admin/me/passkeys/{begin,finish}
//	a.LocalHandler()   // admin.Options.LocalRoutes, socket only: POST /api/admin/local/{setup-link,reset-access}
//	a.Authenticate(r)  // (Principal, error); ErrUnauthenticated when there is no valid session.
//	                   // admin.Auth adapter: p, err := a.Authenticate(r); return admin.Principal(p), mapped err
//	a.RequireSession(h), a.RequireStepUp(h), auth.PrincipalFrom(ctx)   // the same rules as middleware
//
//	auth.NewSetupLink(ctx, st, rec, auth.LinkRequest{Origin, Via, Client, TTL}, now) // setup-link, first-run page
//	auth.ResetAccess(ctx, st, rec, origin, now)        // homed admin reset-access; a.ResetAccess adds the hooks
//	auth.OwnerReady(ctx, st, rpID)                     // an owner with a passkey of the relying party exists
//	auth.ClientAddr(r)                                 // the client behind the relay, for rate limits
//
// The handlers apply ADMIN-API §1.4 (X-Homie: 1, Origin, Sec-Fetch-Site) and their own session and step-up
// checks, so they are safe without the admin in front. Rate limits (ADMIN-API §1.7, ratelimit.go) have separate
// buckets per class: auth/token and registration per client address (20 a minute) and in total (60 a minute); sign-in
// per client address, counting begins and failed finishes, with no shared bucket; step-up and another passkey per
// session. The client is the X-Forwarded-For hop Caddy appended (see ClientAddr); requests without one have a bucket
// of their own. The admin audit actions auth.setup, auth.join, auth.login, auth.login_failed, auth.step_up,
// auth.logout, passkey.add, setup.link and access.reset go to Options.Audit, failures included (with the client's
// address).
//
// Tokens, cookies, challenges and credentials are never logged; failed ceremonies log only the name of the check
// that failed.
package auth
