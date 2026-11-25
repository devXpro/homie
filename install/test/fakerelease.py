"""A fake GitHub release server for the home.sh tests (standard library only, run it from a venv).

  fakerelease.py build ROOT            write the fake releases under ROOT
  fakerelease.py serve ROOT PORTFILE INSTALL_DIR
                                       serve ROOT on a free port (written to PORTFILE) until killed

Routes, the same shapes as github.com:
  /latest                      302 to /tag/<contents of ROOT/LATEST>  (change the file to "publish" a release)
  /download/<tag>/<file>       the release assets
  /raw/<ref>/install/<path>    the files of INSTALL_DIR, like raw.githubusercontent.com
  /cf/<variant>/<file>         fake cloudflared assets (cloudflared-linux-<arch>, cloudflared-darwin-<arch>.tgz) and the
                               sha256 files linux.sha256 / darwin.sha256 that go with --cloudflared-sha256.
                               Variants: new (the pinned version), old (an older one), evil (a tgz that climbs out)
Every request path is appended to ROOT/requests.log.
"""

import gzip
import hashlib
import http.server
import io
import os
import sys
import tarfile
import re

def pinned_cloudflared():
    """The version pinned in install/home.sh, so the fake "new" cloudflared is the one the installer wants."""
    here = os.path.dirname(os.path.abspath(__file__))
    with open(os.path.join(here, "..", "home.sh")) as f:
        m = re.search(r'^readonly CLOUDFLARED_VERSION="([^"]+)"', f.read(), re.M)
    return m.group(1)


