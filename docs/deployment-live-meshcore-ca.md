# Deploying live.meshcore.ca

## Recommended baseline
- `DISABLE_MOSQUITTO=true`
- Do **not** publish port 1883 publicly for production.
- Use external MQTT brokers (`mqtt1.meshcore.ca`, `mqtt2.meshcore.ca`) via WSS/TLS on 443.

## Caddy / reverse proxy
- Route public HTTPS traffic to the Go server service.
- Prefer Cloudflare Tunnel or equivalent reverse-proxy ingress.
- Keep Caddyfile generated/mounted from `caddy-config/` for environment-specific hostnames.
