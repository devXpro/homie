// Package store is the hub's persistent state from phase 2 on (PLAN.md §5.6: <data_dir>/homed.db): people, their
// passkeys, sessions and person keys, invites and setup links, ChatGPT connections, installed plugins, the audit log
// and the home settings.
//
// This file holds the contracts only. The SQLite implementation (modernc.org/sqlite, embedded migrations, the one-time
// import of homed.yaml) is T2.1's. docs/ADMIN-API.md says how the admin API uses each repository.
//
// Conventions for every implementation:
//   - Times are stored in UTC with millisecond precision; a zero time.Time means "never" or "not set".
//   - Get-style methods return ErrNotFound when nothing matches; Create returns ErrConflict on a duplicate id or hash.
//   - Secrets are never stored in clear: session cookies, person keys and invite tokens are stored as sha256 in
//     lowercase hex, compared by the caller (or by an indexed lookup on the hash).
//   - Slices returned to callers are fresh copies; callers may keep and change them.
//   - Every method is safe for concurrent use and honours ctx.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Errors the repositories return. Wrap them with %w; callers test with errors.Is.
var (
	// ErrNotFound means no row matches the id, hash or token.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict means a row with the same id, hash or unique name already exists.
	ErrConflict = errors.New("store: already exists")
	// ErrLastPasskey means the delete would leave the person without any passkey.
	ErrLastPasskey = errors.New("store: the last passkey cannot be removed")
	// ErrTokenExpired means an invite or setup token exists but its time is up.
	ErrTokenExpired = errors.New("store: token expired")
	// ErrTokenUsed means an invite or setup token was already consumed or revoked.
	ErrTokenUsed = errors.New("store: token already used")
)

// Store gives access to every repository over one database.
type Store interface {
	People() People
	Passkeys() Passkeys
	Sessions() Sessions
	Keys() Keys
	Invites() Invites
	Connections() Connections
	Plugins() Plugins
	Audit() Audit
	Settings() Settings

	// InTx runs fn with a Store whose repositories share one transaction. It commits when fn returns nil and rolls
	// back otherwise, returning fn's error. Calling InTx on the Store given to fn runs the inner fn in the same
	// transaction. Use it when several writes must succeed together, e.g. consuming a setup token, creating the
	// owner and storing the first passkey.
	InTx(ctx context.Context, fn func(tx Store) error) error

	// Close closes the database. The Store must not be used afterwards.
	Close() error
}

// Role says what a person may do in the admin.
type Role string

const (
	// RoleOwner runs the home: the whole admin API. Phase 2 has exactly one owner; the homed.yaml import makes the
	// first person of the phase 1 config the owner.
	RoleOwner Role = "owner"
	// RoleMember uses the home through ChatGPT and may manage only their own passkeys, sessions and connection
	// (phase 3).
	RoleMember Role = "member"
)

