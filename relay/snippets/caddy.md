# Existing Caddy

Use this when a Caddy already owns ports 80 and 443 and runs in a docker network
that `homie-frps` has joined (`--network NAME`, default `proxy`).

Add the block to the Caddyfile (replace the domain):

```
homie.example.com {
	reverse_proxy homie-frps:7000 {
		stream_close_delay 30m
	}
}
```

`stream_close_delay 30m` is required. Without it Caddy closes every WebSocket
on each config reload, and the home box has to reconnect (a few seconds of
"home is offline" for the people using it). Other WebSocket routes of yours keep
their own `stream_close_delay`; it is set per site.

Reload safely:

```sh
docker exec <caddy-container> caddy validate --config /etc/caddy/Caddyfile
docker exec <caddy-container> caddy reload --config /etc/caddy/Caddyfile
```

If the Caddyfile is a single-file bind mount, edit it in place
(`cat new > Caddyfile` or an editor that keeps the inode). `sed -i`, `mv` and
most "atomic save" editors replace the file, and the container keeps seeing the
old one.

No Host rewriting is needed: Caddy passes the original `Host` header to frps,
and frps routes by it.
