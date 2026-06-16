---
name: srt-calibrate
description: >
  Sync/calibrate subtitle timing using Whisper as reference. Compares user's SRT against
  Whisper transcription, calculates average time offset, shifts all timestamps.
  Handles Windows-1251 → UTF-8 encoding. Supports LRC → SRT conversion.
  Triggers: "субтитрите не съвпадат", "sync srt", "калибрирай субтитри",
  "fix subtitle timing", "srt offset". NOT for: adding/translating subs (use video-subtitle).
---

# SRT Calibrate

## Workflow

1. **Whisper reference** — Transcribe first 3 minutes of video via SSH to Windows
2. **Compare** — Match 5+ dialogue lines between user SRT and Whisper SRT
3. **Calculate offset** — Average time difference across matched lines
4. **Shift** — Apply offset to entire SRT
5. **Encoding** — Convert Windows-1251 → UTF-8 if needed

## Step 1: Get Whisper Reference

```bash
# Extract first 3 minutes
ffmpeg -i video.mp4 -t 180 -c copy /tmp/first3min.mp4

# Transcribe on Windows (force English to avoid Russian)
scp /tmp/first3min.mp4 <user>@<lan-host>:"C:\\Temp\\first3min.mp4"
ssh <user>@<lan-host> "py -3.10 C:\\Temp\\whisper_srt.py C:\\Temp\\first3min.mp4 --language en"
scp <user>@<lan-host>:"C:\\Temp\\first3min.srt" /tmp/whisper_ref.srt
```

## Step 2: Compare and Calculate Offset

```bash
python3 scripts/compare_timing.py user_subs.srt /tmp/whisper_ref.srt
# Output: average offset in seconds (e.g., +2.35 or -1.80)
```

Algorithm: match dialogue lines (skip music cues like ♪, [Music]), find text similarity > 60%, average time differences. Need 5+ matches for reliable offset.

## Step 3: Shift SRT

```bash
python3 scripts/shift_srt.py user_subs.srt <offset_seconds> output.srt
# Example: python3 scripts/shift_srt.py subs.srt -2.35 subs_fixed.srt
```

## Encoding

Bulgarian SRTs from Windows often use Windows-1251. Scripts auto-detect and convert to UTF-8.

## LRC Files (Song Lyrics)

1. Convert LRC → SRT: parse `[mm:ss.xx] text` lines into SRT format
2. Then calibrate with same Whisper comparison method
