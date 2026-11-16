/* Shared by the unit suites that run frontend files in a vm sandbox: loads
 * the real public/deployment.js and public/api-base.js into it, so code that
 * calls CSApi sees the same seam as in the browser. `deployment` overrides
 * fields of the classic descriptor (e.g. { apiBase: 'https://api.test',
 * auth: false } for the anonymous static build). Not a suite itself. */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const PUBLIC = path.join(__dirname, '..', '..', 'public');
const DEPLOYMENT_SRC = fs.readFileSync(path.join(PUBLIC, 'deployment.js'), 'utf8');
const API_BASE_SRC = fs.readFileSync(path.join(PUBLIC, 'api-base.js'), 'utf8');

// Some suites give the sandbox its own `window` object, others make the
// sandbox the window. Both files write to `window`, and the code under test
// reads CSApi bare (the sandbox global) and as window.CSApi.
const WITH_WINDOW = src => '(function (window) {\n' + src + '\n})(this.window && typeof this.window === "object" ? this.window : this);';

function loadApiBase(ctx, deployment) {
  if (!vm.isContext(ctx)) vm.createContext(ctx);
  vm.runInContext(WITH_WINDOW(DEPLOYMENT_SRC), ctx);
  const win = ctx.window && typeof ctx.window === 'object' ? ctx.window : ctx;
  if (deployment) win.CS_DEPLOYMENT = Object.assign({}, win.CS_DEPLOYMENT, deployment);
  vm.runInContext(WITH_WINDOW(API_BASE_SRC), ctx);
  ctx.CSApi = win.CSApi;
  return ctx.CSApi;
}

module.exports = { loadApiBase };
