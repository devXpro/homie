.PHONY: lint test vuln build web e2e privacy

lint:
	golangci-lint run ./...

test:
	go test -race ./...

# Known vulnerabilities reachable from the code (the go.mod toolchain's standard library and every module); CI runs it too.
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

# homed embeds the admin SPA (internal/web/dist), so the SPA is built first; without it the admin answers 503.
build: web
	@if [ -d cmd/homed ]; then go build -o bin/homed ./cmd/homed; else echo "cmd/homed not present yet, skipping"; fi

web:
	@if [ -d web ]; then npm --prefix web ci && npm --prefix web run build; else echo "web/ not present yet, skipping"; fi

# The generic private-detail patterns (local paths, private LAN addresses, real MAC addresses); CI runs it too.
privacy:
	.github/privacy-check.sh

# Playwright against a real homed (docs/TESTING.md): builds the SPA, then web/tests/real builds homed and the fake
# plugin into a temp dir and drives the admin from the first run on. Needs Chromium for Playwright
# once: npx --prefix web playwright install chromium.
e2e: web
	npm --prefix web run e2e:real
