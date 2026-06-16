---
name: photo-slideshow
description: >
  Create photo slideshow videos with music, dissolve transitions, and animated logo overlay.
  Supports TV (1920x1080) and Phone (1080x1920) variants, splitting into parts.
  Triggers: "направи слайдшоу", "slideshow", "make slideshow", "slideshow from photos".
  NOT for: video editing or subtitle work.
---

# Photo Slideshow

## Workflow

1. Collect photos (local paths or download from URLs)
2. Run `scripts/make_slideshow.sh` to generate slideshow
3. Optionally create Phone variant (1080x1920)
4. Optionally split into parts (ALWAYS re-encode, NEVER `-c copy`)

## Usage

```bash
bash scripts/make_slideshow.sh <photo_dir> <output.mp4> [options]
```

Options set via env vars:
- `DURATION=5` — seconds per photo
- `RESOLUTION=1920x1080` — target resolution (use 1080x1920 for phone)
- `MUSIC=/path/to/music.mp3` — background music file
- `LOGO=1` — include logo (default: 1, always on)
- `LOGO_POS=br` — logo position (br/bl/tr/tl)
- `TRANSITION_DURATION=1` — crossfade duration in seconds

## Defaults

| Parameter | Value |
|-----------|-------|
| Duration per photo | 5 seconds |
| Transition | Dissolve (xfade crossfade) |
| Logo | **ALWAYS included** (`<logo>.mp4`) |
| Resolution (TV) | 1920×1080 |
| Resolution (Phone) | 1080×1920 |

## Output Format (MANDATORY)

```
-c:v libx264 -profile:v main -level 4.0 -pix_fmt yuv420p
-c:a aac -ar 44100 -movflags +faststart
```

## Splitting into Parts

When splitting a long slideshow:
- **ALWAYS re-encode** at cut points
- **NEVER use `-c copy`** — causes black frames at cuts
- Use `-ss` and `-t` with full re-encode

```bash
# Example: split into 60-second parts
ffmpeg -i full.mp4 -ss 0 -t 60 $ENC part1.mp4
ffmpeg -i full.mp4 -ss 60 -t 60 $ENC part2.mp4
```

## Logo

- File: `<workspace>/<logo>.mp4` (480×480, 15s animated, loops)
- Oval mask, scaled to ~100px, positioned like artist signature
- Included by default — user must explicitly say "без лого" to skip

## Photo Preparation

Photos are auto-resized and padded (black bars) to target resolution, preserving aspect ratio.
