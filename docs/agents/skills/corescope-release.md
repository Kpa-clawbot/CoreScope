---
name: corescope-release
description: Cut a CoreScope release end-to-end — verify CI green on target SHA, finalize notes, tag, wait for tag-CI to publish the container, verify in GHCR, then (and only then) hand the operator upgrade commands. Use for any "ship vX.Y.Z" or "tag the release" request on Kpa-clawbot/CoreScope. Prevents the v3.9.0 fire-drill class: tagging a SHA whose CI fails → no Docker publish → operator gets 404 from upgrade commands. Triggers: 'cut release', 'tag vX.Y.Z', 'ship vX.Y.Z', 'release CoreScope', 'publish vX.Y.Z'.
---

# corescope-release

End-to-end release for `Kpa-clawbot/CoreScope`. Read every step. Skip none.

## Hard lessons that earned each step

| Failure mode (date) | Step that prevents it |
|---|---|
| Tag pushed before CI green → Docker publish skipped → `:v3.9.0` 404 in GHCR (2026-06-12) | §3, §6 |
| Told operator upgrade commands before verifying image existed (2026-06-12) | §6, §7 |
| `v3.8.4` tag name burned by failed `gh release create` (immutable-releases reserved name) (2026-06-12) | §4 |
| Slideover test flaked all day, treated as noise, blocked release (2026-06-11/12) | §1.5 |
| Test fix pushed direct-to-master, sat red on master post-release (2026-06-12) | §1.4 |
| Acks listed from memory missed an external contributor (4 PRs) (2026-06-12) | §2 |
| Earlier tags created by `mc-bot`/`openclaw-bot`; current Kpa-clawbot token couldn't tag without immutability disabled (2026-06-12) | §3.2 |

## Inputs

- **Target version** (required): e.g. `v3.9.1`. Decide bump semver from scope; don't ship a major when content is minor.
- **Target SHA** (optional): defaults to current `origin/master` HEAD AS LONG AS §1 passes.
- **Branch** (optional): always `master` unless explicitly told otherwise.

## §1 — Pre-flight (no tag yet)

### 1.1 Resolve target SHA + verify it's not [skip ci]
```bash
cd <workspace>/<repo>
git fetch origin --tags
# Skip [skip ci] commits per AGENTS.md rule 33
TAG_SHA=$(git log --oneline origin/master | grep -v '\[skip ci\]' | head -1 | awk '{print $1}')
git log -1 "$TAG_SHA" --format="%h %s"
```
If the resulting commit subject starts with `[skip ci]` (defensive): STOP, walk back further.

### 1.2 Verify master CI green ON THAT SHA
```bash
gh run list -R Kpa-clawbot/CoreScope --branch master --commit "$TAG_SHA" \
  --json conclusion,databaseId,headSha,displayTitle \
  -q '.[] | "\(.databaseId) \(.conclusion) \(.headSha[0:8]) - \(.displayTitle[0:60])"'
```
ALL CI/CD Pipeline jobs that aren't `skipping` must be `success`. If the most-recent run on TAG_SHA is `failure` or `in_progress`: **STOP**. Either wait, or pick an earlier green SHA. **Never tag a red commit hoping CI will pass on re-run.**

### 1.3 Verify GHCR `:edge` matches TAG_SHA (proves Docker step actually ran on this SHA)
```bash
TOKEN=$(curl -s "https://ghcr.io/token?scope=repository:kpa-clawbot/corescope:pull" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
curl -sL -H "Authorization: Bearer $TOKEN" \
  -H "Accept: application/vnd.oci.image.index.v1+json" \
  "https://ghcr.io/v2/kpa-clawbot/corescope/manifests/edge" > /tmp/m-edge.json
# Get amd64 manifest digest
AMD64_DIGEST=$(jq -r '.manifests[] | select(.platform.architecture=="amd64") | .digest' /tmp/m-edge.json)
# Pull config blob
curl -sL -H "Authorization: Bearer $TOKEN" -H "Accept: application/vnd.oci.image.manifest.v1+json" \
  "https://ghcr.io/v2/kpa-clawbot/corescope/manifests/$AMD64_DIGEST" > /tmp/m-amd64.json
CFG_DIGEST=$(jq -r '.config.digest' /tmp/m-amd64.json)
curl -sL -H "Authorization: Bearer $TOKEN" \
  "https://ghcr.io/v2/kpa-clawbot/corescope/blobs/$CFG_DIGEST" > /tmp/cfg-edge.json
IMG_SHA=$(jq -r '.config.Labels["org.opencontainers.image.revision"]' /tmp/cfg-edge.json)
echo "edge image SHA: $IMG_SHA"
echo "target SHA:     $(git rev-parse $TAG_SHA)"
```
The two SHAs MUST match. If not: a later commit pushed master and CI is still building, OR an earlier commit failed Docker publish. Wait or investigate.

