package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	defaultAPIBase = "https://api.github.com"
	maxAPIBytes    = 4 << 20 // a release JSON is a few KB; this is a ceiling against nonsense
	apiTimeout     = 30 * time.Second
	maxRedirects   = 5
)

var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)

// Client talks to the GitHub Releases API and downloads release assets. All URLs must be https. The optional token
// raises the API rate limit; it is sent only to the API host, never to an asset host, and never appears in an error.
type Client struct {
	// APIBase is the API root, https only. Default https://api.github.com.
	APIBase string
	// Token is an optional GitHub token (see TokenFromEnv).
	Token string
	// HTTP is the underlying client. Default: a plain http.Client. Redirects are always limited to 5 hops and
	// https, whatever CheckRedirect the client had.
	HTTP *http.Client
	// UserAgent is sent with every request. Default "homie-installer".
	UserAgent string
}

// NewClient returns a client for api.github.com with the given token (empty means unauthenticated).
func NewClient(token string) *Client {
	return &Client{APIBase: defaultAPIBase, Token: token}
}

// TokenFromEnv reads the optional GitHub token from HOMIE_GITHUB_TOKEN, then GITHUB_TOKEN.
func TokenFromEnv() string {
	for _, k := range []string{"HOMIE_GITHUB_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// Release is one GitHub release, reduced to what the installer needs.
type Release struct {
	Repo        string    // "devXpro/homie-tv"
	Tag         string    // "v0.2.0"
	Version     string    // "0.2.0" (the tag without the leading v)
	Name        string    // the release title
	NotesURL    string    // the release page
	PublishedAt time.Time // zero when GitHub gave none
	Prerelease  bool
	Assets      []Asset
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string
	URL  string // the public download URL (https)
	Size int64
}

// Asset returns the asset with this exact name.
func (r *Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

type apiRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Latest returns the latest published, non-prerelease release of repo ("owner/name").
func (c *Client) Latest(ctx context.Context, repo string) (*Release, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	return c.release(ctx, repo, "/repos/"+repo+"/releases/latest")
}

// ReleaseByVersion returns the release of a specific version ("0.2.0" or "v0.2.0"); it tries the tag v0.2.0, then 0.2.0.
func (c *Client) ReleaseByVersion(ctx context.Context, repo, version string) (*Release, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	v, err := NormalizeVersion(version)
	if err != nil {
		return nil, err
	}
	rel, err := c.release(ctx, repo, "/repos/"+repo+"/releases/tags/v"+v)
	if errors.Is(err, ErrNotFound) {
		rel, err = c.release(ctx, repo, "/repos/"+repo+"/releases/tags/"+v)
	}
	return rel, err
}

func (c *Client) release(ctx context.Context, repo, path string) (*Release, error) {
	body, err := c.get(ctx, c.apiBase()+path, true, maxAPIBytes)
	if err != nil {
		return nil, err
	}
	var ar apiRelease
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("%w: release JSON: %w", ErrUpstream, err)
	}
	if ar.Draft {
		return nil, fmt.Errorf("%w: %s %s is a draft", ErrNotFound, repo, ar.TagName)
	}
	v, err := NormalizeVersion(ar.TagName)
	if err != nil {
		return nil, fmt.Errorf("%w: release tag %q is not a version", ErrUpstream, ar.TagName)
	}
	rel := &Release{
		Repo: repo, Tag: ar.TagName, Version: v, Name: ar.Name, NotesURL: ar.HTMLURL, Prerelease: ar.Prerelease,
	}
	if t, err := time.Parse(time.RFC3339, ar.PublishedAt); err == nil {
		rel.PublishedAt = t
	}
	for _, a := range ar.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
	}
	return rel, nil
}

func checkRepo(repo string) error {
	if !repoRE.MatchString(repo) || strings.Contains(repo, "..") {
		return fmt.Errorf("install: invalid repository %q (want owner/name)", repo)
	}
	return nil
}

func (c *Client) apiBase() string {
	if c.APIBase == "" {
		return defaultAPIBase
	}
	return strings.TrimRight(c.APIBase, "/")
}

// get fetches a small body (at most max bytes). api marks a request to the API host, which may carry the token.
func (c *Client) get(ctx context.Context, rawURL string, api bool, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	resp, err := c.open(ctx, rawURL, api)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: response from %s", ErrTooLarge, hostOf(rawURL))
	}
	return b, nil
}

// open starts a GET and returns the response for a 2xx status; other statuses become errors.
func (c *Client) open(ctx context.Context, rawURL string, api bool) (*http.Response, error) {
	if err := requireHTTPS(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("install: bad URL: %w", err)
	}
	ua := c.UserAgent
	if ua == "" {
		ua = "homie-installer"
	}
	req.Header.Set("User-Agent", ua)
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
	}
	hc := http.Client{}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("%w: too many redirects", ErrUpstream)
		}
		return requireHTTPS(r.URL.String())
	}
	resp, err := hc.Do(req)
	if err != nil {
		// url.Error repeats the URL only; headers (the token) are never part of it.
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	host := hostOf(rawURL)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s answered 404", ErrNotFound, host)
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
		(resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""):
		return nil, fmt.Errorf("%w: %w (set HOMIE_GITHUB_TOKEN to raise the limit)", ErrUpstream, ErrRateLimited)
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %s rejected the credentials (401)", ErrUpstream, host)
	}
	return nil, fmt.Errorf("%w: %s answered %d", ErrUpstream, host, resp.StatusCode)
}

func requireHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: %s", ErrInsecureURL, hostOf(raw))
	}
	return nil
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return "?"
}
