package relay

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelfCheck(t *testing.T) {
	pki := newTestPKI(t)
	other := newTestPKI(t)

	tests := []struct {
		name    string
		handler http.HandlerFunc
		caFile  string
		wantErr string
	}{
		{"our hub", hub("me", http.StatusOK), pki.CAFile, ""},
		{"another hub", hub("someone-else", http.StatusOK), pki.CAFile, "another Homie hub"},
		{"not a hub", hub("", http.StatusOK), pki.CAFile, "no instance id"},
		{"home offline", hub("", http.StatusNotFound), pki.CAFile, "home is offline"},
		{"hub error", hub("me", http.StatusInternalServerError), pki.CAFile, "status 500"},
		{"untrusted certificate", hub("me", http.StatusOK), other.CAFile, "certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewUnstartedServer(tt.handler)
			ts.TLS = &tls.Config{Certificates: []tls.Certificate{pki.Leaf}, MinVersion: tls.VersionTLS12}
			ts.StartTLS()
			defer ts.Close()

			cfg := Config{Domain: "homie.test", CAFile: tt.caFile, ServerAddr: ts.Listener.Addr().String()}
			err := cfg.SelfCheck(context.Background(), "me")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func hub(id string, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || r.Host != "homie.test" {
			http.Error(w, "wrong request "+r.Host+r.URL.Path, http.StatusTeapot)
			return
		}
		if id != "" {
			w.Header().Set(InstanceHeader, id)
		}
		w.WriteHeader(status)
	}
}
