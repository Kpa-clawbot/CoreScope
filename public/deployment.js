/* Deployment descriptor. The embedded (classic) UI ships this file as is.
 * scripts/static/build.sh replaces it when it packages public/ for a static
 * host, with the API base, channel and commit of that build.
 *
 *   channel     classic | beta | next | dev | static
 *   apiBase     '' = same origin (classic, or a host that forwards /api and
 *               /ws to the server); an https origin on a static host
 *   auth        false = anonymous build: no cookies, no login or admin UI
 *   classicUrl  the embedded UI to link to for what the build cannot do */
window.CS_DEPLOYMENT = { channel: 'classic', commit: null, version: null, apiBase: '', auth: true, classicUrl: null };
