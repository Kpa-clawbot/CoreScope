---
name: video-subtitle
description: >
  Add translated subtitles to video clips with optional logo overlay.
  Transcribes via Whisper (SSH to Windows), translates to Bulgarian, hardcodes subs with ffmpeg.
  Handles songs via LRC files. Triggers: "сложи субтитри", "add subs", "translate subs",
  "subtitle this video", "BG subs". NOT for: subtitle timing fixes (use srt-calibrate).
---

# Video Subtitle

## Workflow

1. **Transcribe** — Run Whisper on Windows via SSH (see scripts/whisper_transcribe.sh)
2. **Translate** — Translate SRT to Bulgarian. Unknown words → English. **NEVER Russian.**
3. **Hardcode subs** — Burn subtitles into video with ffmpeg (see scripts/hardcode_subs.sh)
4. **Logo overlay** — Optional animated logo on top

## Whisper Transcription

```bash
# Copy video to Windows first, then run:
bash scripts/whisper_transcribe.sh /path/to/video.mp4
```

- Whisper machine: `<user>@<lan-host>`
- Script: `C:\Temp\whisper_srt.py`
- **ALWAYS force `language="en"`** to prevent Russian detection
- Output: SRT file on Windows, SCP back

### Songs (Whisper won't work)

Music drowns vocals. Instead:
1. Find LRC lyrics online (web search `"song title" lrc lyrics`)
2. Convert LRC → SRT format
3. Use srt-calibrate skill to sync timing with Whisper offset

## Translation Rules

- Target language: **Bulgarian** (always)
- Unknown/untranslatable words → keep in **English**
- **NEVER output Russian** — if unsure, use English
- Proper nouns stay as-is

## Subtitle Styling (FIXED — DO NOT CHANGE)

- Font: DejaVu Sans, size 20
- Color: white text, semi-transparent gray background
- Position: **bottom** if no existing subs; **TOP** (Alignment=6, MarginV=10) if original subs at bottom

## Hardcoding Subs

```bash
bash scripts/hardcode_subs.sh input.mp4 subs.ass output.mp4 [logo_position]
```

## Logo Overlay

- File: `<workspace>/<logo>.mp4` (480×480, 15s animated, loops)
- Apply oval mask, scale down (~100px), position flexibly (like artist signature)
- Default position: bottom-right. Override with parameter.

## Output Format (MANDATORY)

```
-c:v libx264 -profile:v main -level 4.0 -pix_fmt yuv420p
-c:a aac -b:a 192k -ar 44100
-movflags +faststart
```
