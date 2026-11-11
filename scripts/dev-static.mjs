#!/usr/bin/env node
// Zero-dependency static file server for running the UI from one origin
// against a CoreScope server on another, the way a static host does. There is
// no proxy on purpose: the page on this port really calls the API cross-origin.
//
//   node scripts/dev-static.mjs --root dist/web --port 8788
//
// Development and CI only. It serves files and nothing else.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.ico': 'image/x-icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf': 'font/ttf',
  '.txt': 'text/plain; charset=utf-8',
  '.webmanifest': 'application/manifest+json',
  '.wav': 'audio/wav',
  '.mp3': 'audio/mpeg',
};

export function parseArgs(argv) {
  const opts = { root: 'dist/web', port: 8788, host: '127.0.0.1' };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--root') opts.root = argv[++i];
    else if (a === '--port') opts.port = Number(argv[++i]);
    else if (a === '--host') opts.host = argv[++i];
    else throw new Error('unknown argument: ' + a);
  }
  if (!Number.isInteger(opts.port) || opts.port < 0 || opts.port > 65535) throw new Error('bad --port');
  return opts;
}

// resolveFile maps a URL path to a file under root, or null. It never leaves
// root: the decoded path is normalised and must stay inside it.
export function resolveFile(root, urlPath) {
  let p;
  try { p = decodeURIComponent(urlPath.split('?')[0].split('#')[0]); } catch { return null; }
  if (p.includes('\0')) return null;
  const abs = path.resolve(root, '.' + path.posix.normalize('/' + p));
  const rootAbs = path.resolve(root);
  if (abs !== rootAbs && !abs.startsWith(rootAbs + path.sep)) return null;
  let file = abs;
  try {
    if (fs.statSync(file).isDirectory()) file = path.join(file, 'index.html');
    if (!fs.statSync(file).isFile()) return null;
  } catch { return null; }
  return file;
}

export function createServer(root) {
  return http.createServer((req, res) => {
    if (req.method !== 'GET' && req.method !== 'HEAD') {
      res.writeHead(405, { Allow: 'GET, HEAD', 'Content-Type': 'text/plain; charset=utf-8' });
      res.end('method not allowed\n');
      return;
    }
    const file = resolveFile(root, req.url || '/');
    if (!file) {
      // An HTML page, like a static host's 404: api() must report it as not
      // JSON instead of failing to parse it.
      res.writeHead(404, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end('<!doctype html><title>404</title><p>Not found</p>\n');
      return;
    }
    const type = MIME[path.extname(file).toLowerCase()] || 'application/octet-stream';
    res.writeHead(200, { 'Content-Type': type, 'Cache-Control': 'no-cache' });
    if (req.method === 'HEAD') { res.end(); return; }
    fs.createReadStream(file).pipe(res);
  });
}

if (import.meta.url === `file://${process.argv[1]}` || process.argv[1]?.endsWith('dev-static.mjs')) {
  const opts = parseArgs(process.argv.slice(2));
  if (!fs.existsSync(opts.root)) {
    console.error(`dev-static: ${opts.root} not found (run scripts/static/build.sh first)`);
    process.exit(1);
  }
  createServer(opts.root).listen(opts.port, opts.host, () => {
    console.log(`dev-static: serving ${opts.root} on http://localhost:${opts.port}`);
  });
}
