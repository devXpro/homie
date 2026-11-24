package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/devXpro/homie/internal/auth/wire"
)

// tokenBytes is the entropy of session cookies and setup tokens.
const tokenBytes = 32

// newToken returns a fresh secret: 32 random bytes as base64url without padding.
func newToken() string { return wire.Random(tokenBytes) }

// randomBytes returns n random bytes (WebAuthn user handles).
func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails since Go 1.24
	return b
}

// hashToken returns sha256(token) in lowercase hex, the only form secrets are stored in.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// wellFormedToken reports whether s can be a token newToken made, so junk never reaches the store.
func wellFormedToken(s string) bool {
	if len(s) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}

// newID returns a public random id with a prefix, e.g. "s_…" for sessions.
func newID(prefix string) string { return prefix + wire.Random(16) }

// checkName trims a display name and checks it with the rule internal/admin shares (wire.CheckName).
func checkName(path, v string) (string, error) { return wire.CheckName(path, v) }

// cyrillic maps Russian and Ukrainian letters to Latin for person ids ("Роман" → "roman").
var cyrillic = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'ґ': "g", 'д': "d", 'е': "e", 'ё': "e", 'є': "ye", 'ж': "zh",
	'з': "z", 'и': "i", 'і': "i", 'ї': "yi", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh",
	'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// slug turns a display name into a person id candidate matching ^[a-z][a-z0-9-]{0,31}$, or "owner" when nothing
// usable is left.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		s, ok := cyrillic[r]
		switch {
		case ok:
		case r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			s = string(r)
		default:
			s = "-"
		}
		for _, c := range s {
			if c == '-' {
				dash = b.Len() > 0
				continue
			}
			if b.Len() == 0 && !unicode.IsLetter(c) {
				continue
			}
			if dash {
				b.WriteByte('-')
				dash = false
			}
			b.WriteRune(c)
		}
	}
	id := b.String()
	if len(id) > 32 {
		id = strings.TrimRight(id[:32], "-")
	}
	if id == "" {
		return "owner"
	}
	return id
}
