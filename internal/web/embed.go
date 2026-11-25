// Package web serves the admin SPA (web/, built by `make web` into dist/) from the homed binary, through MCP
// Gatekeeper's webui: the SPA's client routes answer index.html (a reload of /devices/tv works), hashed assets under
// /assets/ are cached for a year, the top-level files of the build (theme.js, the icons, the manifest) revalidate by
// ETag, and every answer carries the strict CSP of csp.txt (no inline script or style, no eval). Before `make web` the
// pages answer 503 with a hint.
//
// The public listener serves Routes (the admin at the root, the gatekeeper's /login, /setup, /join and /consent);
// v0.1's /admin paths answer 308 to /. The first-run listener serves the same build with its one route, /firstrun.
package web

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/devXpro/mcp-gatekeeper/core"
	"github.com/devXpro/mcp-gatekeeper/webui"
)

//go:embed all:dist
var dist embed.FS

// cspFile is shared with web/vite.config.ts, which sends the same policy from the mock preview.
//
//go:embed csp.txt
var cspFile string

// CSP returns the Content-Security-Policy every answer carries.
func CSP() string { return strings.TrimSpace(cspFile) }

// Routes are the SPA's client routes on the public listener (docs/ADMIN-API.md §8). A new top-level page of the
// SPA needs its route here.
var Routes = []string{
	"/", "/home", "/login", "/setup", "/join", "/consent",
	"/connect", "/account", "/account/{rest...}",
	"/devices", "/devices/{rest...}", "/activity", "/activity/{rest...}",
	"/people", "/people/{rest...}", "/settings", "/settings/{rest...}",
}

// FirstRunRoute is the first-run page's route on its own listener.
const FirstRunRoute = "/firstrun"

// noOpener are the routes that run inside an assistant's connector window: without Cross-Origin-Opener-Policy.
var noOpener = []string{"/consent"}

const notBuilt = "The admin UI is not built into this homed: run `make web`, then rebuild homed."

func build() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory
	}
	return sub
}

// New returns the SPA of the public listener: the client routes, the assets and the public files, plus the 308 of
// the old /admin paths.
func New(log *slog.Logger) (core.RouteRegistrar, error) { return newRoutes(build(), log) }

func newRoutes(fsys fs.FS, log *slog.Logger) (core.RouteRegistrar, error) {
	ui, err := webui.New(webui.Options{FS: fsys, Routes: Routes, NoOpener: noOpener, CSP: CSP(), NotBuiltHint: notBuilt,
		Logger: log})
	if err != nil {
		return nil, err
	}
	return registrars{ui, legacy{}}, nil
}

type registrars []core.RouteRegistrar

func (rs registrars) RegisterRoutes(mux *http.ServeMux) {
	for _, r := range rs {
		r.RegisterRoutes(mux)
	}
}

// legacy sends v0.1's admin paths (/admin, /admin/…) to the SPA's root, which is where the admin lives now.
type legacy struct{}

func (legacy) RegisterRoutes(mux *http.ServeMux) {
	to := http.RedirectHandler("/", http.StatusPermanentRedirect)
	mux.Handle("/admin", to)
	mux.Handle("/admin/", to)
}

// FirstRun returns the SPA of the first-run listener: /firstrun, the assets and the public files; anything else 404.
func FirstRun(log *slog.Logger) (http.Handler, error) { return firstRun(build(), log) }

func firstRun(fsys fs.FS, log *slog.Logger) (http.Handler, error) {
	ui, err := webui.New(webui.Options{FS: fsys, Routes: []string{FirstRunRoute}, CSP: CSP(), NotBuiltHint: notBuilt,
		Logger: log})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	ui.RegisterRoutes(mux)
	return mux, nil
}