### 1.4 No direct-to-master pushes for test/CI changes during a release window
If you've pushed test infrastructure changes direct to master within the last 4 hours, they should have gone through a PR. Roll forward via PR before tagging.

### 1.5 No known-flaky tests left untreated
```bash
# Quick scan: any test that's flaked 3+ times in last 24h master runs?
gh run list -R Kpa-clawbot/CoreScope --branch master --limit 20 \
  --json conclusion,databaseId,createdAt,headSha -q '.[] | select(.conclusion=="failure") | .databaseId' | head -20
```
For each: skim the failed test name. If the same test appears 3+ times across recent failures, that's the flake of the day — file an issue + fix BEFORE the release. Don't ship with "it's flaky, just re-run."

## §2 — Finalize release notes

### 2.1 Generate the contributor list from data, not memory
```bash
PREV_TAG=$(git tag -l "v*" | sort -V | tail -1)
echo "Previous tag: $PREV_TAG"
# All merged PRs in window, grouped by author — copy directly
gh pr list -R Kpa-clawbot/CoreScope --search "merged:>$(git log -1 $PREV_TAG --format=%aI)" --state merged \
  --json number,title,author,mergedAt --limit 300 \
  -q '[.[] | {n:.number, a:.author.login, t:(.title[0:60])}] | group_by(.a) | map({author:.[0].a, count:length, prs:[.[].n]})'
```
Every author OTHER than the bot account (`Kpa-clawbot`) is an external contributor. ALL of them get an Acknowledgements bullet with their PR list. No exceptions, no "I'll remember the others."

### 2.2 Highlights = operator-felt impact, not changelog ToC
Bad highlight: "M2: emoji → Phosphor Icons in page headers and table chrome (#1650)"
Good highlight: "Hide your own node from a public dashboard with a prefix rename (#1655)"

If a highlight doesn't answer "what does the operator FEEL on day one?", demote to "What's New" or "Behind the scenes."

Cap: 5 highlights. More = no highlights.

### 2.3 Write/update `docs/release-notes/vX.Y.Z.md`
Match the prior release voice. Every bullet ends with `(#PR, 8-char-sha)`. Upgrade urgency = `Low|Medium|High` with a one-line rationale.

### 2.4 Update `CHANGELOG.md` `[Unreleased]` → `## [X.Y.Z]` block.

### 2.5 PII preflight on the notes file BEFORE any gh write
```bash
grep -nEi 'YOUR_NAME|YOUR_HANDLE|YOUR_PHONE|RFC1918_PRIVATE_IPS|PROD_VM_IPS|/your/home/|api[_-]?key|YOUR_API_KEY_PATTERN' docs/release-notes/vX.Y.Z.md \
  && echo "PII HIT — abort" || echo "PII clean"
```

### 2.6 Commit + push directly to master (admin bypass)
Master is branch-protected `non_admins`, the bot is admin → direct push fine for docs.
```bash
git add docs/release-notes/vX.Y.Z.md CHANGELOG.md
git commit -m "docs(vX.Y.Z): release notes"
git push origin master
```
Yes, this triggers CI. That's the point — §1 will verify it green on the NEW HEAD before tagging.

## §3 — Tag

### 3.1 Wait for master CI green on the notes commit (re-run §1.2 + §1.3)
The notes commit becomes the new TAG_SHA candidate. Verify CI passed on it AND `:edge` was rebuilt from it.

### 3.2 Verify tag bypass actor list — current bot token must be in it
```bash
# Confirm current auth user
gh api /user --jq '.login'  # should be Kpa-clawbot

# If user changed (e.g. mc-bot / openclaw-bot tokens were retired),
# check whether the current account can create tag refs:
gh api -X POST repos/Kpa-clawbot/CoreScope/git/refs \
  -f ref="refs/tags/precheck-$(date +%s)" -f sha="$(git rev-parse $TAG_SHA)" 2>&1 | head -c 300
# If success: delete the precheck ref, proceed.
# If GH013: report to user, ruleset/immutability needs adjustment before tagging.
```

