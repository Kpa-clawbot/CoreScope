---
name: usenet-movie
description: "End-to-end movie download pipeline: search NZBgeek, download via SABnzbd, monitor, upload to GDrive, notify user. Use for any movie download request. Shorthand triggers: 'dm QUERY' (download movie), 's QUERY' (search only), 'q' (queue status), 'gd' (gdrive link), 'del FILE' (delete from gdrive), 'link FILE' (share link). Full triggers: 'свали филм', 'download movie', 'търси филм', 'search movie', 'dm', 'movie download'. NOT for: TV shows, music, or non-movie downloads."
---

# Usenet Movie Download Pipeline

All commands use the existing script at `<workspace>/usenet.py`. Do NOT recreate it.

## Shorthand Dispatch

| Input | Action |
|-------|--------|
| `dm <query>` | Full pipeline: search → user picks → download → track |
| `s <query>` | Search only, show results |
| `q` | Show SABnzbd queue status |
| `gd` | Send GDrive folder link |
| `del <file>` | Delete file from GDrive |
| `link <file>` | Get share link for file |

## Commands

```bash
python3 <workspace>/usenet.py search "<query>"    # Search NZBgeek
python3 <workspace>/usenet.py download <guid>     # Send to SABnzbd
python3 <workspace>/usenet.py queue               # Queue status
python3 <workspace>/usenet.py history             # Completed downloads
python3 <workspace>/usenet.py list                # List GDrive movies
python3 <workspace>/usenet.py link "<name>"       # Get share link
```

## Workflow

### `dm <query>` — Full Download Pipeline

1. **Search**: Run `usenet.py search "<query>"`. For 4K requests, append "2160p" to the query.
2. **Present results** to user with numbered list (title, size, quality info).
3. **User picks** a result → run `usenet.py download <guid>`.
4. **Track** in `<workspace>/memory/movie-downloads.json` under `"pending"`.
5. **Monitor**: SABnzbd auto-uploads to GDrive on completion. Check with `usenet.py queue` / `usenet.py history`.
6. **Notify** user with GDrive link when upload completes.

### `del <file>` — Delete from GDrive

ALWAYS use `--drive-use-trash=false` to avoid filling quota:
```bash
rclone purge --drive-use-trash=false "gdrive:Movie downloads/<file>"
```

To empty trash: `rclone cleanup gdrive:`

## Tracking

Maintain `<workspace>/memory/movie-downloads.json`:
- **pending**: Downloads in progress or waiting for upload
- **watching**: Movies not yet available on usenet (check periodically)

## Key Links

- GDrive folder: https://drive.google.com/open?id=1sBhE-sW--bS39dQvnrN32Kw2EbXI2WIS
- GDrive remote: `gdrive:Movie downloads/`
- For full pipeline details, see [references/movie-workflow.md](references/movie-workflow.md).
