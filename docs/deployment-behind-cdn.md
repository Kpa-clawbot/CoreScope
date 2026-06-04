# Deployment behind a CDN

This page is referenced from the server log warning and from the
`scripts/check-cdn-bypass.sh` helper output. The canonical content
lives in [`docs/deployment.md` → "Behind a CDN (Cloudflare, Fastly)"](./deployment.md#behind-a-cdn-cloudflare-fastly).

**TL;DR for operators behind Cloudflare/Fastly/etc.:**

1. Verify from outside the CDN:
   ```sh
   curl -sI 'https://<your-domain>/api/observers' | grep -iE 'cf-cache|age|cache-control'
   ```
   Look for `cf-cache-status: BYPASS` and `age: 0`.
2. If you see `cf-cache-status: HIT` or `age > 0`, add a Cloudflare
   **Cache Rule** (Caching → Cache Rules → Create rule):
   - When: URI Path starts with `/api/`
   - Then: Cache eligibility → **Bypass cache**
3. Re-verify with the curl in step 1.
4. Run `scripts/check-cdn-bypass.sh https://<your-domain>` — should exit 0.

See [`docs/deployment.md`](./deployment.md#behind-a-cdn-cloudflare-fastly)
for the full discussion (Fastly equivalent, why the origin header
alone isn't sufficient, what the startup warning means).

Issue: [#1561](https://github.com/Kpa-clawbot/CoreScope/issues/1561).
Related: [#1551](https://github.com/Kpa-clawbot/CoreScope/issues/1551).
