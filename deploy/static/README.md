# Frontend on a static host or CDN

The CoreScope UI is static files. This directory and `scripts/static/build.sh`
let it be served from Cloudflare Pages or any other static host, while the
existing container keeps serving the API and the classic, embedded UI. This is
the third of the deployment modes in
[docs/deployment.md](../../docs/deployment.md#deployment-modes); the other two
run the UI in a container.

This is the **anonymous** build (`AUTH=false`): it reads everything a logged-out
visitor can read, live updates included, and links to the classic site for
logging in and admin work. It needs **no server code change**, only one env var
on the server.

## How it works

- `public/deployment.js` says where the API is. The embedded UI ships it with
  `apiBase: ''` (same origin, as today). `build.sh` rewrites it for the
  static build.
- `public/api-base.js` (`window.CSApi`) is the only place that builds API and
  WebSocket URLs. A lint rule keeps raw `fetch('/api…')` out of every other file.
- The anonymous build sends no cookies, sends its three JSON `POST`s as
  `text/plain` (so the browser sends no preflight), and hides login, account and
  admin controls in favour of an "available on classic" link.

## Building

```bash
make web API_BASE=https://analyzer.example.org AUTH=false
```

writes `dist/web`: `public/` byte for byte, except that `index.html` gets a
cache buster and `deployment.js` says where the API is. Serve that folder at the
root of a hostname; the UI uses absolute paths such as `/icons/…`. `_headers`
holds the cache rules (Cloudflare Pages and Netlify read it; other hosts need
the same three rules in their own config). The variables are in step 6 below and
at the top of `scripts/static/build.sh`.

## Deploying from a fork

This repository is the software, not any one CoreScope instance: it has no
Pages project and deploys nothing. Each instance (a region, the flagship)
deploys from its own fork. Pages needs that anyway: its GitHub App is installed
on a repository you administer.

The fork carries **no code changes**. Every setting lives in the Pages project
variables, and deploying means moving a branch to a commit that already exists
upstream.

### One-time setup

1. Fork this repository into an account or org your maintainers administer.
2. In the fork, turn GitHub Actions off (Settings → Actions). Otherwise syncing
   `master` runs this repository's CI and deploy workflows there, which fail
   without its secrets. Pages builds do not use Actions.
3. Create the deploy branches `stage` and `prod`, and protect `prod` so only
   maintainers can move it.
4. Install the Cloudflare Pages GitHub App on the fork only.
5. Create one Pages project per deploy branch, on the Free plan:

   | Project | Production branch | Serves | API |
   |---|---|---|---|
   | e.g. `corescope-web-stage` | `stage` | your staging frontend | your staging server |
   | e.g. `corescope-web-prod` | `prod` | your production frontend | your production server |

   For both:
   - build command: `bash scripts/static/build.sh`;
   - build output directory: `dist/web`; root directory: blank (the
     repository root holds files over Pages' 25 MiB limit);
   - preview branches: None, or All to get a preview of every other branch
     pushed to the fork (see Previews below).

   One project on `prod` is enough if you have no staging server.
6. Environment variables (Production environment, and Preview too if previews
   are on) for each project:

   | Name | Example | Notes |
   |---|---|---|
   | `API_BASE` | `https://analyzer.example.org` | The CoreScope server. https, lowercase, no path |
   | `AUTH` | `false` | Required, no default. `false` until the server supports credentialed CORS |
   | `CHANNEL` | `beta` | `beta`, `next` or `dev` show a badge; `static` shows none |
   | `CLASSIC_URL` | `https://analyzer.example.org` | Optional. Target of the "Open in classic" links |
   | `SKIP_DEPENDENCY_INSTALL` | `1` | `build.sh` needs no npm packages |

   `COMMIT` defaults to the first 7 characters of `CF_PAGES_COMMIT_SHA`.
7. Add a custom domain to each project (for example `beta.example.org`). The
   UI uses absolute paths such as `/icons/…`, so it must be served at the root
   of a hostname.

### Deploying

Keep each frontend on the version its server runs: `prod` on the release tag
your production containers run, `stage` on upstream `master` if staging runs
the master image. Upgrade the server first, then move the branch. Move branches
by fast-forward, so they stay identical to upstream commits (`upstream` is this
repository, `region` is your fork):

```bash
git fetch upstream --tags
git push region v3.16.0:refs/heads/prod
git push region upstream/master:refs/heads/stage
```

Only move `stage` to a `master` commit whose CI passed. A pull request into
`prod` also works if you want a review step, but its merge commits make the
branch drift from upstream. Each project builds only when its branch moves.

### Previews

With preview branches on, every other branch pushed to the fork, and every pull
request between branches of the fork, gets its own deployment at
`<hash>.<project>.pages.dev` plus a branch alias such as
`my-branch.<project>.pages.dev`. Set `CHANNEL=dev` in the Preview environment
so previews show the Dev badge. Pull requests to this repository come from
forks and get no preview.

A preview is code from a branch, so keep previews anonymous (`AUTH=false`),
pointed at a staging server if you have one.

## Server

Append the static origin to the embed CORS list and restart:

```bash
# Keep every origin already listed; add the static one exactly. Never "*".
CORS_ALLOWED_ORIGINS=https://existing-embed.example.org,https://beta.example.org
```

- The env var replaces `corsAllowedOrigins` from `config.json`, so it must list
  every origin, old and new.
- An entry is an exact origin: lowercase, scheme included, no trailing slash.
- For previews, also list `https://<project>.pages.dev` and
  `https://*.<project>.pages.dev` (wildcard labels need a server with #2362; see
  `config.example.json`). The wildcard matches one label only, and the server
  refuses `https://*.pages.dev`.
- The same list admits the origin to `/ws`. A `*` entry does not work for `/ws`.

Check it:

```bash
curl -sI -H "Origin: https://beta.example.org" https://analyzer.example.org/api/stats | grep -i access-control-allow-origin
```

It must print the origin, and no `Access-Control-Allow-Credentials` line.

## Local and CI

```bash
make dev-split
```

The UI on `http://localhost:8788` calls an API-only server on
`http://localhost:3000` cross-origin, with no proxy. CI runs
`tests/e2e/test-split-anonymous-e2e.js` the same way on E2E shard 1. Pull requests
to this repository get no preview deployment; this is how to try one.

## Roll back

- **The UI:** in the Pages dashboard, roll back to the previous deployment, or
  move the deploy branch back to the previous tag
  (`git push --force-with-lease region v3.15.0:refs/heads/prod`).
- **Access from the static origin:** remove it from `CORS_ALLOWED_ORIGINS` and
  restart. The static UI then shows "Can't reach the CoreScope API"; the classic
  UI and embeds are unaffected.
