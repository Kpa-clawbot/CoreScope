# AUDIO-WORKBENCH.md — Sound Shaping & Debug Interface

> **Archived plan.** Historical planning material; proposals, status claims, and
> instructions below may be outdated or unimplemented. For current guidance, see
> the [README](../../README.md) and [deployment guide](../../DEPLOY.md).

## Problem

Live packets arrive randomly and animate too fast to understand what's happening musically. You hear sound, but can't connect it to what the data is doing — which bytes become which notes, why this packet sounds different from that one.

## Milestone 1: Packet Jukebox

A standalone page (`#/audio-lab`) that lets you trigger packets manually and understand the data→sound mapping.

### Packet Buckets

Pre-load representative packets from the database, bucketed by type:

| Type ID | Name | Typical Size | Notes |
|---------|------|-------------|-------|
| 0x04 | ADVERT | 109-177 bytes | Node advertisements, most musical (long payload) |
| 0x05 | GRP_TXT | 18-173 bytes | Group messages, wide size range |
| 0x01 | TXT_MSG | 22-118 bytes | Direct messages |
| 0x02 | ACK/REQ | 22-57 bytes | Short acknowledgments |
| 0x09 | TRACE | 11-13 bytes | Very short, sparse |
| 0x00 | RAW | 22-33 bytes | Raw packets |

For each type, pull 5-10 representative packets spanning the size range (smallest, median, largest) and observation count range (1 obs, 10+ obs, 50+ obs).

### API

New endpoint: `GET /api/audio-lab/buckets`

Returns pre-selected packets grouped by type with decoded data and raw_hex. Server picks representatives so the client doesn't need to sift through hundreds.

### UI Layout

```
┌─────────────────────────────────────────────────────┐
│  🎵 Audio Lab                                       │
├──────────┬──────────────────────────────────────────┤
│          │                                          │
│ ADVERT   │  [▶ Play]  [🔁 Loop]  [⏱ Slow 0.5x]    │
│  ▸ #1    │                                          │
│  ▸ #2    │  ┌─ Packet Data ──────────────────────┐  │
│  ▸ #3    │  │ Type: ADVERT                       │  │
│          │  │ Size: 141 bytes (payload: 138)      │  │
│ GRP_TXT  │  │ Hops: 3  Observations: 12          │  │
│  ▸ #1    │  │ Raw: 04 8b 33 87 e9 c5 cd ea ...   │  │
│  ▸ #2    │  └────────────────────────────────────┘  │
│          │                                          │
│ TXT_MSG  │  ┌─ Sound Mapping ────────────────────┐  │
│  ▸ #1    │  │ Instrument: Bell (triangle)         │  │
│          │  │ Scale: C major pentatonic           │  │
│ TRACE    │  │ Notes: 12 (√138 ≈ 11.7)            │  │
│  ▸ #1    │  │ Filter: 4200 Hz (3 hops)           │  │
│          │  │ Volume: 0.48 (12 obs)               │  │
│          │  │ Voices: 4 (12 obs, capped)          │  │
│          │  │ Pan: -0.3 (lon: -105.2)             │  │
│          │  └────────────────────────────────────┘  │
│          │                                          │
│          │  ┌─ Note Sequence ────────────────────┐  │
│          │  │ #1: byte 0x8B → C4 (880Hz) 310ms   │  │
│          │  │     gap: 82ms (Δ=0x58)              │  │
│          │  │ #2: byte 0x33 → G3 (392Hz) 120ms   │  │
│          │  │     gap: 210ms (Δ=0xB4)             │  │
│          │  │ ...                                 │  │
│          │  └────────────────────────────────────┘  │
│          │                                          │
│          │  ┌─ Byte Visualizer ──────────────────┐  │
│          │  │ ████░░██████░░░████████░░██░░░░████ │  │
│          │  │ ↑    ↑       ↑          ↑          │  │
│          │  │ sampled bytes highlighted in payload │  │
│          │  └────────────────────────────────────┘  │
├──────────┴──────────────────────────────────────────┤
│  BPM [====●========] 120    Vol [==●===========] 30 │
│  Voice: [constellation ▾]                           │
└─────────────────────────────────────────────────────┘
```

