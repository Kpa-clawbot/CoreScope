// Minify the JS and CSS in public/ into public-dist/ for production serving.
// Run via `npm run build:frontend` or `node scripts/build-frontend.mjs`.
//
// Why a separate output directory: the dev workflow serves public/ as-is so
// every change is visible without a build step. Production COPYs public-dist/
// into the container as /app/public so the Go static handler picks it up.
//
// Files under public/vendor/ are passed through unchanged — they're already
// minified or vendored verbatim, and re-minifying them sometimes breaks
// integrity-checked third-party code.
import { build } from 'esbuild';
import { mkdir, readdir, copyFile, stat, rm } from 'node:fs/promises';
import { dirname, join, relative, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = join(__dirname, '..');
const SRC = join(ROOT, 'public');
const OUT = join(ROOT, 'public-dist');

async function walk(dir) {
  const out = [];
  const entries = await readdir(dir, { withFileTypes: true });
  for (const e of entries) {
    const full = join(dir, e.name);
    if (e.isDirectory()) out.push(...(await walk(full)));
    else out.push(full);
  }
  return out;
}

async function copyAsIs(srcPath, relPath) {
  const dstPath = join(OUT, relPath);
  await mkdir(dirname(dstPath), { recursive: true });
  await copyFile(srcPath, dstPath);
}

async function main() {
  console.log(`[build-frontend] cleaning ${OUT}`);
  await rm(OUT, { recursive: true, force: true });
  await mkdir(OUT, { recursive: true });

  const all = await walk(SRC);
  const jsToMinify = [];
  const cssToMinify = [];
  let copied = 0;

  for (const f of all) {
    const rel = relative(SRC, f);
    const ext = extname(f).toLowerCase();
    // Pass vendor/ through untouched; those files are third-party.
    if (rel.split('/')[0] === 'vendor') {
      await copyAsIs(f, rel);
      copied++;
      continue;
    }
    if (ext === '.js') jsToMinify.push({ src: f, rel });
    else if (ext === '.css') cssToMinify.push({ src: f, rel });
    else {
      await copyAsIs(f, rel);
      copied++;
    }
  }

  console.log(`[build-frontend] minifying ${jsToMinify.length} .js + ${cssToMinify.length} .css files`);

  // esbuild handles all entries in parallel and is very fast (~100ms total
  // for the CoreScope public/ tree on a laptop).
  await Promise.all(
    jsToMinify.map(({ src, rel }) =>
      build({
        entryPoints: [src],
        outfile: join(OUT, rel),
        minify: true,
        target: 'es2020',
        legalComments: 'none',
        // Do NOT bundle — each file is loaded independently from index.html
        // and shares globals (window.HashColor etc). Bundling would lose the
        // implicit script-tag ordering contract.
        bundle: false,
        logLevel: 'warning',
      })
    )
  );

  await Promise.all(
    cssToMinify.map(({ src, rel }) =>
      build({
        entryPoints: [src],
        outfile: join(OUT, rel),
        minify: true,
        loader: { '.css': 'css' },
        legalComments: 'none',
        bundle: false,
        logLevel: 'warning',
      })
    )
  );

  // Quick size summary.
  let beforeBytes = 0, afterBytes = 0;
  for (const { src, rel } of [...jsToMinify, ...cssToMinify]) {
    beforeBytes += (await stat(src)).size;
    afterBytes += (await stat(join(OUT, rel))).size;
  }
  const pct = beforeBytes ? Math.round(100 * (1 - afterBytes / beforeBytes)) : 0;
  console.log(
    `[build-frontend] done. minified ${beforeBytes} -> ${afterBytes} bytes (${pct}% smaller) + ${copied} passthrough files`
  );
}

main().catch((e) => {
  console.error('[build-frontend] failed:', e);
  process.exit(1);
});
