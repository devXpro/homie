package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArchive(t *testing.T, dir string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// unpackInto unpacks data into <tmp>/outer/dest and returns the directories, so a test can check that nothing
// appeared outside dest.
func unpackInto(t *testing.T, data []byte, lim Limits) (outer, dest string, err error) {
	t.Helper()
	tmp := t.TempDir()
	outer = filepath.Join(tmp, "outer")
	dest = filepath.Join(outer, "dest")
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	return outer, dest, Unpack(writeArchive(t, tmp, data), dest, lim)
}

func TestUnpackRefusesUnsafeArchives(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
		lim     Limits
		want    error
	}{
		{"dotdot", []tarEntry{{name: "../evil", body: "x"}}, Limits{}, ErrUnsafeArchive},
		{"dotdot in the middle", []tarEntry{{name: "a/../../evil", body: "x"}}, Limits{}, ErrUnsafeArchive},
		{"absolute", []tarEntry{{name: "/etc/evil", body: "x"}}, Limits{}, ErrUnsafeArchive},
		{"backslash", []tarEntry{{name: `..\evil`, body: "x"}}, Limits{}, ErrUnsafeArchive},
		{"absolute symlink", []tarEntry{{name: "l", typ: tar.TypeSymlink, target: "/etc/passwd"}}, Limits{}, ErrUnsafeArchive},
		{"escaping symlink", []tarEntry{{name: "l", typ: tar.TypeSymlink, target: "../outside"}}, Limits{}, ErrUnsafeArchive},
		{"nested escaping symlink", []tarEntry{{name: "a/b/l", typ: tar.TypeSymlink, target: "../../../outside"}}, Limits{}, ErrUnsafeArchive},
		{"symlink inside", []tarEntry{{name: "bin/tool", body: "x"}, {name: "link", typ: tar.TypeSymlink, target: "bin/tool"}}, Limits{}, ErrUnsafeArchive},
		// Each link on its own resolves inside dest lexically; together they point above it (the review's chain).
		{"chained symlinks", []tarEntry{
			{name: "a/b/c/", typ: tar.TypeDir},
			{name: "a/b/c/up", typ: tar.TypeSymlink, target: "../../.."},
			{name: "homie-plugin.json", typ: tar.TypeSymlink, target: "a/b/c/up/../escaped"},
		}, Limits{}, ErrUnsafeArchive},
		{"hard link", []tarEntry{{name: "h", typ: tar.TypeLink, target: "../outside"}}, Limits{}, ErrUnsafeArchive},
		{"hard link inside", []tarEntry{{name: "a", body: "x"}, {name: "h", typ: tar.TypeLink, target: "a"}}, Limits{}, ErrUnsafeArchive},
		{"device", []tarEntry{{name: "d", typ: tar.TypeChar}}, Limits{}, ErrUnsafeArchive},
		{"fifo", []tarEntry{{name: "f", typ: tar.TypeFifo}}, Limits{}, ErrUnsafeArchive},
		{"duplicate", []tarEntry{{name: "a", body: "1"}, {name: "a", body: "2"}}, Limits{}, ErrUnsafeArchive},
		{"file under a file", []tarEntry{{name: "a", body: "1"}, {name: "a/b", body: "2"}}, Limits{}, ErrUnsafeArchive},
		{"too many entries", []tarEntry{{name: "a"}, {name: "b"}, {name: "c"}}, Limits{MaxEntries: 2}, ErrTooLarge},
		{"declared size over the cap", []tarEntry{{name: "a", body: strings.Repeat("x", 100)}}, Limits{MaxUnpackedBytes: 50}, ErrTooLarge},
		{"total over the cap", []tarEntry{{name: "a", body: strings.Repeat("x", 40)}, {name: "b", body: strings.Repeat("x", 40)}}, Limits{MaxUnpackedBytes: 60}, ErrTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outer, _, err := unpackInto(t, makeTarGz(t, tc.entries), tc.lim)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			// Nothing leaked next to dest.
			entries, _ := os.ReadDir(outer)
			if len(entries) != 1 {
				t.Fatalf("outer dir holds %d entries, want only dest", len(entries))
			}
		})
	}
}