### Key Features

1. **Play button** — triggers `sonifyPacket()` with the selected packet
2. **Loop** — retrigger every N seconds (configurable)
3. **Slow mode** — 0.25x / 0.5x / 1x / 2x tempo override (separate from BPM, multiplies it)
4. **Note sequence breakdown** — shows every sampled byte, its MIDI note, frequency, duration, gap to next. Highlights each note in real-time as it plays.
5. **Byte visualizer** — hex dump of payload with sampled bytes highlighted. Shows which bytes the voice module chose and what they became.
6. **Sound mapping panel** — shows computed parameters (instrument, scale, filter, pan, volume, voice count) so you can see exactly why it sounds the way it does.

### Playback Highlighting

As each note plays, highlight:
- The corresponding byte in the hex dump
- The note row in the sequence table
- A playhead marker on the byte visualizer bar

This connects the visual and auditory — you SEE which byte is playing RIGHT NOW.

---

## Milestone 2: Parameter Overrides

Once you can hear individual packets clearly, add override sliders to shape the sound:

### Envelope & Tone
- **Oscillator type** — sine / triangle / square / sawtooth
- **ADSR sliders** — attack, decay, sustain, release (with real-time envelope visualizer curve)
- **Scale override** — force any scale regardless of packet type (C maj pent, A min pent, E nat minor, D whole tone, chromatic, etc.)
- **Root note** — base MIDI note for the scale

### Spatial & Filter
- **Filter type** — lowpass / highpass / bandpass
- **Filter cutoff** — manual override of hop-based cutoff (Hz slider + "data-driven" toggle)
- **Filter Q/resonance** — 0.1 to 20
- **Pan lock** — force stereo position (-1 to +1)

### Voicing & Dynamics
- **Voice count** — force 1-8 voices regardless of observation count
- **Detune spread** — cents per voice (0-50)
- **Volume** — manual override of observation-based volume
- **Limiter threshold** — per-packet compressor threshold (dB)
- **Limiter ratio** — 1:1 to 20:1

### Note Timing
- **Note duration range** — min/max duration mapped from byte value
- **Note gap range** — min/max gap mapped from byte delta
- **Lookahead** — scheduling buffer (ms)

Each override has a "lock 🔒" toggle — locked = your value, unlocked = data-driven. Unlocked shows the computed value in real-time so you can see what the data would produce.

The voice module's `play()` accepts an `overrides` object from the workbench. Locked parameters override computed values; unlocked ones pass through.

---

## Milestone 3: A/B Voice Comparison

- Split-screen: two voice modules side by side
- Same packet, different voices
- "Play Both" button with configurable delay between them
- Good for iterating on v2/v3 voices against v1 constellation

---

## Milestone 4: Sequence Editor

- Drag packets into a timeline to create a sequence
- Adjust timing between packets manually
- Play the sequence as a composition
- Export as audio (MediaRecorder API → WAV/WebM)
- Useful for demoing "this is what the mesh sounds like" without waiting for live traffic

---

## Milestone 5: Live Annotation Mode

- Toggle on live map that shows the sound mapping panel for each packet as it plays
- Small floating card near the animated path showing: type, notes, instrument
- Fades out after the notes finish
- Connects the live visualization with the audio in real-time

---

## Architecture Notes

- Audio Lab is a new SPA page like packets/nodes/analytics
- Reuses existing `MeshAudio.sonifyPacket()` and voice modules
- Voice modules need a small extension: `play()` should return a `NoteSequence` object describing what it will play, not just play it. This enables the visualizer.
- Or: add a `describe(parsed, opts)` method that returns the mapping without playing
- BPM/volume/voice selection shared with live map via `MeshAudio.*`

## Implementation Order

1. API endpoint for bucketed representative packets
2. Basic page layout with packet list and play button
3. Sound mapping panel (computed parameters display)
4. Note sequence breakdown
5. Playback highlighting
6. Byte visualizer
7. Override sliders (M2)
