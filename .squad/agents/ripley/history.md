# Ripley — Support Engineer History

## Core Context
- Project: CoreScope — real-time LoRa mesh packet analyzer
- User: Kpa-clawbot
- Joined the team 2026-03-27 to handle community support and triage

## Learnings

- **Staleness thresholds (2026-03-27):** Nodes have per-role health calculations:
  - **Companions & sensors:** 24-hour stale threshold
  - **Infrastructure (repeaters, rooms):** 72-hour stale threshold
  - All-time node count tracked separately (new 	otalNodesAllTime field in /api/stats)
  - 7-day active window used for stats endpoint 	otalNodes display
  - Source: getNodeStatus() in oles.js, used by live page pruning every 60s

- **Phantom nodes incident (2026-03-27):** Cascadia mesh instance showed 7,308 nodes (6,638 repeaters) when real count ~200-400. Root cause: utoLearnHopNodes() created stubs for unresolved hop prefixes. Fixed at backend + frontend real-time pruning. Now properly cleaned at startup.

- **Database state (2026-03-27):** Staging DB (185MB, 50K transmissions, 1.2M observations) successfully merged with prod (21MB). Merged DB now 51,723 tx + 1,237,186 obs. Load time 8,491ms, memory 860MiB RSS. No data loss. Backups retained 7 days.

- **Team structure:** 6 agents active during massive session (Kobayashi lead, Hicks backend, Newt frontend, Bishop tester, Hudson DevOps, Ripley support). All use claude-opus-4.6 per user directive. Delivered 6 bug fixes + Go rewrite in one day.
