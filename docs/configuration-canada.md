# MeshCore Canada configuration

Use `config.meshcore-ca.example.json` for operator-oriented Canadian deployments.
Use `config.live.meshcore.ca.example.json` for public-safe defaults.

Both profiles include:
- MeshCore Canada Live branding
- MeshCore Canada logo and blue MeshCore.ca theme defaults
- Canada map fallback center `[56.1304, -106.3468]`, zoom `4`
- Canada IATA region set (YKF, YYZ, YTZ, YOW, YHM, YUL, YQB, YVR, YYC, YEG, YXE, YQR, YWG, YHZ, YYT, YXY, YZF, YFB)
- MeshCore.ca-oriented home links

## Current production target

For the MeshCore.ca rollout, new dev and production deployments should prefer
Postgres by setting `DB_DRIVER=postgres` and `DATABASE_URL`. SQLite remains
supported as a compatibility fallback and rollback source during cutover.

The default home title is `Canada Meshcore Corescope`, which renders as
`Welcome to Canada Meshcore Corescope` on the first-run chooser.
