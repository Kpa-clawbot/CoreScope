/* filter-ux.js — Wireshark-style filter UX (issue #966)
 *
 * Owns:
 *   - Help popover (filter syntax, fields, operators, examples)
 *   - Autocomplete dropdown (field names, operators, type/route values, payload.*)
 *   - Right-click context menu on packet table cells → "Filter by this value"
 *   - Saved-filter dropdown (localStorage, with starter defaults)
 *
 * Pure-logic helpers (SavedFilters, buildCellFilterClause, appendClauseToExpr)
 * are unit-tested in test-packet-filter-ux.js. DOM glue is exercised by
 * test-filter-ux-e2e.js (Playwright).
 */
(function() {
  'use strict';

  var LS_KEY = 'corescope_saved_filters_v1';

  // ── Saved filters store ────────────────────────────────────────────────
  var DEFAULT_FILTERS = [
    { name: 'Adverts only',                expr: 'type == ADVERT',        builtin: true },
    { name: 'Channel traffic',             expr: 'type == GRP_TXT',       builtin: true },
    { name: 'Direct messages',             expr: 'type == TXT_MSG',       builtin: true },
    { name: 'Strong signal (SNR > 5)',     expr: 'snr > 5',               builtin: true },
    { name: 'Multi-hop (hops > 1)',        expr: 'hops > 1',              builtin: true },
    { name: 'Repeater adverts',            expr: 'type == ADVERT && payload.flags.repeater == true', builtin: true },
    { name: 'Recent (last 5 min)',         expr: 'age < 5m',              builtin: true },
  ];

  function _getStore() {
    try {
      var raw = window.localStorage.getItem(LS_KEY);
      if (!raw) return [];
      var parsed = JSON.parse(raw);
      return Array.isArray(parsed) ? parsed : [];
    } catch (e) { return []; }
  }
  function _setStore(arr) {
    try { window.localStorage.setItem(LS_KEY, JSON.stringify(arr)); } catch (e) {}
  }

  var SavedFilters = {
    defaults: function() { return DEFAULT_FILTERS.slice(); },
    list: function() {
      // Defaults first, then user filters (deduped by name — user wins on collision)
      var user = _getStore();
      var userNames = {};
      for (var i = 0; i < user.length; i++) userNames[user[i].name] = true;
      var defaults = DEFAULT_FILTERS.filter(function(d) { return !userNames[d.name]; });
      return defaults.concat(user);
    },
    save: function(name, expr) {
      if (!name || !expr) return;
      var user = _getStore();
      var idx = -1;
      for (var i = 0; i < user.length; i++) { if (user[i].name === name) { idx = i; break; } }
      var entry = { name: name, expr: expr, ts: Date.now() };
      if (idx >= 0) user[idx] = entry; else user.push(entry);
      _setStore(user);
    },
    delete: function(name) {
      var user = _getStore();
      _setStore(user.filter(function(f) { return f.name !== name; }));
    },
  };

  // ── Right-click filter clause builders ─────────────────────────────────
  // Numeric strings stay unquoted; identifiers from TYPE_VALUES/ROUTE_VALUES
  // stay unquoted; everything else gets double-quoted.
  function _isNumericString(s) {
    if (typeof s !== 'string') return false;
    return /^-?\d+(\.\d+)?$/.test(s.trim());
  }
  function _isBareIdentifier(s) {
    return typeof s === 'string' && /^[A-Z_][A-Z0-9_]*$/.test(s);
  }
  function buildCellFilterClause(field, value, op) {
    op = op || '==';
    if (value == null) value = '';
    var v = String(value);
    var rendered;
    if (op === 'contains' || op === 'starts_with' || op === 'ends_with') {
      // String-only ops: always quote
      rendered = '"' + v.replace(/"/g, '\\"') + '"';
    } else if (_isNumericString(v)) {
      rendered = v;
    } else if (_isBareIdentifier(v)) {
      rendered = v;
    } else {
      rendered = '"' + v.replace(/"/g, '\\"') + '"';
    }
    return field + ' ' + op + ' ' + rendered;
  }
  function appendClauseToExpr(expr, clause) {
    if (!expr || !expr.trim()) return clause;
    return expr.trim() + ' && ' + clause;
  }

  // ── DOM glue (only runs in browser, after init()) ──────────────────────
  var _ctxMenu = null;

  function _h(tag, attrs, html) {
    var el = document.createElement(tag);
    if (attrs) for (var k in attrs) {
      if (k === 'class') el.className = attrs[k];
      else if (k === 'style') el.setAttribute('style', attrs[k]);
      else if (k.indexOf('data-') === 0) el.setAttribute(k, attrs[k]);
      else el[k] = attrs[k];
    }
    if (html != null) el.innerHTML = html;
    return el;
  }
  function _esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function(c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function _buildHelpHtml() {
    var PF = window.PacketFilter;
    var rows = (PF.FIELDS || []).map(function(f) {
      return '<tr><td class="fux-mono">' + _esc(f.name) + '</td><td>' + _esc(f.desc) + '</td></tr>';
    }).join('');
    var ops = (PF.OPERATORS || []).map(function(o) {
      return '<tr><td class="fux-mono">' + _esc(o.op) + '</td><td>' + _esc(o.desc) +
             '</td><td class="fux-mono">' + _esc(o.example) + '</td></tr>';
    }).join('');
    var examples = [
      'type == ADVERT',
      'type == GRP_TXT && size > 50',
      'payload.name contains "Gilroy"',
      'payload.flags.repeater == true',
      'snr > 5 && rssi > -90',
      'hops < 2',
      'observer == "Dorrington" && type == ADVERT',
      '(type == ADVERT || type == ACK) && snr > 0',
      'age < 1h',
      'time after "2025-01-01"',
    ].map(function(e) { return '<li class="fux-mono">' + _esc(e) + '</li>'; }).join('');
    return [
      '<h3>Filter syntax</h3>',
      '<p>Wireshark-style boolean expressions over packet fields. Combine with <code>&amp;&amp;</code>, <code>||</code>, <code>!</code>, and parentheses. Strings are case-insensitive. Tip: append <code>?filter=…</code> to the URL to share a filter.</p>',
      '<h4>Fields</h4>',
      '<table class="fux-table"><thead><tr><th>Name</th><th>Description</th></tr></thead><tbody>' + rows + '</tbody></table>',
      '<h4>Operators</h4>',
      '<table class="fux-table"><thead><tr><th>Op</th><th>Meaning</th><th>Example</th></tr></thead><tbody>' + ops + '</tbody></table>',
      '<h4>Examples</h4>',
      '<ul class="fux-examples">' + examples + '</ul>',
      '<h4>Tips</h4>',
      '<ul>',
      '<li>Right-click any cell in the packet table to add a clause for that value.</li>',
      '<li>Type a partial field name to autocomplete; Tab/Enter accepts, Esc dismisses.</li>',
      '<li>Save commonly-used expressions via the ★ Save button — they appear in the Saved dropdown.</li>',
      '</ul>',
    ].join('');
  }

  function _showHelp() {
    var existing = document.getElementById('filterHelpPopover');
    if (existing) { existing.remove(); return; }
    var pop = _h('div', { id: 'filterHelpPopover', class: 'fux-popover', role: 'dialog', 'aria-label': 'Filter syntax help' });
    pop.innerHTML =
      '<div class="fux-popover-header"><strong>Filter syntax</strong>' +
      '<button type="button" class="fux-popover-close" aria-label="Close">✕</button></div>' +
      '<div class="fux-popover-body">' + _buildHelpHtml() + '</div>';
    document.body.appendChild(pop);
    pop.querySelector('.fux-popover-close').addEventListener('click', function() { pop.remove(); });
    document.addEventListener('keydown', function _esc(ev) {
      if (ev.key === 'Escape') { pop.remove(); document.removeEventListener('keydown', _esc); }
    });
  }

  // ── Autocomplete ───────────────────────────────────────────────────────
  function _wireAutocomplete(input) {
    var dd = _h('div', { id: 'filterAcDropdown', class: 'fux-ac-dropdown', role: 'listbox' });
    dd.style.display = 'none';
    input.parentNode.appendChild(dd);
    var sel = -1, items = [];

    function _gatherPayloadKeys() {
      // Best-effort: scan the first ~50 visible packets for decoded_json keys
      var keys = {};
      try {
        var rows = document.querySelectorAll('#pktTable tbody tr');
        for (var r = 0; r < rows.length && r < 50; r++) {
          var dj = rows[r].getAttribute('data-decoded');
          if (!dj) continue;
          var obj = JSON.parse(dj);
          for (var k in obj) keys[k] = true;
        }
      } catch (e) {}
      return Object.keys(keys);
    }

    function close() { dd.style.display = 'none'; sel = -1; items = []; input.removeAttribute('aria-activedescendant'); }
    function render() {
      if (!items.length) { close(); return; }
      dd.innerHTML = items.map(function(it, i) {
        return '<div class="fux-ac-item' + (i === sel ? ' active' : '') + '" id="fux-ac-' + i +
          '" role="option" data-idx="' + i + '">' +
          '<span class="fux-ac-val">' + _esc(it.value) + '</span>' +
          (it.desc ? '<span class="fux-ac-desc">' + _esc(it.desc) + '</span>' : '') +
          '</div>';
      }).join('');
      dd.style.display = 'block';
      if (sel >= 0) input.setAttribute('aria-activedescendant', 'fux-ac-' + sel);
    }
    function accept(idx) {
      if (!items[idx]) return;
      var rs = items._replaceStart, re = items._replaceEnd;
      var val = items[idx].value;
      var v = input.value;
      var newVal = v.slice(0, rs) + val + v.slice(re);
      var caret = rs + val.length;
      // Append space + helpful next char for fields (so user can type op)
      if (items[idx].kind === 'field') { newVal = newVal.slice(0, caret) + ' ' + newVal.slice(caret); caret++; }
      input.value = newVal;
      input.setSelectionRange(caret, caret);
      close();
      // Trigger filter recompile
      input.dispatchEvent(new Event('input', { bubbles: true }));
    }

    function refresh() {
      var PF = window.PacketFilter;
      if (!PF || !PF.suggest) return close();
      var r = PF.suggest(input.value, input.selectionStart || 0, { payloadKeys: _gatherPayloadKeys() });
      items = (r && r.suggestions) ? r.suggestions.slice(0, 12) : [];
      items._replaceStart = r ? r.replaceStart : 0;
      items._replaceEnd = r ? r.replaceEnd : 0;
      sel = items.length ? 0 : -1;
      render();
    }
    input.addEventListener('input', refresh);
    input.addEventListener('focus', refresh);
    input.addEventListener('blur', function() { setTimeout(close, 150); });
    input.addEventListener('keydown', function(ev) {
      if (dd.style.display === 'none') return;
      if (ev.key === 'ArrowDown') { sel = (sel + 1) % items.length; render(); ev.preventDefault(); }
      else if (ev.key === 'ArrowUp') { sel = (sel - 1 + items.length) % items.length; render(); ev.preventDefault(); }
      else if (ev.key === 'Tab' || ev.key === 'Enter') {
        if (sel >= 0) { accept(sel); ev.preventDefault(); }
      } else if (ev.key === 'Escape') { close(); ev.preventDefault(); }
    });
    dd.addEventListener('mousedown', function(ev) {
      var target = ev.target.closest('.fux-ac-item');
      if (!target) return;
      ev.preventDefault();
      accept(parseInt(target.getAttribute('data-idx'), 10));
    });
  }

  // ── Right-click context menu ───────────────────────────────────────────
  function _showContextMenu(x, y, field, value) {
    if (_ctxMenu) { _ctxMenu.remove(); _ctxMenu = null; }
    var input = document.getElementById('packetFilterInput');
    if (!input) return;
    var menu = _h('div', { id: 'filterContextMenu', class: 'fux-ctx-menu', role: 'menu' });
    var ops = [
      { label: 'Filter ' + field + ' == "' + value + '"',  op: '==' },
      { label: 'Filter ' + field + ' != "' + value + '"',  op: '!=' },
      { label: 'Filter ' + field + ' contains "' + value + '"', op: 'contains' },
    ];
    menu.innerHTML = ops.map(function(o, i) {
      return '<button type="button" class="fux-ctx-item" data-idx="' + i + '" role="menuitem">' + _esc(o.label) + '</button>';
    }).join('');
    menu.style.left = x + 'px';
    menu.style.top = y + 'px';
    document.body.appendChild(menu);
    _ctxMenu = menu;
    menu.addEventListener('click', function(ev) {
      var btn = ev.target.closest('.fux-ctx-item');
      if (!btn) return;
      var op = ops[parseInt(btn.getAttribute('data-idx'), 10)].op;
      var clause = buildCellFilterClause(field, value, op);
      input.value = appendClauseToExpr(input.value, clause);
      input.dispatchEvent(new Event('input', { bubbles: true }));
      menu.remove(); _ctxMenu = null;
    });
    function dismiss(ev) {
      if (_ctxMenu && !_ctxMenu.contains(ev.target)) { _ctxMenu.remove(); _ctxMenu = null;
        document.removeEventListener('mousedown', dismiss);
        document.removeEventListener('keydown', escDismiss);
      }
    }
    function escDismiss(ev) { if (ev.key === 'Escape') dismiss({ target: document.body }); }
    setTimeout(function() {
      document.addEventListener('mousedown', dismiss);
      document.addEventListener('keydown', escDismiss);
    }, 0);
  }

  function _wireContextMenu() {
    // Delegated listener on the table — extracts field+value from data-* attrs.
    var tbl = document.getElementById('pktTable');
    if (!tbl) return;
    tbl.addEventListener('contextmenu', function(ev) {
      var cell = ev.target.closest('td[data-filter-field]');
      if (!cell) return;
      var field = cell.getAttribute('data-filter-field');
      var value = cell.getAttribute('data-filter-value');
      if (!field || value == null || value === '') return;
      ev.preventDefault();
      _showContextMenu(ev.pageX, ev.pageY, field, value);
    });
  }

  // ── Saved filters dropdown ─────────────────────────────────────────────
  function _renderSavedDropdown(container, input) {
    var btn = _h('button', { type: 'button', class: 'fux-saved-trigger', id: 'filterSavedTrigger', title: 'Saved filters' }, '★ Saved ▾');
    var menu = _h('div', { class: 'fux-saved-menu hidden', id: 'filterSavedMenu', role: 'menu' });
    container.appendChild(btn);
    container.appendChild(menu);

    function build() {
      var list = SavedFilters.list();
      var rows = list.map(function(f, i) {
        var del = f.builtin ? '' :
          '<button type="button" class="fux-saved-del" data-name="' + _esc(f.name) + '" title="Delete">✕</button>';
        return '<div class="fux-saved-item" data-idx="' + i + '">' +
          '<span class="fux-saved-name">' + _esc(f.name) + '</span>' +
          '<span class="fux-saved-expr fux-mono">' + _esc(f.expr) + '</span>' +
          del + '</div>';
      }).join('');
      menu.innerHTML =
        '<div class="fux-saved-header">Saved filters</div>' +
        rows +
        '<div class="fux-saved-footer">' +
        '<button type="button" id="filterSaveCurrent" class="fux-saved-save">＋ Save current expression</button>' +
        '</div>';
    }

    btn.addEventListener('click', function(ev) {
      ev.stopPropagation();
      build();
      menu.classList.toggle('hidden');
    });
    document.addEventListener('click', function(ev) {
      if (!menu.contains(ev.target) && ev.target !== btn) menu.classList.add('hidden');
    });
    menu.addEventListener('click', function(ev) {
      var del = ev.target.closest('.fux-saved-del');
      if (del) {
        SavedFilters.delete(del.getAttribute('data-name'));
        build();
        ev.stopPropagation();
        return;
      }
      if (ev.target.id === 'filterSaveCurrent') {
        var expr = (input.value || '').trim();
        if (!expr) { alert('Type a filter expression first.'); return; }
        var name = prompt('Name this filter:', '');
        if (name && name.trim()) {
          SavedFilters.save(name.trim(), expr);
          build();
        }
        return;
      }
      var item = ev.target.closest('.fux-saved-item');
      if (item) {
        var list = SavedFilters.list();
        var f = list[parseInt(item.getAttribute('data-idx'), 10)];
        if (f) {
          input.value = f.expr;
          input.dispatchEvent(new Event('input', { bubbles: true }));
          menu.classList.add('hidden');
        }
      }
    });
  }

  // ── Init: idempotent, called by packets.js after filter input renders ──
  function init() {
    var input = document.getElementById('packetFilterInput');
    if (!input || input.dataset.fuxInit === '1') return;
    input.dataset.fuxInit = '1';

    // Help icon + saved-filters dropdown — injected next to the input
    var wrap = input.parentNode;
    if (wrap) {
      var bar = document.getElementById('filterUxBar');
      if (!bar) {
        bar = _h('div', { id: 'filterUxBar', class: 'fux-bar' });
        var helpBtn = _h('button', { type: 'button', class: 'fux-help-btn', id: 'filterHelpBtn',
          'aria-label': 'Filter syntax help', title: 'Filter syntax help' }, 'ⓘ Help');
        helpBtn.addEventListener('click', _showHelp);
        bar.appendChild(helpBtn);
        _renderSavedDropdown(bar, input);
        wrap.appendChild(bar);
      }
    }

    _wireAutocomplete(input);
    _wireContextMenu();
  }

  var _exports = {
    SavedFilters: SavedFilters,
    buildCellFilterClause: buildCellFilterClause,
    appendClauseToExpr: appendClauseToExpr,
    init: init,
    _showHelp: _showHelp, // exposed for E2E
  };
  if (typeof window !== 'undefined') window.FilterUX = _exports;
  if (typeof module !== 'undefined' && module.exports) module.exports = _exports;
})();
