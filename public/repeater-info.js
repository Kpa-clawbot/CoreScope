/* Repeater info section on the node detail views: community-maintained
 * details (antenna height and gain, status, power, notes) plus the claim
 * button. window.CSRepeaterInfo.mount(el, node) is inert unless the node is a
 * repeater and user management is enabled. Every dynamic value in HTML goes
 * through escapeHtml; status messages use textContent. */
(function () {
  'use strict';
  var STATUSES = ['active', 'maintenance', 'decommissioned'];
  var POWERS = ['mains', 'battery', 'solar'];

  function esc(s) { return escapeHtml(String(s)); }
  function has(v) { return v !== null && v !== undefined && v !== ''; }

  function row(label, valueHtml) {
    return '<div class="repeater-info-row"><dt>' + esc(label) + '</dt><dd>' + valueHtml + '</dd></div>';
  }

  function fmtDate(s) {
    var d = new Date(s);
    return isNaN(d.getTime()) ? String(s) : d.toLocaleDateString();
  }

  function viewHtml(info, user) {
    var rows = '';
    var claimers = info.claimers || [];
    if (has(info.heightM)) rows += row('Antenna height', esc(info.heightM + ' m'));
    if (info.updatedAt) rows += row('Antenna gain', esc(Number(info.gainDbi || 0) + ' dBi'));
    if (has(info.status)) rows += row('Status', esc(info.status));
    if (has(info.power)) rows += row('Power', esc(info.power));
    if (has(info.notes)) rows += row('Notes', '<span class="repeater-info-notes">' + esc(info.notes) + '</span>');
    if (claimers.length) rows += row('Maintained by', claimers.map(function (c) { return esc(c.displayName); }).join(', '));
    if (info.updatedAt) {
      rows += has(info.updatedBy)
        ? row('Last edited by', esc(info.updatedBy) + ', ' + esc(fmtDate(info.updatedAt)))
        : row('Last edited', esc(fmtDate(info.updatedAt)));
    }
    var html = rows ? '<dl class="repeater-info-list">' + rows + '</dl>' : '<p class="text-muted repeater-info-empty">No details yet.</p>';
    if (user) {
      html += '<div class="repeater-info-actions">';
      if (info.canEdit) html += '<button type="button" class="btn-primary" data-ri-edit>Edit</button> ';
      html += info.claimedByMe
        ? '<button type="button" class="btn-primary" data-ri-unclaim>Unclaim</button>'
        : '<button type="button" class="btn-primary" data-ri-claim>Claim</button>';
      html += '</div>';
    } else {
      html += '<p class="text-muted repeater-info-login">Log in to claim or edit this repeater.</p>';
    }
    return html + '<small class="repeater-info-msg" data-ri-msg role="status" aria-live="polite"></small>';
  }

  function options(values, current) {
    return '<option value="">\u2014</option>' + values.map(function (v) {
      return '<option value="' + v + '"' + (current === v ? ' selected' : '') + '>' + v + '</option>';
    }).join('');
  }

  function formHtml(info) {
    return '<form class="repeater-info-form" data-ri-form novalidate>' +
      '<label>Antenna height (m)<input type="number" name="heightM" min="0" max="300" step="0.1" value="' + (has(info.heightM) ? esc(info.heightM) : '') + '"></label>' +
      '<label>Antenna gain (dBi)<input type="number" name="gainDbi" min="-10" max="30" step="0.1" value="' + esc(has(info.gainDbi) ? info.gainDbi : 0) + '"></label>' +
      '<label>Status<select name="status">' + options(STATUSES, info.status) + '</select></label>' +
      '<label>Power<select name="power">' + options(POWERS, info.power) + '</select></label>' +
      '<label>Notes<textarea name="notes" maxlength="500" rows="3">' + esc(info.notes || '') + '</textarea></label>' +
      '<p class="repeater-info-notice">Your display name will be shown publicly as the last editor of this repeater.</p>' +
      '<div class="repeater-info-actions"><button type="submit" class="btn-primary" data-ri-save>Save</button> ' +
      '<button type="button" class="btn-primary" data-ri-cancel>Cancel</button></div>' +
      '<small class="repeater-info-msg" data-ri-msg role="status" aria-live="polite"></small></form>';
  }

  function confirmHtml() {
    return '<div class="repeater-info-confirm"><p>Your display name will be shown publicly as a maintainer of this repeater.</p>' +
      '<div class="repeater-info-actions"><button type="button" class="btn-primary" data-ri-confirm>Confirm</button> ' +
      '<button type="button" class="btn-primary" data-ri-cancel>Cancel</button></div>' +
      '<small class="repeater-info-msg" data-ri-msg role="status" aria-live="polite"></small></div>';
  }

  function readForm(root) {
    function val(n) { var c = root.querySelector('[name="' + n + '"]'); return c ? String(c.value) : ''; }
    var h = val('heightM').trim();
    var g = val('gainDbi').trim();
    return {
      heightM: h === '' ? null : Number(h),
      gainDbi: g === '' ? 0 : Number(g),
      status: val('status'),
      power: val('power'),
      notes: val('notes')
    };
  }

  // formError returns why the height or gain input cannot be sent, or ''.
  // An empty height is "unknown" and fine; a gain left empty means 0.
  function formError(root) {
    function bad(n, empty) {
      var c = root.querySelector('[name="' + n + '"]');
      if (!c) return false;
      if (c.validity && c.validity.badInput) return true;
      var v = String(c.value).trim();
      return v === '' ? !empty : !isFinite(Number(v));
    }
    if (bad('heightM', true)) return 'Antenna height must be a number.';
    if (bad('gainDbi', true)) return 'Antenna gain must be a number.';
    return '';
  }

  // drafts keeps an open edit form per section slot (element id) across a
  // re-render of the node page, which mounts a fresh element (theme-refresh
  // does that). At most one entry per slot; any navigation drops them all.
  var drafts = {};
  if (window.addEventListener) window.addEventListener('hashchange', function () { drafts = {}; });

  function rawForm(root) {
    var out = {};
    ['heightM', 'gainDbi', 'status', 'power', 'notes'].forEach(function (n) {
      var c = root.querySelector('[name="' + n + '"]');
      out[n] = c ? String(c.value) : '';
    });
    return out;
  }

  function say(el, text) {
    var m = el.querySelector('[data-ri-msg]');
    if (m) m.textContent = text;
  }

  function mount(el, node) {
    if (!el || !node || node.role !== 'repeater') return Promise.resolve();
    var auth = window.CSAuth;
    if (!auth) return Promise.resolve();
    var pk = String(node.public_key || '').toLowerCase();
    var base = '/api/repeaters/' + encodeURIComponent(pk);
    var slot = el.id || '';
    var info = null;

    function keep() {
      var form = el.querySelector('[data-ri-form]');
      if (form) drafts[slot] = { pk: pk, values: rawForm(form) };
    }
    function show(mode, values) {
      var user = auth.user();
      if (mode !== 'edit') delete drafts[slot];
      el.innerHTML = '<h4>Repeater info</h4>' +
        (mode === 'edit' ? formHtml(values || info) : mode === 'confirm' ? confirmHtml() : viewHtml(info, user));
      if (mode === 'edit' && !values) keep();
    }
    var busy = false;
    function send(btn, method, path, body, done) {
      if (busy) return Promise.resolve();
      busy = true;
      if (btn) btn.disabled = true;
      function settle() { busy = false; if (btn) btn.disabled = false; }
      return auth.request(method, path, body).then(function (r) {
        settle();
        if (!r.ok) { say(el, auth.errText(r)); return; }
        info = r.data;
        done();
      }, function () { settle(); say(el, 'Network error, try again.'); });
    }

    function bind() {
      el.addEventListener('click', function (ev) {
        var t = ev.target && ev.target.closest ? ev.target.closest('button') : null;
        if (!t || busy) return;
        var back = function () { show('view'); };
        if (t.hasAttribute('data-ri-edit')) show('edit');
        else if (t.hasAttribute('data-ri-claim')) show('confirm');
        else if (t.hasAttribute('data-ri-cancel')) show('view');
        else if (t.hasAttribute('data-ri-confirm')) send(t, 'POST', base + '/claim', undefined, back);
        else if (t.hasAttribute('data-ri-unclaim')) send(t, 'DELETE', base + '/claim', undefined, back);
        else if (t.hasAttribute('data-ri-save')) {
          ev.preventDefault();
          var form = el.querySelector('[data-ri-form]');
          var err = formError(form);
          if (err) { say(el, err); return; }
          send(t, 'PUT', base + '/info', readForm(form), back);
        }
      });
      el.addEventListener('submit', function (ev) { ev.preventDefault(); });
      el.addEventListener('input', keep);
      el.addEventListener('change', keep);
    }

    return Promise.resolve(auth.ready()).then(function () {
      if (!auth.isEnabled()) return;
      bind();
      return auth.request('GET', base + '/info').then(function (r) {
        if (!r.ok) return;
        info = r.data;
        var d = drafts[slot];
        if (d && d.pk === pk && info.canEdit) show('edit', d.values);
        else show('view');
        el.hidden = false;
        var hash = String(window.location.hash || '');
        if (hash.indexOf('section=node-repeater-info') !== -1 && el.scrollIntoView) el.scrollIntoView();
      }, function () {});
    });
  }

  window.CSRepeaterInfo = { mount: mount, _test: { viewHtml: viewHtml, formHtml: formHtml, readForm: readForm, formError: formError } };
})();