PINNED_CLOUDFLARED = pinned_cloudflared()
PLATFORMS = ["darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "linux_armv7"]

# A stand-in for homed: a shell script, so one archive content serves every CPU.
FAKE_HOMED = """#!/bin/sh
# Fake homed for the install tests (version @VERSION@).
case "${1:-}" in
version)
	echo "homed @VERSION@"
	;;
run)
	printf '%s|%s|%s|@VERSION@\\n' "$$" "$(id -un)" "${HOMED_DATA_DIR:-}" >>"$HOMED_DATA_DIR/runs"
	# LAN checks the systemd sandbox must allow: a UDP broadcast and a multicast group join.
	if [ -e "$HOMED_DATA_DIR/nettest" ] && command -v socat >/dev/null 2>&1; then
		echo x | socat -u - UDP-DATAGRAM:255.255.255.255:9,broadcast >/dev/null 2>&1
		bc=$?
		timeout 1 socat -u UDP4-RECV:19000,reuseaddr,ip-add-membership=239.255.255.250:0.0.0.0 - >/dev/null 2>&1
		mc=$?
		echo "broadcast=$bc multicast=$mc" >"$HOMED_DATA_DIR/nettest.out"
	fi
	trap 'exit 75' USR1
	trap 'exit 0' TERM INT
	while :; do
		sleep 1 &
		wait $!
	done
	;;
cloudflare)
	# The CLI the installer calls for --domain / --quick-tunnel. It logs who ran it, on which data dir and which
	# cloudflared the PATH gives it (the installed one, next to homed or Homebrew's).
	printf '%s|%s|%s|%s\n' "$*" "$(id -un)" "${HOMED_DATA_DIR:-}" "$(command -v cloudflared || echo none)" \
		>>"${HOMED_DATA_DIR:-/tmp}/cloudflare.log"
	cat >/dev/null
	case "${2:-}" in
	connect)
		[ -n "${3:-}" ] || exit 2
		if [ "$3" = fail.example.com ]; then
			echo "the login link expired" >&2
			exit 1
		fi
		echo "Open this link and log in to Cloudflare: https://dash.cloudflare.com/login/fake"
		;;
	quick | status | disconnect) ;;
	*) exit 2 ;;
	esac
	;;
admin)
	if [ "${2:-}" = firstrun ]; then
		echo "The first-run page is open until 23:59 on:"
		echo "  http://192.0.2.2:8090/firstrun"
		echo "PIN: 1234 5678 (it changes at 23:59)"
		exit 0
	fi
	exit 2
	;;
env)
	echo "$(id -un) ${HOMED_DATA_DIR:-}"
	;;
*)
	exit 2
	;;
esac
"""


def archive(name, version, wrapped=False, evil=False):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tf:
        prefix = name[: -len(".tar.gz")] + "/" if wrapped else ""
        data = FAKE_HOMED.replace("@VERSION@", version).encode()
        info = tarfile.TarInfo(prefix + "homed")
        info.size, info.mode = len(data), 0o755
        tf.addfile(info, io.BytesIO(data))
        lic = b"fake license\n"
        info = tarfile.TarInfo(prefix + "LICENSE")
        info.size, info.mode = len(lic), 0o644
        tf.addfile(info, io.BytesIO(lic))
        if evil:
            info = tarfile.TarInfo("../evil")
            info.size = 1
            tf.addfile(info, io.BytesIO(b"x"))
    return buf.getvalue()


FAKE_CLOUDFLARED = """#!/bin/sh
# Fake cloudflared for the install tests.
case "${1:-}" in
--version | version) echo "cloudflared version @VERSION@ (built fake)" ;;
*) exit 2 ;;
esac
"""


def cloudflared_tgz(version, evil=False):
    buf = io.BytesIO()
    # mtime 0 everywhere, so the same input always gives the same bytes (and the same sha256).
    with gzip.GzipFile(fileobj=buf, mode="wb", mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode="w") as tf:
            data = FAKE_CLOUDFLARED.replace("@VERSION@", version).encode()
            info = tarfile.TarInfo("cloudflared")
            info.size, info.mode = len(data), 0o755
            tf.addfile(info, io.BytesIO(data))
            if evil:
                info = tarfile.TarInfo("../evil")
                info.size = 1
                tf.addfile(info, io.BytesIO(b"x"))
    return buf.getvalue()


def write_cloudflared(root, variant, version, evil=False):
    d = os.path.join(root, "cf", variant)
    os.makedirs(d, exist_ok=True)
    linux = FAKE_CLOUDFLARED.replace("@VERSION@", version).encode()
    darwin = cloudflared_tgz(version, evil)
    for a in ("amd64", "arm64", "arm"):
        with open(os.path.join(d, "cloudflared-linux-" + a), "wb") as f:
            f.write(linux)
    for a in ("amd64", "arm64"):
        with open(os.path.join(d, "cloudflared-darwin-%s.tgz" % a), "wb") as f:
            f.write(darwin)
    with open(os.path.join(d, "linux.sha256"), "w") as f:
        f.write(hashlib.sha256(linux).hexdigest())
    with open(os.path.join(d, "darwin.sha256"), "w") as f:
        f.write(hashlib.sha256(darwin).hexdigest())


def write_release(root, tag, platforms=PLATFORMS, wrapped=False, evil=False, bad_sums=False):
    version = tag.lstrip("v")
    d = os.path.join(root, "download", tag)
    os.makedirs(d, exist_ok=True)
    lines = []
    for p in platforms:
        name = "homed_%s_%s.tar.gz" % (version, p)
        data = archive(name, version, wrapped, evil)
        with open(os.path.join(d, name), "wb") as f:
            f.write(data)
        digest = hashlib.sha256(b"tampered" if bad_sums else data).hexdigest()
        lines.append("%s  %s\n" % (digest, name))
    with open(os.path.join(d, "checksums.txt"), "w") as f:
        f.writelines(lines)


def build(root):
    write_release(root, "v0.0.1")
    write_release(root, "v0.0.2")
    write_release(root, "v0.0.3", wrapped=True)  # GoReleaser wrap_in_directory
    write_release(root, "v0.0.4", bad_sums=True)  # the archive does not match checksums.txt
    write_release(root, "v0.0.5", evil=True)  # an entry that climbs out of the directory
    write_release(root, "v0.0.6", platforms=["linux_arm64"])  # no build for the other platforms
    write_cloudflared(root, "new", PINNED_CLOUDFLARED)
    write_cloudflared(root, "old", "2026.10.0")
    write_cloudflared(root, "evil", PINNED_CLOUDFLARED, evil=True)
    with open(os.path.join(root, "LATEST"), "w") as f:
        f.write("v0.0.1\n")


class Handler(http.server.BaseHTTPRequestHandler):
    root = ""
    install_dir = ""

    def log_message(self, fmt, *args):  # the request log is a file, not stderr
        with open(os.path.join(self.root, "requests.log"), "a") as f:
            f.write("%s %s\n" % (self.command, self.path))

    def reply(self, code, body=b"", headers=None):
        self.send_response(code)
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path == "/latest":
            with open(os.path.join(self.root, "LATEST")) as f:
                tag = f.read().strip()
            self.reply(302, headers={"Location": "http://%s/tag/%s" % (self.headers.get("Host"), tag)})
            return
        if path.startswith("/download/") or path.startswith("/cf/"):
            self.serve_file(os.path.join(self.root, path[1:]), self.root)
            return
        if path.startswith("/raw/"):
            parts = path.split("/", 3)  # "", raw, ref, install/...
            if len(parts) == 4 and parts[3].startswith("install/"):
                self.serve_file(os.path.join(self.install_dir, parts[3][len("install/"):]), self.install_dir)
                return
        self.reply(404, b"not found\n")

    do_HEAD = do_GET

    def serve_file(self, p, base):
        p = os.path.realpath(p)
        base = os.path.realpath(base)
        if not p.startswith(base + os.sep) or not os.path.isfile(p):
            self.reply(404, b"not found\n")
            return
        with open(p, "rb") as f:
            self.reply(200, f.read(), {"Content-Type": "application/octet-stream"})


def serve(root, portfile, install_dir):
    Handler.root = os.path.abspath(root)
    Handler.install_dir = os.path.abspath(install_dir)
    srv = http.server.ThreadingHTTPServer(("0.0.0.0", 0), Handler)
    with open(portfile, "w") as f:
        f.write(str(srv.server_address[1]))
    srv.serve_forever()


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "build":
        build(sys.argv[2])
    elif len(sys.argv) == 5 and sys.argv[1] == "serve":
        serve(sys.argv[2], sys.argv[3], sys.argv[4])
    else:
        sys.exit(__doc__)
