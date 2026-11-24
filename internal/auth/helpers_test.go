package auth

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/devXpro/homie/internal/auth/wire"
)

func TestSlug(t *testing.T) {
	idRe := regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	for name, want := range map[string]string{
		"Роман":                        "roman",
		"Таня":                         "tanya",
		"Щука Ёлкина":                  "shchuka-elkina",
		"Їжак":                         "yizhak",
		"Roman":                        "roman",
		"  Mary-Jane O'Neil ":          "mary-jane-o-neil",
		"2Pac":                         "pac",
		"123":                          "owner",
		"🙂":                            "owner",
		"":                             "owner",
		strings.Repeat("abcdefgh-", 5): "abcdefgh-abcdefgh-abcdefgh-abcde",
	} {
		got := slug(name)
		if got != want || !idRe.MatchString(got) {
			t.Errorf("slug(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCheckName(t *testing.T) {
	if got, err := checkName("/n", "  Мой шалаш \t"); err != nil || got != "Мой шалаш" {
		t.Fatalf("%q %v", got, err)
	}
	for _, good := range []string{"«Мой шалаш»", "Roman's home", "🏠 Дача 2", "Ю" + strings.Repeat("x", 39)} {
		if got, err := checkName("/n", good); err != nil || got != good {
			t.Errorf("%q: %q %v", good, got, err)
		}
	}
	for _, bad := range []string{
		"", " ", strings.Repeat("x", 41), "a\x00b", "line\nbreak", "cr\rhere", "tab\tinside", "nel\u0085here",
		"line\u2028sep", "para\u2029sep", "rtl\u202eoverride", "isolate\u2066x", `say "hi"`, `back\slash`, "tick`",
	} {
		_, err := checkName("/n", bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
			continue
		}
		if we, ok := errors.AsType[*wire.Error](err); !ok || we.Code != "invalid_args" || we.Fields[0].Path != "/n" {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestDeviceLabel(t *testing.T) {
	for ua, want := range map[string]string{
		safariMac: "Safari · Mac",
		safariIOS: "Safari · iPhone",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/140.0 Mobile/15E148 Safari/604.1": "Chrome · iPhone",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36":                  "Chrome · Mac",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0":          "Edge · Windows",
		"Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0":                                                                 "Firefox · Linux",
		"Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 YaBrowser/25.0 Mobile Safari/537.36":                "Yandex Browser · Android",
		"curl/8.11.1": "Browser",
		"":            "Browser",
	} {
		if got := deviceLabel(ua); got != want {
			t.Errorf("deviceLabel(%q) = %q, want %q", ua, got, want)
		}
	}
	if got := defaultPasskeyName(safariIOS); got != "iPhone" {
		t.Errorf("passkey name %q", got)
	}
	if got := defaultPasskeyName("curl/8"); got != "Passkey" {
		t.Errorf("passkey name %q", got)
	}
}

func TestProviderName(t *testing.T) {
	icloud := []byte{0xfb, 0xfc, 0x30, 0x07, 0x15, 0x4e, 0x4e, 0xcc, 0x8c, 0x0b, 0x6e, 0x02, 0x05, 0x57, 0xd7, 0xbd}
	if got := providerName(icloud); got != "iCloud Keychain" {
		t.Fatalf("provider %q", got)
	}
	if providerName(make([]byte, 16)) != "" || providerName(nil) != "" {
		t.Fatal("zero or missing AAGUID named")
	}
}

// The stored form keeps what go-webauthn needs at the next assertion, the raw flags included.
func TestCredentialRoundTrip(t *testing.T) {
	flags := protocol.FlagUserPresent | protocol.FlagUserVerified | protocol.FlagBackupEligible | protocol.FlagBackupState
	in := webauthn.Credential{
		ID: []byte{1, 2, 3}, PublicKey: []byte{4, 5}, AttestationFormat: "none",
		Transport: []protocol.AuthenticatorTransport{protocol.Internal, protocol.Hybrid},
		Flags:     webauthn.NewCredentialFlags(flags),
		Authenticator: webauthn.Authenticator{
			AAGUID: make([]byte, 16), SignCount: 7,
		},
	}
	blob, err := encodeCredential(testDomain, &in)
	if err != nil {
		t.Fatal(err)
	}
	rpID, out, err := decodeCredential(blob)
	if err != nil || rpID != testDomain || out.Flags.ProtocolValue() != flags || !out.Flags.BackupEligible ||
		out.Authenticator.SignCount != 7 || string(out.PublicKey) != string(in.PublicKey) || len(out.Transport) != 2 {
		t.Fatalf("round trip: %q %+v %v", rpID, out, err)
	}
	if _, _, err := decodeCredential([]byte(`{"v":2}`)); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestLimiterPrune(t *testing.T) {
	l := newLimiter(1, 1_000_000)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	for i := range maxBuckets + 10 {
		l.take(strings.Repeat("x", i%7)+time.Duration(i).String(), now)
		l.peek("p"+time.Duration(i).String(), now)
	}
	if len(l.buckets) > maxBuckets {
		t.Fatalf("%d buckets", len(l.buckets))
	}
}