// A symlink that is fine on its own is refused too, so no later entry can write through it to the outside.
func TestUnpackWritesCannotFollowSymlinkOut(t *testing.T) {
	data := makeTarGz(t, []tarEntry{
		{name: "sub/", typ: tar.TypeDir},
		{name: "sub/up", typ: tar.TypeSymlink, target: ".."}, // resolves to dest itself
		{name: "sub/up/up/evil", body: "x"},                  // would resolve to outer/ if followed twice
	})
	outer, dest, err := unpackInto(t, data, Limits{})
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("err = %v, want ErrUnsafeArchive", err)
	}
	if _, statErr := os.Stat(filepath.Join(outer, "evil")); statErr == nil {
		t.Fatal("a write escaped through a symlink")
	}
	if _, statErr := os.Lstat(filepath.Join(dest, "sub", "up")); statErr == nil {
		t.Fatal("the symlink was created")
	}
}

func TestUnpackGzipBombStopsAtTheCap(t *testing.T) {
	// A header that lies about its size is not possible with archive/tar, so compress a big run of zeros that
	// the header declares honestly but that exceeds the cap once decompressed.
	big := strings.Repeat("\x00", 8<<20)
	data := makeTarGz(t, []tarEntry{{name: "zeros", body: big}})
	if len(data) > 100<<10 {
		t.Fatalf("test archive should compress well, got %d bytes", len(data))
	}
	_, _, err := unpackInto(t, data, Limits{MaxUnpackedBytes: 1 << 20})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestUnpackNormalizesModes(t *testing.T) {
	data := makeTarGz(t, []tarEntry{
		{name: "./", typ: tar.TypeDir},
		{name: "./bin/tool", body: "x", mode: 0o4755}, // setuid is dropped
		{name: "plain", body: "y", mode: 0o666},
	})
	_, dest, err := unpackInto(t, data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"bin/tool": 0o755, "plain": 0o644} {
		fi, err := os.Lstat(filepath.Join(dest, name))
		if err != nil || fi.Mode() != want {
			t.Fatalf("%s: %v %v, want %v", name, fi, err, want)
		}
	}
}

// The released manifest is written through os.Root and never through a name the archive left behind.
func TestWriteManifest(t *testing.T) {
	released := []byte(`{"id":"tv","binary":"homie-tv"}`)
	dir := t.TempDir()
	if err := writeManifest(dir, released); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, ManifestAsset)); got != string(released) {
		t.Fatalf("manifest = %q", got)
	}
	// The archive's own copy is replaced by the released one.
	if err := os.WriteFile(filepath.Join(dir, ManifestAsset), []byte(`{"id":"evil"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(dir, released); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, ManifestAsset)); got != string(released) {
		t.Fatalf("manifest after an archive copy = %q", got)
	}
	// A symlink in its place (were one ever to get there) is refused, and its target stays untouched.
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(linked, ManifestAsset)); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(linked, released); !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("symlinked manifest: err = %v", err)
	}
	if got := readFile(t, outside); got != "keep" {
		t.Fatalf("the link target was written: %q", got)
	}
	// So is a directory of that name.
	withDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(withDir, ManifestAsset), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(withDir, released); !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("directory manifest: err = %v", err)
	}
}

func TestUnpackRejectsNonGzip(t *testing.T) {
	_, _, err := unpackInto(t, []byte("not an archive"), Limits{})
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("err = %v", err)
	}
	// A valid gzip of garbage is not a tar either.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(bytes.Repeat([]byte("garbage"), 100))
	_ = gz.Close()
	if _, _, err := unpackInto(t, buf.Bytes(), Limits{}); !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("err = %v", err)
	}
}
