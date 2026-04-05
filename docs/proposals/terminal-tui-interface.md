# Proposal: Terminal/TUI Interface for CoreScope

**Status:** Approved for MVP
**Issue:** TBD

## Problem

CoreScope's web UI requires a browser. Operators managing remote mesh deployments often work over SSH — headless servers, Raspberry Pis, field laptops with spotty connectivity. They need to check mesh health, view packet flow, and diagnose issues without opening a browser.

## Vision

A terminal-based user interface (TUI) that connects to a CoreScope instance's API and renders key views directly in the terminal. Think `htop` for mesh networks.

---

## Expert Review

### Carmack (Performance / Data Flow)

- **bubbletea is fine for this.** The TUI is a thin API consumer — it's not processing 7.3M observations locally. The server does the heavy lifting; the TUI just renders summary data from `/api/observers/metrics/summary` (dozens of rows, not millions). No performance concern here.
- **WebSocket in a TUI — one gotcha:** reconnection. SSH sessions drop, networks flake. The TUI MUST have automatic reconnect with exponential backoff. Don't let a dropped WS kill the whole UI — show a "reconnecting..." status and keep the last-known state visible.
- **Memory footprint:** Should be trivial. The TUI holds at most a few hundred packets in a ring buffer for the live feed + summary stats. Target <20MB RSS. bubbletea itself is lightweight. The danger is unbounded packet accumulation — use a fixed-size ring buffer (e.g., last 1000 packets) for the live feed, not an ever-growing slice.
- **Batch WS messages.** Don't re-render on every single packet. Coalesce WS messages and re-render at most 10fps (every 100ms). Terminal rendering is slow — flooding it with updates causes flicker and CPU burn.

### Torvalds (Simplicity / Scope)

- **The scope is too big for an MVP.** Node detail view, sparklines, SSH server mode, multi-instance, export — delete all of that from M1. You need TWO views to prove this works: fleet dashboard table and live packet feed. That's it.
- **bubbletea vs tview:** bubbletea. Not because Elm-architecture is "clean" — because it's what the Go community actually uses now, the examples are good, and lipgloss makes table rendering trivial. Don't overthink this.
- **Over-engineering risk is HIGH.** The proposal describes 4 views, stretch features, and SSH server mode before a single line of code exists. Build the two-view demo. Ship it. Then decide what's next based on whether anyone actually uses it.
- **Same repo, `cmd/tui/`.** Don't create a separate repo for what's going to be 500 lines of Go initially. It shares the same API types. Keep it together.
- **Kill the "Open Questions" section.** Answer them: Target user = anyone with SSH access. M1 = dashboard + live feed. Same repo. Name = `corescope-tui`. Done. Stop discussing, start building.

### Doshi (Strategy / Prioritization)

- **This is an N (Neutral) feature, not an L.** It doesn't change CoreScope's trajectory — the web UI already works. But it's a solid N: it unlocks a real use case (SSH-only operators) and proves CoreScope's API is a proper platform, not just a web app backend.
- **The MVP that proves the concept:** Can an operator SSH into a Pi, run `corescope-tui --url http://analyzer:3000`, and immediately see fleet health + live packets? If yes, the concept is proven. Everything else (node detail, sparklines, alerting) is M2+.
- **Defer list:** Node detail view, RF sparklines, SSH server mode, multi-instance, export, mouse support, true-color fallback, alerting. ALL of these are M2 or later.
- **Pre-mortem — why would this fail?**
  1. Nobody uses it because the web UI is good enough (likely for most users — that's fine, this is for the SSH-only niche)
  2. The API doesn't return what the TUI needs in the right shape (validate this FIRST — curl the endpoints before writing any TUI code)
  3. Scope creep kills the demo — someone adds "just one more view" and it's never done
- **Opportunity cost:** Low. This is a day of work for the MVP. The API already exists. The risk is spending a week on polish nobody asked for.

---

## MVP Definition (Demo Target)

**Goal:** A working two-view TUI that connects to any CoreScope instance and displays real-time mesh data in a terminal. Buildable in one focused session.

### View 1: Fleet Dashboard (default)
```
┌─ CoreScope TUI ──────────────────────────────────────────┐
│ Connected: analyzer.00id.net | Observers: 35 | ● Live    │
├──────────────────────────────────────────────────────────┤
│ Observer          │ Nodes │ Pkts/hr │ NF     │ Status    │
│ GY889 Repeater    │   142 │     312 │ -112   │ ● active  │
│ C0ffee SF         │    89 │     201 │ -108   │ ● active  │
│ ELC-ONNIE-RPT-1  │    67 │     156 │  -95   │ ▲ warning │
│ Bar Repeater      │    12 │       3 │  -76   │ ▼ stale   │
└──────────────────────────────────────────────────────────┘
  Tab: [Dashboard] [Live Feed]    q: quit    ?: help
```

- **Data source:** `GET /api/observers/metrics/summary`
- **Refresh:** Poll every 5s (simple, no WS needed for this view)
- **Sort:** By observer name initially. Stretch: column sort with arrow keys.

### View 2: Live Packet Feed
```
┌─ Live Feed ──────────────────────────────────────────────┐
│ 14:32:01 ADVERT   GY889 Repeater       → 3 hops  -112dB │
│ 14:32:02 GRP_TXT  #test "hello world"  → 5 hops   -98dB │
│ 14:32:03 TXT_MSG  [encrypted]          → 2 hops  -105dB │
│ 14:32:04 CHAN     #sf "anyone on?"     → 8 hops   -91dB │
│ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │
└──────────────────────────────────────────────────────────┘
  Tab: [Dashboard] [Live Feed]    p: pause    q: quit
```

- **Data source:** WebSocket (`/ws`)
- **Buffer:** Ring buffer, last 500 packets max
- **Render:** Coalesce updates, re-render at most 10fps
- **Reconnect:** Auto-reconnect with exponential backoff (1s, 2s, 4s, max 30s)

### What's NOT in MVP
- Node detail view
- RF sparklines
- SSH server mode (`--serve-ssh`)
- Multi-instance support
- Export to CSV/JSON
- Mouse support
- Alerting / terminal bell
- Color theme configuration
- Custom filters (/ to filter)

### Technical Decisions (Resolved)
| Question | Answer |
|---|---|
| Target user | SSH operators, power users, field techs |
| Library | bubbletea + lipgloss |
| Location | `cmd/tui/` in same repo |
| Binary name | `corescope-tui` |
| Min terminal | 256-color, 80x24 |
| State | Stateless — pure API consumer, no local DB |

### Implementation Plan
1. Scaffold `cmd/tui/main.go` — flag parsing (`--url`), bubbletea app init
2. Fleet dashboard model — fetch `/api/observers/metrics/summary`, render table
3. Live feed model — WebSocket connect, ring buffer, packet rendering
4. Tab switching between views
5. Status bar (connection state, help hints)
6. Test against `https://analyzer.00id.net`

---

## Future Milestones (post-MVP, not scheduled)

### M2: Navigation & Detail
- Node detail view (select observer → see its packets/neighbors)
- Keyboard navigation (j/k, Enter, Esc)
- `/` to filter packets

### M3: Visualization
- RF noise floor sparklines (`▁▂▃▅▇█`)
- Health history over time
- Color theme support

### M4: Advanced
- SSH server mode (`--serve-ssh :2222`)
- Multi-instance tabs
- Export current view to stdout (CSV/JSON)
- Desktop notifications on anomalies
