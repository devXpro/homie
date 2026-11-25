package tunnel

import (
	"os"
	"path/filepath"
	"testing"
)

// A configured cloudflared that is missing is run as configured, so the tunnel fails to start: it is never replaced
// by another cloudflared found in PATH, which in a test would be the real one talking to Cloudflare.
func TestCloudflaredNeverFallsBackFromAConfiguredBinary(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "cloudflared")
	if err := os.WriteFile(inPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // an executable stub
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	missing := filepath.Join(t.TempDir(), "absent", "cloudflared")
	if bin, err := Cloudflared(missing); err == nil || bin != missing {
		t.Fatalf("Cloudflared(missing) = %q, %v; want the configured path and an error", bin, err)
	}
	if bin, err := Cloudflared(""); err != nil || bin != inPath {
		t.Fatalf("Cloudflared(\"\") = %q, %v; want the one in PATH", bin, err)
	}

	t.Setenv("PATH", t.TempDir())
	if bin, err := Cloudflared(""); err == nil || bin != "cloudflared" {
		t.Fatalf("Cloudflared(\"\") with none installed = %q, %v; want \"cloudflared\" and an error", bin, err)
	}
}
