# Existing nginx

nginx must forward the frpc WebSocket (`GET /~!frp`) and normal HTTP to the same
upstream, `homie-frps:7000`. The home box keeps one WebSocket open for days,
so the timeouts matter.

In the `http {}` context:

```nginx
map $http_upgrade $connection_upgrade {
	default upgrade;
	''      close;
}
```

The server block (TLS configured as you usually do, for example with certbot):

```nginx
server {
	listen 443 ssl;
	http2 on;
	server_name homie.example.com;

	# ssl_certificate / ssl_certificate_key ...

	# Resolve the container at request time, so nginx still starts when
	# homie-frps is down. 127.0.0.11 is docker's embedded DNS.
	resolver 127.0.0.11 valid=10s;
	set $homie_upstream http://homie-frps:7000;

	client_max_body_size 50m;

	location / {
		proxy_pass $homie_upstream;
		proxy_http_version 1.1;
		proxy_set_header Host $host;
		proxy_set_header X-Real-IP $remote_addr;
		proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
		proxy_set_header X-Forwarded-Proto $scheme;
		proxy_set_header Upgrade $http_upgrade;
		proxy_set_header Connection $connection_upgrade;
		proxy_read_timeout 1h;
		proxy_send_timeout 1h;
		proxy_buffering off;
	}
}
```

`nginx -s reload` is graceful: the old worker keeps the open WebSocket until it
closes. Set `worker_shutdown_timeout` only if you accept dropping the channel on
reload.