// Person is one family member. Their plugin scope decides which assistant tools their keys and connections see.
type Person struct {
	ID         string   // stable, ^[a-z][a-z0-9-]{0,31}$, e.g. "roman"; never reused while the row exists
	Name       string   // display name, e.g. "Roman"
	Role       Role     // RoleOwner or RoleMember
	Plugins    []string // plugin ids this person may use; ["*"] means all (hub.Person.Plugins)
	Language   string   // preferred UI language, "ru" or "en"; empty means the home default
	UserHandle []byte   // WebAuthn user.id: 32 random bytes made at creation; never shown, never changed
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// People stores the family.
type People interface {
	// Get returns one person by id.
	Get(ctx context.Context, id string) (Person, error)
	// GetByUserHandle returns the person a WebAuthn user handle belongs to (discoverable login).
	GetByUserHandle(ctx context.Context, handle []byte) (Person, error)
	// List returns everybody, owners first, then by name.
	List(ctx context.Context) ([]Person, error)
	// HasOwner reports whether an owner exists, whether or not they have a passkey yet.
	HasOwner(ctx context.Context) (bool, error)
	// Create stores a new person. UserHandle must be set; CreatedAt and UpdatedAt are set by the store when zero.
	Create(ctx context.Context, p Person) error
	// Update replaces Name, Role, Plugins and Language of an existing person and sets UpdatedAt.
	Update(ctx context.Context, p Person) error
	// Delete removes the person together with their passkeys, sessions, keys, invites and connections. Audit records
	// keep the name they were written with.
	Delete(ctx context.Context, id string) error
}

// Passkey is one WebAuthn credential of a person.
type Passkey struct {
	ID         string // base64url (no padding) of the WebAuthn credential id
	PersonID   string
	Name       string // human label, editable, e.g. "MacBook Pro" or "iPhone"
	Provider   string // the authenticator's name from its AAGUID when known, e.g. "iCloud Keychain"; may be empty
	Credential []byte // the auth package's serialized credential (public key, sign count, flags, transports); opaque here
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// Passkeys stores WebAuthn credentials.
type Passkeys interface {
	// Get returns one passkey by credential id.
	Get(ctx context.Context, id string) (Passkey, error)
	// ListByPerson returns a person's passkeys, oldest first.
	ListByPerson(ctx context.Context, personID string) ([]Passkey, error)
	// Count returns the number of passkeys of all people; zero means the home has no way in yet.
	Count(ctx context.Context) (int, error)
	// Create stores a new passkey. ErrConflict when the credential id is already known.
	Create(ctx context.Context, pk Passkey) error
	// Rename changes the label.
	Rename(ctx context.Context, id, name string) error
	// RecordUse stores the credential as updated by a successful assertion (the new sign count) and LastUsedAt.
	RecordUse(ctx context.Context, id string, credential []byte, at time.Time) error
	// Delete removes one passkey. It returns ErrLastPasskey, and deletes nothing, when the passkey is its person's
	// only one; the check and the delete are atomic.
	Delete(ctx context.Context, id string) error
	// DeleteAll removes every passkey of every person (homed admin reset-access) and returns how many it removed.
	DeleteAll(ctx context.Context) (int, error)
}

// Session is one signed-in browser.
type Session struct {
	ID         string // public random id used by the API to list and revoke; not a secret
	TokenHash  string // sha256 hex of the cookie value; the cookie value itself is never stored
	PersonID   string
	PasskeyID  string    // the passkey used to sign in
	Device     string    // short label from the User-Agent, e.g. "Safari · iPhone"
	CreatedAt  time.Time // the absolute lifetime (7 days, PLAN §5.8) counts from here
	LastSeenAt time.Time // the idle lifetime (12 hours) counts from here
	StepUpAt   time.Time // the last fresh passkey assertion in this session, sign-in included
}

// Sessions stores admin sessions. Expiry policy lives in internal/auth; the store only keeps the times.
type Sessions interface {
	// GetByTokenHash returns the session behind a cookie.
	GetByTokenHash(ctx context.Context, tokenHash string) (Session, error)
	// ListByPerson returns a person's sessions, newest LastSeenAt first.
	ListByPerson(ctx context.Context, personID string) ([]Session, error)
	// Create stores a new session.
	Create(ctx context.Context, s Session) error
	// Touch sets LastSeenAt. Callers throttle it (at most once a minute per session).
	Touch(ctx context.Context, id string, at time.Time) error
	// SetStepUp sets StepUpAt after a fresh assertion.
	SetStepUp(ctx context.Context, id string, at time.Time) error
	// Delete removes one session (logout, revoke).
	Delete(ctx context.Context, id string) error
	// DeleteByPerson removes a person's sessions except the one with id keep (empty keeps none) and returns how many
	// it removed.
	DeleteByPerson(ctx context.Context, personID, keep string) (int, error)
	// DeleteAll removes every session (homed admin reset-access).
	DeleteAll(ctx context.Context) (int, error)
	// DeleteExpired removes sessions last seen before idleBefore or created before createdBefore.
	DeleteExpired(ctx context.Context, idleBefore, createdBefore time.Time) (int, error)
}

// Key is one person key (PLAN §5.3 key format): the bearer a ChatGPT Site or another client sends to the gateway.
type Key struct {
	ID           string // public random id used by the API and the CLI; not derived from the key
	PersonID     string
	SHA256       string // sha256 hex of the key; what keys.Resolver matches
	Label        string // e.g. "ChatGPT Site"
	Hint         string // the first 8 characters of the key ("ghk_AbCd"), to tell keys apart; empty for imports
	ConnectionID string // the connection that owns the key, if any (phase 3, path B)
	CreatedAt    time.Time
	LastUsedAt   time.Time
}

// Keys stores person keys. Every change must be followed by keys.Resolver.Update with the result of List.
type Keys interface {
	// Get returns one key by id.
	Get(ctx context.Context, id string) (Key, error)
	// List returns every key of every person, for rebuilding the resolver.
	List(ctx context.Context) ([]Key, error)
	// ListByPerson returns a person's keys, newest first.
	ListByPerson(ctx context.Context, personID string) ([]Key, error)
	// Create stores a new key. ErrConflict when the hash is already stored.
	Create(ctx context.Context, k Key) error
	// Touch sets LastUsedAt. Callers throttle it (at most once a minute per key).
	Touch(ctx context.Context, id string, at time.Time) error
	// Delete revokes one key.
	Delete(ctx context.Context, id string) error
}

// InviteKind tells a setup link from a family invite. Both are one-time tokens that let somebody register a passkey.
type InviteKind string

const (
	// InviteSetup is the first-run link for the owner (PLAN §5.8): 15 minutes, single use.
	InviteSetup InviteKind = "setup"
	// InviteMember is a family invite (phase 3): 24 hours, single use, with the invitee's scope already on the person.
	InviteMember InviteKind = "member"
)

// Invite is one setup link or family invite. The token itself is shown once and never stored.
type Invite struct {
	ID        string // public random id for the API (list, revoke)
	TokenHash string // sha256 hex of the token
	Kind      InviteKind
	PersonID  string // the person who registers with it; empty for a setup link when no owner row exists yet
	CreatedBy string // person id of the inviter; empty for setup links made on the home box
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    time.Time // set by Consume or Revoke; zero while the token is usable
}

// Invites stores setup links and invites.
type Invites interface {
	// GetByTokenHash returns the invite for a token in any state, so callers can tell expired from used.
	GetByTokenHash(ctx context.Context, tokenHash string) (Invite, error)
	// ListActive returns the unused, unexpired invites of a kind at now, newest first.
	ListActive(ctx context.Context, kind InviteKind, now time.Time) ([]Invite, error)
	// Create stores a new invite.
	Create(ctx context.Context, inv Invite) error
	// Consume atomically marks the token used and returns the invite. It returns ErrNotFound, ErrTokenExpired or
	// ErrTokenUsed, and changes nothing, when the token cannot be used at now.
	Consume(ctx context.Context, tokenHash string, now time.Time) (Invite, error)
	// Revoke marks one invite used without consuming it for a registration.
	Revoke(ctx context.Context, id string, at time.Time) error
	// RevokeAll marks every unused invite of a kind (and, when personID is not empty, of that person) used. A new
	// setup link revokes the older ones this way.
	RevokeAll(ctx context.Context, kind InviteKind, personID string, at time.Time) (int, error)
}

// ConnectionKind is how a person's ChatGPT reaches the home (PLAN §2, "ChatGPT side"). T3.0 picks one; the model
// holds both.
type ConnectionKind string

const (
	// ConnectionSite is path B: a ChatGPT Site in the person's account calling the REST façade with a person key.
	ConnectionSite ConnectionKind = "site"
	// ConnectionPlugin is path A: a custom MCP plugin added by URL, authorized with OAuth.
	ConnectionPlugin ConnectionKind = "plugin"
)

// ConnectionState is the stored life state of a connection. "Stale" is not stored: the API derives it when a live
// connection's Fingerprint differs from the hub's current one.
type ConnectionState string

const (
	// ConnectionPending exists but was never set up.
	ConnectionPending ConnectionState = "pending"
	// ConnectionNeedsLogin waits for the person to sign in to ChatGPT (path B device code, path A OAuth).
	ConnectionNeedsLogin ConnectionState = "needs_login"
	// ConnectionPublishing is being published or republished right now.
	ConnectionPublishing ConnectionState = "publishing"
	// ConnectionLive works: ChatGPT can call the home.
	ConnectionLive ConnectionState = "live"
	// ConnectionFailed means the last publish or authorization failed; LastError says why.
	ConnectionFailed ConnectionState = "failed"
	// ConnectionRevoked was switched off; its key or tokens are gone.
	ConnectionRevoked ConnectionState = "revoked"
)

// Connection is one person's link from ChatGPT to the home (phase 3).
type Connection struct {
	ID          string // public random id
	PersonID    string
	Kind        ConnectionKind
	State       ConnectionState
	Title       string          // the name ChatGPT shows: the home name at the last publish, e.g. «Мой шалаш»
	KeyID       string          // path B: the person key the Site uses; empty for path A
	Detail      json.RawMessage // kind-specific, owned by internal/sites or internal/oauth (slug, site id, client id…); never secrets
	Fingerprint string          // of title, tools and instructions at the last publish; differs from the hub's → stale
	PublishedAt time.Time
	LastError   string // human English, never secrets
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Connections stores ChatGPT connections.
type Connections interface {
	// Get returns one connection by id.
	Get(ctx context.Context, id string) (Connection, error)
	// List returns every connection, by person, then newest first.
	List(ctx context.Context) ([]Connection, error)
	// ListByPerson returns a person's connections, newest first.
	ListByPerson(ctx context.Context, personID string) ([]Connection, error)
	// Create stores a new connection.
	Create(ctx context.Context, c Connection) error
	// Update replaces the mutable fields (State, Title, KeyID, Detail, Fingerprint, PublishedAt, LastError) and sets
	// UpdatedAt.
	Update(ctx context.Context, c Connection) error
	// Delete removes one connection. The caller revokes its key or tokens first.
	Delete(ctx context.Context, id string) error
}

// PluginSource says where a plugin came from.
type PluginSource string

const (
	// PluginGitHub was installed from a GitHub release (T2.5).
	PluginGitHub PluginSource = "github"
	// PluginLocal is a command on this machine, added with the CLI (development, phase 1 imports).
	PluginLocal PluginSource = "local"
)

// Plugin is one installed plugin: what the plugin host needs to run it (plugins.Spec) and where it came from.
type Plugin struct {
	ID              string // ^[a-z][a-z0-9]{1,15}$ (PLAN §5.1)
	Source          PluginSource
	Repo            string   // "owner/name" for PluginGitHub, e.g. "devXpro/homie-tv"
	Version         string   // installed release, e.g. "0.2.0"; empty for PluginLocal
	PreviousVersion string   // the release kept for rollback; empty when none
	Command         []string // executable (absolute path) and arguments, e.g. [".../bin/0.2.0/homie-tv", "serve"]
	Enabled         bool     // disabled plugins are not started and their tools are not listed
	InstalledAt     time.Time
	UpdatedAt       time.Time
}

// Plugins stores the installed plugins.
type Plugins interface {
	// Get returns one plugin by id.
	Get(ctx context.Context, id string) (Plugin, error)
	// List returns every plugin, by id.
	List(ctx context.Context) ([]Plugin, error)
	// Create stores a newly installed plugin. ErrConflict when the id is taken.
	Create(ctx context.Context, p Plugin) error
	// Update replaces every field but ID and InstalledAt, and sets UpdatedAt.
	Update(ctx context.Context, p Plugin) error
	// Delete removes one plugin's row; its files are the installer's business.
	Delete(ctx context.Context, id string) error
}

// AuditKind separates tool calls from admin and security events.
type AuditKind string

const (
	// AuditTool is a tool call through any surface (hub.AuditEntry).
	AuditTool AuditKind = "tool"
	// AuditAdmin is an admin or security event: sign-in, passkey added, key created, plugin installed… The action
	// names are listed in docs/ADMIN-API.md.
	AuditAdmin AuditKind = "admin"
)

// AuditRecord is one audit log line.
type AuditRecord struct {
	ID       int64 // assigned by Append, strictly increasing; the pagination cursor
	At       time.Time
	Kind     AuditKind
	PersonID string          // empty when nobody is known, e.g. a failed sign-in
	Person   string          // display name at the time, kept after the person is deleted
	Via      string          // "site", "mcp", "admin" or "cli"
	Tool     string          // AuditTool: the tool name, e.g. "tv_power_on"
	PluginID string          // AuditTool: the tool's plugin; AuditAdmin: the plugin acted on, if any
	Action   string          // AuditAdmin: e.g. "auth.login", "plugin.install"
	Target   string          // AuditAdmin: the object acted on (person id, key id, passkey name…), if any
	Args     json.RawMessage // redacted arguments or details (audit.Redact); may be nil
	OK       bool
	Error    string // why it failed, human English; never secrets
	Duration time.Duration
}

// AuditStatus filters by outcome.
type AuditStatus string

const (
	// AuditAny matches every record.
	AuditAny AuditStatus = ""
	// AuditOK matches successes only.
	AuditOK AuditStatus = "ok"
	// AuditFailed matches failures only.
	AuditFailed AuditStatus = "failed"
)

// AuditFilter selects audit records. Zero fields match everything.
type AuditFilter struct {
	Kind     AuditKind // "" for both kinds
	PersonID string
	PluginID string
	Tool     string
	Via      []string // any of these surfaces; empty matches all
	Status   AuditStatus
	Since    time.Time // At >= Since
	Until    time.Time // At < Until
	BeforeID int64     // ID < BeforeID: the cursor of the next page; 0 starts at the newest
	Limit    int       // page size; the store caps it at 200 and uses 50 when it is 0
}

// Audit stores the audit log. hub.Auditor stays non-blocking: the writer in internal/audit batches into Append.
type Audit interface {
	// Append stores records in order and returns them with ID set.
	Append(ctx context.Context, recs []AuditRecord) ([]AuditRecord, error)
	// Query returns matching records, newest (highest ID) first. When it returns Limit records, the next page is the
	// same filter with BeforeID set to the last record's ID.
	Query(ctx context.Context, f AuditFilter) ([]AuditRecord, error)
	// Get returns one record by id.
	Get(ctx context.Context, id int64) (AuditRecord, error)
	// Prune deletes records older than before and returns how many it deleted.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// HomeSettings is the home-wide configuration that the admin can change. The relay (domain and token) stays in
// homed.yaml and relay-token (PLAN §5.6), because the hub needs it before the database opens.
type HomeSettings struct {
	HomeName       string         // e.g. «Мой шалаш»; the ChatGPT name of every person's connection
	Language       string         // default UI language, "ru" or "en"
	ConnectionKind ConnectionKind // phase 3: the path T3.0 chose; empty until then
	SiteSignOut    bool           // phase 3, path B: sign out of the person's ChatGPT after each publish
	UpdatedAt      time.Time
}

// Settings stores the single HomeSettings row.
type Settings interface {
	// Get returns the settings. Before the first Put it returns HomeSettings{HomeName: "", Language: "ru"}, not
	// ErrNotFound.
	Get(ctx context.Context) (HomeSettings, error)
	// Put replaces the settings and sets UpdatedAt.
	Put(ctx context.Context, s HomeSettings) error
}
