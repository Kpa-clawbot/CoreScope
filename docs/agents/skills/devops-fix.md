---
name: devops-fix
description: Live operational fixes — SSH, docker, sqlite, log triage, container health on staging/prod.
triggers:
  - "staging is down"
  - "prod 502"
  - "container OOM"
  - "trim staging DB"
  - "ingestor stuck"
  - "restart staging"
  - "MQTT not connecting on prod"
---

# devops-fix — Live Operational Fixes

## Purpose
Diagnose and fix live operational issues on staging/prod infrastructure. SSH, docker exec/restart, sqlite operations, log triage, container health.

## Scope
- Container management (docker restart, logs, exec)
- SQLite operations on live DBs (backup first!)
- Log triage and error identification
- MQTT connectivity diagnosis
- Service health checks

## Connection details
Read from MEMORY.md — do NOT hardcode IPs/credentials in this file.

## Process
1. Identify symptom (502, OOM, stuck, disconnected)
2. SSH to relevant host (from MEMORY.md)
3. Check container status: `docker ps`, `docker logs --tail 50 <container>`
4. Identify root cause from logs
5. Apply fix (restart, config change, DB trim)
6. Verify fix (curl endpoint, check logs)

## Safety rules
- **BACKUP before any destructive operation**: `cp db.sqlite db.sqlite.bak-$(date +%s)`
- **Stop container before raw SQL on its DB**: `docker stop <c> && sqlite3 ... && docker start <c>`
- **Never `--force` destructive commands** without explicit user confirmation
- **Never DELETE without WHERE** on a live DB
- **Log every action** — write to daily memory file what you did

## What NOT to do
- Run destructive SQL without backup
- Restart prod without checking if staging has the same issue first
- Hardcode credentials in commands (use MEMORY.md references)
- Assume "restart fixes it" without understanding root cause
- Make config changes without documenting the before/after

## Output format
```
## Diagnosis
- Symptom: <what's broken>
- Root cause: <why>
- Evidence: <log line / status output>

## Fix applied
- Action: <what you did>
- Backup: <path to backup>
- Verification: <how you confirmed it works>

## Prevention
- <suggestion to prevent recurrence>
```
