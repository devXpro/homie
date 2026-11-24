package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strings"
)

// ManifestAsset is the release asset that describes a plugin (PLAN §5.1).
const ManifestAsset = "homie-plugin.json"

// ChecksumsAsset is the GoReleaser checksum file.
const ChecksumsAsset = "checksums.txt"

const maxManifestBytes = 64 << 10

var pluginIDRE = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)

// Manifest is homie-plugin.json.
type Manifest struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Binary        string   `json:"binary"`
	Args          []string `json:"args"`
	MinHubVersion string   `json:"minHubVersion"`
	Homepage      string   `json:"homepage"`
}

// ParseManifest decodes and validates a manifest: exactly one JSON object, nothing after it but white space. Args
// default to ["serve"], Title to the id.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadManifest, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data after the JSON object", ErrBadManifest)
	}
	if !pluginIDRE.MatchString(m.ID) {
		return nil, fmt.Errorf("%w: id %q must match %s", ErrBadManifest, m.ID, pluginIDRE)
	}
	if m.Binary == "" || m.Binary == "." || m.Binary == ".." || strings.ContainsAny(m.Binary, "/\\\x00") {
		return nil, fmt.Errorf("%w: binary %q must be a plain file name", ErrBadManifest, m.Binary)
	}
	if m.MinHubVersion != "" {
		v, err := NormalizeVersion(m.MinHubVersion)
		if err != nil {
			return nil, fmt.Errorf("%w: minHubVersion: %w", ErrBadManifest, err)
		}
		m.MinHubVersion = v
	}
	if m.Args == nil {
		m.Args = []string{"serve"}
	}
	if m.Title == "" {
		m.Title = m.ID
	}
	return &m, nil
}

// Compatible reports whether a hub of this version satisfies minHubVersion. A development build (no release
// version) is always compatible.
func (m *Manifest) Compatible(hubVersion string) bool {
	if m.MinHubVersion == "" || isDevVersion(hubVersion) {
		return true
	}
	c, err := CompareVersions(hubVersion, m.MinHubVersion)
	return err == nil && c >= 0
}

// FetchManifest downloads and parses the release's homie-plugin.json. A release without it is ErrBadManifest:
// it is not a plugin release.
func (c *Client) FetchManifest(ctx context.Context, rel *Release) (*Manifest, []byte, error) {
	a, ok := rel.Asset(ManifestAsset)
	if !ok {
		return nil, nil, fmt.Errorf("%w: release %s has no %s", ErrBadManifest, rel.Tag, ManifestAsset)
	}
	data, err := c.get(ctx, a.URL, false, maxManifestBytes)
	if err != nil {
		return nil, nil, err
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, nil, err
	}
	return m, data, nil
}

// ArchiveName builds the GoReleaser archive name: <binary>_<version>_<os>_<arch>.tar.gz.
func ArchiveName(binary, version, goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", binary, version, goos, goarch)
}

// FindAsset picks the archive for binary at the release's version for goos/goarch. Empty goos/goarch mean the
// running platform. For 32-bit ARM the GoReleaser spellings armv7, armv6 and arm are tried in that order.
func FindAsset(rel *Release, binary, goos, goarch string) (Asset, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	archs := []string{goarch}
	if goarch == "arm" {
		archs = []string{"armv7", "armv6", "arm"}
	}
	for _, arch := range archs {
		if a, ok := rel.Asset(ArchiveName(binary, rel.Version, goos, arch)); ok {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("%w: %s has no %s for %s/%s", ErrNoAsset, rel.Tag, binary, goos, goarch)
}
