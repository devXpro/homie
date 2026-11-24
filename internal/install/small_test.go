package install

import (
	"errors"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.2.0", 0},
		{"v0.2.0", "0.2.0", 0},
		{"0.2.0", "0.10.0", -1},
		{"1.0.0", "0.99.99", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0-alpha", "1.0.0-1", 1},
		{"1.0.0-rc", "1.0.0-rc.1", -1},
	}
	for _, tc := range cases {
		got, err := CompareVersions(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Errorf("Compare(%s,%s) = %d, %v; want %d", tc.a, tc.b, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "dev", "1.2", "1.2.3+build", "../1.2.3", "1.2.3/x"} {
		if _, err := NormalizeVersion(bad); err == nil {
			t.Errorf("NormalizeVersion(%q) accepted", bad)
		}
	}
}

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(`{"id":"tv","binary":"homie-tv","minHubVersion":"v0.2.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "tv" || len(m.Args) != 1 || m.Args[0] != "serve" || m.MinHubVersion != "0.2.0" {
		t.Fatalf("defaults: %+v", m)
	}
	if _, err := ParseManifest([]byte(" {\"id\":\"tv\",\"binary\":\"x\"}\n\t ")); err != nil {
		t.Fatalf("white space around the object: %v", err)
	}
	for name, body := range map[string]string{
		"not json":    `nope`,
		"bad id":      `{"id":"TV","binary":"x"}`,
		"short id":    `{"id":"t","binary":"x"}`,
		"no binary":   `{"id":"tv"}`,
		"path binary": `{"id":"tv","binary":"bin/x"}`,
		"dotdot":      `{"id":"tv","binary":".."}`,
		"bad min":     `{"id":"tv","binary":"x","minHubVersion":"soon"}`,
		"second json": `{"id":"tv","binary":"x"}{"id":"evil","binary":"y"}`,
		"trailing":    `{"id":"tv","binary":"x"} trailing`,
		"stray brace": `{"id":"tv","binary":"x"}}`,
	} {
		if _, err := ParseManifest([]byte(body)); !errors.Is(err, ErrBadManifest) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestManifestCompatible(t *testing.T) {
	m := &Manifest{MinHubVersion: "0.2.0"}
	for hub, want := range map[string]bool{"0.2.0": true, "0.3.1": true, "0.1.9": false, "dev": true, "": true, "0.2.0-rc.1": false} {
		if got := m.Compatible(hub); got != want {
			t.Errorf("hub %q: %v, want %v", hub, got, want)
		}
	}
	if !(&Manifest{}).Compatible("0.0.1") {
		t.Error("no minHubVersion means compatible")
	}
}

func TestParseChecksums(t *testing.T) {
	h1, h2 := sha([]byte("a")), sha([]byte("b"))
	got, err := ParseChecksums([]byte(h1 + "  a.tar.gz\n\n" + h2 + " *b.tar.gz\n"))
	if err != nil || got["a.tar.gz"] != h1 || got["b.tar.gz"] != h2 {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"zz  a", h1 + "\n", h1 + "  a\n" + h2 + "  a\n"} {
		if _, err := ParseChecksums([]byte(bad)); !errors.Is(err, ErrNoChecksum) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestFindAsset(t *testing.T) {
	rel := &Release{Tag: "v1.0.0", Version: "1.0.0", Assets: []Asset{
		{Name: "homed_1.0.0_linux_armv7.tar.gz"}, {Name: "homed_1.0.0_darwin_arm64.tar.gz"},
	}}
	if a, err := FindAsset(rel, "homed", "darwin", "arm64"); err != nil || a.Name != "homed_1.0.0_darwin_arm64.tar.gz" {
		t.Fatalf("%+v %v", a, err)
	}
	if a, err := FindAsset(rel, "homed", "linux", "arm"); err != nil || a.Name != "homed_1.0.0_linux_armv7.tar.gz" {
		t.Fatalf("arm: %+v %v", a, err)
	}
	if _, err := FindAsset(rel, "homed", "linux", "amd64"); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("%v", err)
	}
}

func TestCode(t *testing.T) {
	if Code(nil) != "" || Code(errors.New("x")) != "internal" {
		t.Fatal("basic codes")
	}
}
