#!/bin/sh

# Copy example config if no config.json exists (check volume mount first)
if [ ! -f /app/data/config.json ]; then
  echo "[entrypoint] No config.json found in /app/data/, copying example"
  cp /app/config.example.json /app/data/config.json
fi

# Symlink so the app finds it at /app/config.json
ln -sf /app/data/config.json /app/config.json

# Same for theme.json (optional)
if [ -f /app/data/theme.json ]; then
  ln -sf /app/data/theme.json /app/theme.json
fi

exec /usr/bin/supervisord -c /etc/supervisor/conf.d/supervisord.conf
