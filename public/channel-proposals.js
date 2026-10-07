/* Channel proposals client (docs/specs/2026-10-07-channel-proposals-design.md).
 * window.CSProposals: the hashtag name rules (mirror of Go
 * internal/channel.ValidateHashtagName; the server decides), the propose
 * control of the Add Channel dialog, the approved-channel merge of the
 * channel list, and the account page's My proposals. Inert unless
 * /api/config/client advertises userManagement.channelProposals (roles.js
 * sets window.MC_USER_MGMT). Every dynamic value in HTML goes through
 * escapeHtml; messages use textContent. */
(function () {
  'use strict';
  var KIND = 'hashtag_channel';
  var MAX_BYTES = 31; // firmware ChannelDetails.name[32] minus the NUL
  // Go's unicode.IsSpace set, so trimming matches strings.TrimSpace.
  var GO_SPACE = '[\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000]';
  var TRIM_RE = new RegExp('^' + GO_SPACE + '+|' + GO_SPACE + '+$', 'g');
  var BAD_RE = null;
  try { BAD_RE = new RegExp('[\\p{Cc}\\p{Cf}\\u2028\\u2029]', 'u'); } catch (e) { BAD_RE = null; } // old engines: the server still checks
  var MSG = {
    empty: 'enter a channel name after #',
    long: 'a channel name is at most 31 bytes including the # (MeshCore stores 32 with the terminator)',
    pub: 'Public is the built-in channel and cannot be proposed',
    bad: 'the name contains invisible or control characters'
  };
  var STATUS_LABELS = { pending: 'Waiting for review', approved: 'Approved', rejected: 'Rejected', revoked: 'Revoked' };

  function trimGo(s) { return s.replace(TRIM_RE, ''); }

  function utf8Length(s) {
    var n = 0;
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i);
      if (c < 0x80) n += 1;
      else if (c < 0x800) n += 2;
      else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length && s.charCodeAt(i + 1) >= 0xdc00 && s.charCodeAt(i + 1) <= 0xdfff) { n += 4; i++; }
      else n += 3;
    }
    return n;
  }

  function validate(raw) {
    var s = trimGo(String(raw == null ? '' : raw));
    if (s.charAt(0) !== '#') s = '#' + s;
    if (trimGo(s.slice(1).replace(/‍/g, '')) === '') return { ok: false, error: MSG.empty };
    if (utf8Length(s) > MAX_BYTES) return { ok: false, error: MSG.long };
    if (s.toLowerCase() === '#public') return { ok: false, error: MSG.pub };
    if (BAD_RE && BAD_RE.test(s.replace(/‍/g, ''))) return { ok: false, error: MSG.bad };
    return { ok: true, name: s };
  }

  function enabled() { return !!(window.MC_USER_MGMT && window.MC_USER_MGMT.channelProposals); }
  function canPropose() { return enabled() && !!(window.CSAuth && window.CSAuth.user()); }

  function propose(name) {
    return window.CSAuth.request('POST', '/api/proposals', { kind: KIND, subject: name }).then(function (r) {
      return r.ok ? { ok: true, text: 'Proposed ' + r.data.subject + ', an admin will review it.' }
                  : { ok: false, text: window.CSAuth.errText(r) };
    });
  }

  function setMsg(el, text, ok) {
    el.textContent = text;
    el.className = 'ch-propose-msg' + (text ? (ok ? ' ok' : ' err') : '');
  }

  // bindPropose wires the "Propose for everyone" button of the Add Channel
  // dialog to the hashtag input. The dialog calls sync() when it opens.
  function bindPropose(doc) {
    var input = doc.getElementById('chHashtagName');
    var btn = doc.getElementById('chHashtagProposeBtn');
    var msg = doc.getElementById('chHashtagProposeMsg');
    if (!input || !btn || !msg) return null;
    function sync() {
      btn.hidden = !canPropose();
      if (btn.hidden) setMsg(msg, '', true);
    }
    function submit() {
      var v = validate(input.value);
      if (!v.ok) { setMsg(msg, v.error, false); return Promise.resolve(false); }
      btn.disabled = true;
      setMsg(msg, 'Sending…', true);
      return propose(v.name).then(function (r) { setMsg(msg, r.text, r.ok); return r.ok; },
        function () { setMsg(msg, 'Network error, try again.', false); return false; })
        .then(function (done) { btn.disabled = false; return done; });
    }
    input.addEventListener('input', function () {
      if (btn.hidden) return;
      var v = validate(input.value);
      setMsg(msg, (v.ok || trimGo(String(input.value || '')) === '') ? '' : v.error, v.ok);
    });
    btn.addEventListener('click', function () { submit(); });
    sync();
    return { sync: sync, submit: submit };
  }

  // mergeApproved marks the rows of approved channels and appends a row for
  // each approved channel without traffic. O(rows + names); returns a new
  // array, marked rows are copies, unmarked rows are the input objects.
  function mergeApproved(rows, names) {
    if (!Array.isArray(names) || names.length === 0) return rows;
    var want = new Set(names);
    var seen = new Set();
    var out = rows.map(function (ch) {
      if (!ch || !want.has(ch.name)) return ch;
      seen.add(ch.name);
      return Object.assign({}, ch, { approved: true });
    });
    names.forEach(function (n) {
      if (seen.has(n)) return;
      seen.add(n);
      out.push({ hash: n, name: n, messageCount: 0, lastActivityMs: 0, lastSender: '', lastMessage: '', approved: true });
    });
    return out;
  }

  function fmtDate(iso) { try { return new Date(iso).toLocaleString(); } catch (_) { return String(iso); } }

  function statusChip(status) {
    var label = STATUS_LABELS[status] || status;
    return '<span class="um-status um-status-' + escapeHtml(status) + '">' + escapeHtml(label) + '</span>';
  }

  function mineHtml(list) {
    if (!list || !list.length) return '<p class="account-hint">You have not proposed a channel yet. Use Channels, Add channel, Propose for everyone.</p>';
    return '<ul class="account-proposals">' + list.map(function (p) {
      return '<li data-subject="' + escapeHtml(p.subject) + '"><span>' + escapeHtml(p.subject) +
        ' <small>' + escapeHtml(fmtDate(p.createdAt)) + '</small></span> ' + statusChip(p.status) +
        (p.note ? '<p class="account-hint">' + escapeHtml(p.note) + '</p>' : '') + '</li>';
    }).join('') + '</ul>';
  }

  function loadMine(el, msgId) {
    return window.CSAuth.request('GET', '/api/account/proposals').then(function (r) {
      if (!r.ok) { window.CSAuth.say(msgId, window.CSAuth.errText(r), false); return; }
      el.innerHTML = mineHtml(r.data);
    }).catch(function () { window.CSAuth.say(msgId, 'Network error, try again.', false); });
  }

  window.CSProposals = {
    KIND: KIND, enabled: enabled, canPropose: canPropose, validate: validate, utf8Length: utf8Length,
    propose: propose, bindPropose: bindPropose, mergeApproved: mergeApproved,
    statusChip: statusChip, fmtDate: fmtDate, mineHtml: mineHtml, loadMine: loadMine
  };
})();
