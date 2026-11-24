# Homie hub (homed) for NAS boxes and other Docker hosts.
#
# This is the RELEASE image: GoReleaser (dockers_v2 in .goreleaser.yaml) builds homed for linux/amd64, linux/arm64 and
# linux/arm/v7 first and puts the finished binaries in the build context as <os>/<arch>[/<variant>]/homed, next to this
# file and LICENSE. Nothing is compiled here. To build an image from the sources instead, use Dockerfile.dev.
#
#   docker run -d --name homie --restart unless-stopped --network host -v homie:/data ghcr.io/devxpro/homie
#   docker logs homie                                  # the first-run page address
#   docker exec homie homed admin firstrun             # its address and PIN (also: homed admin setup-link)
#   docker exec -i homie homed relay connect -         # then paste the connect code of server.sh
#
# --network host is required, not a convenience. The hub finds devices with SSDP (multicast) and wakes them with
# Wake-on-LAN (UDP broadcast); neither crosses the NAT of Docker's default bridge network, so a bridged container sees
# no TV and cannot wake one. Host networking puts the container on the home network itself. Docker Desktop on macOS (and
# Windows) runs containers inside a Linux VM, so "host" means that VM and not your Mac's LAN: discovery and wake do not
# work there. On a Mac use the Homebrew formula or install/home.sh; Docker is for a Linux box (a NAS, a Pi).
#
# The hub runs as an unprivileged user (uid 10001) and keeps all its state in the volume /data (mode 0700). With a
# bind mount instead of a named volume, give that directory to uid 10001 (chown 10001:10001 DIR) or pass --user.
# Plugins you install in the admin live in the volume too. The hub's own binary is root-owned and read-only here, so
# "Settings > Updates" cannot replace it: update the container with `docker pull` and a new `docker run`.
FROM alpine:3.22

# TARGETPLATFORM is linux/amd64, linux/arm64 or linux/arm/v7: the directory GoReleaser put the binary in.
ARG TARGETPLATFORM

# ca-certificates: TLS to the relay (wss) and to GitHub (plugin installs). tzdata: local times. tini: reaps the plugin
# processes' orphans, the hub itself is not an init.
RUN apk add --no-cache ca-certificates tzdata tini \
	&& addgroup -S -g 10001 homie \
	&& adduser -S -u 10001 -G homie -h /data -H -s /sbin/nologin homie \
	&& install -d -m 0700 -o homie -g homie /data

COPY --chmod=0755 $TARGETPLATFORM/homed /usr/local/bin/homed
COPY LICENSE /usr/share/licenses/homie/LICENSE

ENV HOMED_DATA_DIR=/data
VOLUME /data
WORKDIR /data
USER 10001:10001

# homed listens on 127.0.0.1:8080 unless homed.yaml says otherwise; with --network host that is the host's loopback.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
	CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/homed"]
CMD ["run"]
