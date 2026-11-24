package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/devXpro/homie/internal/store"
)

// rpDisplayName is the relying party name the browser may show next to the domain.
const rpDisplayName = "Homie"

// relyingParty returns the current relying party and its go-webauthn instance.
func (a *Auth) relyingParty() (RelyingParty, *webauthn.WebAuthn, error) {
	rp, ok := a.rp()
	if !ok {
		return RelyingParty{}, nil, errNoRelay
	}
	wa, err := newWebAuthn(rp)
	return rp, wa, err
}

// newWebAuthn configures go-webauthn for ADMIN-API §3.2: discoverable credentials, user verification required, no
// attestation, one origin.
func newWebAuthn(rp RelyingParty) (*webauthn.WebAuthn, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:                  rp.ID,
		RPDisplayName:         rpDisplayName,
		RPOrigins:             []string{rp.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
		// No extension is requested, and client extension outputs are unsigned and never read here, so an output a
		// password manager adds on its own must not fail an otherwise valid ceremony.
		ExtensionsUnsolicitedOutputPolicy: protocol.UnsolicitedOutputPolicyIgnore,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: relying party %q: %w", rp.ID, err)
	}
	return wa, nil
}

// user is a person as go-webauthn sees them.
type user struct {
	person store.Person
	creds  []webauthn.Credential
}

func (u user) WebAuthnID() []byte { return u.person.UserHandle }

func (u user) WebAuthnName() string {
	if u.person.Name == "" {
		return "owner"
	}
	return u.person.Name
}

func (u user) WebAuthnDisplayName() string                { return u.WebAuthnName() }
func (u user) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// descriptors lists the person's credentials for excludeCredentials.
func (u user) descriptors() []protocol.CredentialDescriptor {
	return webauthn.Credentials(u.creds).CredentialDescriptors()
}

// storedCredential is store.Passkey.Credential. The JSON form of webauthn.CredentialFlags drops the raw flags byte,
// so it is kept next to it; the RP ID records which domain the credential belongs to.
type storedCredential struct {
	V          int                 `json:"v"`
	RPID       string              `json:"rpId"`
	Flags      byte                `json:"flags"`
	Credential webauthn.Credential `json:"credential"`
}

func encodeCredential(rpID string, c *webauthn.Credential) ([]byte, error) {
	return json.Marshal(storedCredential{V: 1, RPID: rpID, Flags: byte(c.Flags.ProtocolValue()), Credential: *c})
}

func decodeCredential(b []byte) (rpID string, c webauthn.Credential, err error) {
	var sc storedCredential
	if err := json.Unmarshal(b, &sc); err != nil {
		return "", webauthn.Credential{}, err
	}
	if sc.V != 1 {
		return "", webauthn.Credential{}, fmt.Errorf("unknown credential format %d", sc.V)
	}
	sc.Credential.Flags = webauthn.NewCredentialFlags(protocol.AuthenticatorFlags(sc.Flags))
	return sc.RPID, sc.Credential, nil
}

// passkeyID is store.Passkey.ID of a credential id.
func passkeyID(credentialID []byte) string { return base64.RawURLEncoding.EncodeToString(credentialID) }

// credentials loads a person's passkeys of the relying party rpID. Passkeys of another domain (before a relay
// change) cannot verify and are left out.
func (a *Auth) credentials(ctx context.Context, personID, rpID string) ([]webauthn.Credential, error) {
	pks, err := a.st.Passkeys().ListByPerson(ctx, personID)
	if err != nil {
		return nil, fmt.Errorf("auth: list passkeys: %w", err)
	}
	creds := make([]webauthn.Credential, 0, len(pks))
	for _, pk := range pks {
		id, c, err := decodeCredential(pk.Credential)
		if err != nil {
			a.log.Warn("auth: unreadable passkey skipped", "passkey", pk.ID, "err", err)
			continue
		}
		if id == rpID {
			creds = append(creds, c)
		}
	}
	return creds, nil
}

// recordUse stores the credential an assertion updated (sign count, flags) and its last use.
func (a *Auth) recordUse(ctx context.Context, rpID string, c *webauthn.Credential) error {
	blob, err := encodeCredential(rpID, c)
	if err != nil {
		return err
	}
	err = a.st.Passkeys().RecordUse(context.WithoutCancel(ctx), passkeyID(c.ID), blob, a.now())
	if errors.Is(err, store.ErrNotFound) {
		return errLoginFailed // removed while the ceremony ran
	}
	return err
}