### 3.3 Verify the tag NAME isn't immutable-reserved
A `gh release create vX.Y.Z` that ever ran (even and especially if it failed) reserves the name forever under GitHub's immutable-releases feature. Check:
```bash
gh api "repos/Kpa-clawbot/CoreScope/releases/tags/vX.Y.Z" 2>&1 | head -c 200
# 404 = name free. Anything else = name in use or reserved.
```
If reserved: bump to next patch/minor. **Never** try to "free" it via support tickets — by the time it's resolved you've lost the day.

### 3.4 Create annotated tag locally, push
```bash
git tag -a vX.Y.Z "$TAG_SHA" -m "vX.Y.Z"
git push origin vX.Y.Z
```

## §4 — Wait for tag-CI

Pushing the tag triggers a `push:tag` event → CI/CD Pipeline reruns → Docker publishes `:vX.Y.Z` only on a fully-green pipeline.

```bash
sleep 30  # let it queue
TAG_RUN=$(gh run list -R Kpa-clawbot/CoreScope --event push --branch vX.Y.Z --limit 1 --json databaseId -q '.[0].databaseId')
echo "Tag run: $TAG_RUN — polling..."
# Poll until terminal. Cap 35min. NEVER --admin, NEVER bypass.
```

If Playwright fails on a known-flaky test that bot can rule out as not the release commit's fault: ONE `gh run rerun $TAG_RUN --failed` is OK. If it fails a second time on the same flake: STOP. Fix the flake first (file issue + PR + merge), then re-cut as vX.Y.Z+1.

## §5 — Create GH release

```bash
git show origin/master:docs/release-notes/vX.Y.Z.md > /tmp/relbody.md
sed -i '1,2d' /tmp/relbody.md  # strip the `# CoreScope vX.Y.Z` title — GH adds its own
# PII grep again
grep -nEi '...' /tmp/relbody.md && echo "PII HIT" || echo "PII clean"
gh release create vX.Y.Z -R Kpa-clawbot/CoreScope \
  --title "CoreScope vX.Y.Z" --notes-file /tmp/relbody.md --verify-tag
```

## §6 — VERIFY THE CONTAINER EXISTS BEFORE TELLING THE OPERATOR ANYTHING

Same probe as §1.3, but checking `:vX.Y.Z` instead of `:edge`:

```bash
TOKEN=$(curl -s "https://ghcr.io/token?scope=repository:kpa-clawbot/corescope:pull" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
HTTP=$(curl -s -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Accept: application/vnd.oci.image.index.v1+json" \
  "https://ghcr.io/v2/kpa-clawbot/corescope/manifests/vX.Y.Z")
echo ":vX.Y.Z manifest HTTP: $HTTP"
```

`200` = ship. Anything else: **STOP, don't message the operator with upgrade commands yet.**

Also verify the SHA baked into `:vX.Y.Z` matches `TAG_SHA`:
```bash
# (full probe from §1.3 but on :vX.Y.Z)
# IMG_SHA must equal $TAG_SHA
```

## §7 — Hand off to the operator (only after §6 passes)

Post upgrade commands ONLY now. For MikroTik RouterOS:

```
# Always one-line (RouterOS doesn't accept `\` continuations)
/container add remote-image=ghcr.io/kpa-clawbot/corescope:vX.Y.Z envlist=cs-env mounts=cs-data,cs-caddyfile interface=veth-corescope start-on-boot=yes name=corescope-prod-vXYZ
# Wait for status=stopped (pull complete)
/container print where name=corescope-prod-vXYZ
# Swap
/container stop corescope-prod
/container start corescope-prod-vXYZ
# Verify
curl -sf https://<prod-host>/api/stats | jq '.version,.commit'
```

For docker-compose:
```
ghcr.io/kpa-clawbot/corescope:vX.Y.Z  # or pin the digest
```

## Drafted-but-not-shipped state

If you have notes drafted but §3.2/§3.3/§4 blocks:
- Notes file lives at `docs/release-notes/vX.Y.Z.md` — leave it
- DO NOT auto-bump to vX.Y.Z+1 without telling the operator first; they may want to debug the block

## What this skill REPLACES

- Any informal "let me just tag and we'll see" pattern
- Recommending upgrade commands without a registry HEAD-check first
- Generating ack lists from memory
- Treating known-flaky tests as ignorable noise during a release window

## What this skill does NOT cover

- Hotfix releases that need to skip `[Unreleased]` accumulation (write a separate `corescope-hotfix` skill if it comes up)
- Cherry-picking onto a release branch (CoreScope doesn't maintain LTS branches)
- Customer-comms beyond GH release + Discord blurb (no Twitter/blog)
