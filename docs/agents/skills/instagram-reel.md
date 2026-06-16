---
name: instagram-reel
description: Download an Instagram reel/post/video by URL using yt-dlp and send it back to the requester. Triggers on phrases like "download reel", "ig reel", "instagram reel", "grab this reel", or just an instagram.com/reel/ URL pasted in chat. NOT for: TikTok (different host), YouTube, or batch scraping accounts.
---

# instagram-reel

Download a single Instagram reel/post and deliver it.

## When to use
- User pastes an `instagram.com/reel/...`, `instagram.com/p/...`, or `instagram.com/tv/...` URL.
- User says "download this reel" / "grab this IG" / similar.

## Steps

1. Extract the URL from the message.
2. Run yt-dlp into the workspace (media must live under an allowed directory):
   ```bash
   cd <workspace>
   yt-dlp "<URL>" -o "ig_%(id)s.%(ext)s" --no-playlist
   ```
3. If yt-dlp fails with a login/age/private error, retry once with the chromium cookie jar:
   ```bash
   yt-dlp --cookies-from-browser chromium "<URL>" -o "ig_%(id)s.%(ext)s" --no-playlist
   ```
   (Note: chromium jar is often empty — anonymous fetch works for most public reels.)
4. Send the resulting mp4 back via the `message` tool with the file path under `<workspace>/`. Include a short caption (reel id is fine).
5. After delivery, reply with `NO_REPLY` (avoid duplicate messages).
6. Clean up the file afterwards if it's >20MB to keep the workspace tidy: `trash <file>`.

## Notes
- `yt-dlp --version` should be ≥ 2026.x; older versions break on IG often.
- Only handles single posts. For carousels yt-dlp returns multiple files — send them all.
- If URL has tracking params (`?igsh=...`), strip or leave them; yt-dlp tolerates both.
- Do NOT push downloaded media to any public location.
