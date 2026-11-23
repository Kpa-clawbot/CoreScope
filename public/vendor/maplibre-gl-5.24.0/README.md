# MapLibre GL JS 5.24.0 (vendored)

Byte-identical copies of files from the npm package, loaded on demand by
`public/map-vector.js` (never at page load). Do not edit; to upgrade, vendor a
new versioned directory and update the integrity table in `map-vector.js`
(`tests/unit/test-vendored-libs.js` checks it).

- Source: https://registry.npmjs.org/maplibre-gl/-/maplibre-gl-5.24.0.tgz
- Tarball integrity (matches `npm view maplibre-gl@5.24.0 dist.integrity`):
  `sha512-ALyFxgtd5R+65UqZ/++lOqwWcC0SNho9c27fYSyLmG7AfnAul2o46F05aDJGPbFU57wos9dgcIySHs0Xe6ia3A==`
- License: BSD-3-Clause, see `LICENSE.txt` (from `dist/LICENSE.txt`)

| File (from `package/dist/`) | Bytes | sha384 |
|---|---|---|
| `maplibre-gl-csp.js` | 971665 | `sha384-+lDFpEYaS5ofD0qW/5dA+duOMWMSGs+utCY+wx9H/lS1i0WZ0bQH31giSmqYsxqR` |
| `maplibre-gl-csp-worker.js` | 457880 | `sha384-wBzwLUk46fiEnNOOlHXeylfQrL/SzyNqSD0gYdQJskF9cbBaNcRDYn6ZmSBW4l0z` |
| `maplibre-gl.css` | 70024 | `sha384-uTttxo/aOKbdE5RlD/SPzSDoDmNvGlUYPjONi2MN/b7c9HPSvW07OIuyP7uL6jxK` |
| `LICENSE.txt` | 5984 | `sha384-3Zf/f+K0L6bn+JFiUedpEU79XsEmWDOSzj/+j17JomfRWmlkO60ByjZbpgZkeRiL` |

The CSP build is used so the worker is a plain same-origin file
(`maplibregl.setWorkerUrl`) instead of a `blob:` worker. The two `.js` files end
with a `sourceMappingURL` comment; the `.map` files are not vendored, so
DevTools reports a 404 for them, which is harmless.
