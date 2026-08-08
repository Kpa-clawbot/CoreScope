(function () {
  'use strict';

  var whoEl = document.getElementById('who');
  var searchInput = document.getElementById('search-input');
  var searchBtn = document.getElementById('search-btn');
  var searchResults = document.getElementById('search-results');
  var infraList = document.getElementById('infra-list');

  function escapeHTML(s) {
    var div = document.createElement('div');
    div.textContent = s;
    return div.innerHTML;
  }

  function getCookie(name) {
    var match = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
    return match ? decodeURIComponent(match[1]) : '';
  }

  // Double-submit CSRF — same pattern as admin.js (see its comment for
  // why a cross-site page can't forge this header).
  function csrfHeaders() {
    return { 'X-CSRF-Token': getCookie('corescope_admin_csrf') };
  }

  function fetchJSON(url, opts) {
    opts = opts || {};
    var headers = Object.assign({}, opts.headers, csrfHeaders());
    return fetch(url, Object.assign({ credentials: 'same-origin' }, opts, { headers: headers })).then(function (res) {
      if (res.status === 401) {
        window.location.href = '/admin/login';
        return Promise.reject(new Error('not logged in'));
      }
      return res.json().then(function (body) {
        if (!res.ok) throw new Error(body.error || ('request failed (' + res.status + ')'));
        return body;
      });
    });
  }

  function renderWho(me) {
    whoEl.innerHTML = 'Logged in as <strong>' + escapeHTML(me.username) + '</strong> (' + escapeHTML(me.role) + ')';
    var logout = document.createElement('button');
    logout.className = 'link';
    logout.textContent = 'Log out';
    logout.addEventListener('click', function () {
      fetch('/api/admin/logout', { method: 'POST', credentials: 'same-origin', headers: csrfHeaders() }).then(function () {
        window.location.href = '/admin/login';
      });
    });
    whoEl.appendChild(logout);
  }

  // Polls /api/admin/nodes/infrastructure/status?id=<id> until the
  // ingestor (which owns the writable DB handle) reports done/error.
  // The write is asynchronous — see internal/infraqueue — so an
  // enqueue accepted by the server isn't applied yet. The ingestor
  // drains this queue on a 15s ticker (cmd/ingestor/main.go), so a
  // request enqueued just after a tick can legitimately take close to
  // 15s to be picked up — the timeout here must clear that with room
  // to spare, or a perfectly healthy request reads as a false failure.
  function pollStatus(statusUrl) {
    var start = Date.now();
    var timeoutMs = 25000;
    var intervalMs = 500;
    return new Promise(function (resolve, reject) {
      (function tick() {
        fetchJSON(statusUrl).then(function (body) {
          if (body.status === 'pending') {
            if (Date.now() - start > timeoutMs) {
              reject(new Error('timed out waiting for the change to apply'));
              return;
            }
            setTimeout(tick, intervalMs);
            return;
          }
          if (body.status === 'error') {
            reject(new Error(body.error || 'failed to apply'));
            return;
          }
          resolve(body);
        }).catch(reject);
      })();
    });
  }

  function toggleInfrastructure(pubkey, newValue, btn, errEl) {
    btn.disabled = true;
    btn.textContent = 'Saving…';
    errEl.textContent = '';

    fetchJSON('/api/admin/nodes/infrastructure', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ pubkey: pubkey, infrastructure: newValue })
    })
      .then(function (body) {
        return pollStatus(body.statusUrl);
      })
      .then(function () {
        // Refresh both views — the toggled node may now belong (or no
        // longer belong) in either list.
        loadInfraList();
        if (searchInput.value.trim()) runSearch();
      })
      .catch(function (err) {
        errEl.textContent = err.message || 'Failed to save';
        btn.disabled = false;
        btn.textContent = newValue ? 'Mark infrastructure' : 'Remove';
      });
  }

  function nodeRow(node, opts) {
    var tr = document.createElement('tr');
    var isInfra = !!node.infrastructure;
    var td = function (text) {
      var el = document.createElement('td');
      el.textContent = text;
      return el;
    };
    tr.appendChild(td(node.name || '(unnamed)'));
    tr.appendChild(td(node.role || '?'));
    var pkTd = document.createElement('td');
    pkTd.className = 'pubkey';
    pkTd.textContent = node.public_key;
    tr.appendChild(pkTd);

    var actionTd = document.createElement('td');
    var btn = document.createElement('button');
    btn.className = 'toggle ' + (isInfra ? 'on' : 'off');
    btn.textContent = isInfra ? 'Remove' : 'Mark infrastructure';
    var errEl = document.createElement('div');
    errEl.className = 'row-error';
    btn.addEventListener('click', function () {
      toggleInfrastructure(node.public_key, !isInfra, btn, errEl);
    });
    actionTd.appendChild(btn);
    actionTd.appendChild(errEl);
    tr.appendChild(actionTd);

    return tr;
  }

  function renderEmptyRow(tbody, cols, message) {
    var tr = document.createElement('tr');
    tr.className = 'empty-row';
    var td = document.createElement('td');
    td.colSpan = cols;
    td.textContent = message;
    tr.appendChild(td);
    tbody.appendChild(tr);
  }

  function loadInfraList() {
    return fetchJSON('/api/nodes/infrastructure').then(function (body) {
      infraList.innerHTML = '';
      var nodes = body.nodes || [];
      if (nodes.length === 0) {
        renderEmptyRow(infraList, 4, 'No infrastructure nodes yet.');
        return;
      }
      nodes.forEach(function (n) {
        infraList.appendChild(nodeRow(n));
      });
    });
  }

  function runSearch() {
    var q = searchInput.value.trim();
    searchResults.innerHTML = '';
    if (!q) {
      renderEmptyRow(searchResults, 4, 'Type a name or public key to search.');
      return;
    }
    fetchJSON('/api/nodes/search?q=' + encodeURIComponent(q)).then(function (body) {
      searchResults.innerHTML = '';
      var nodes = body.nodes || [];
      if (nodes.length === 0) {
        renderEmptyRow(searchResults, 4, 'No matching nodes.');
        return;
      }
      nodes.forEach(function (n) {
        searchResults.appendChild(nodeRow(n));
      });
    });
  }

  searchBtn.addEventListener('click', runSearch);
  searchInput.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') runSearch();
  });

  fetchJSON('/api/admin/me')
    .then(function (me) {
      renderWho(me);
      renderEmptyRow(searchResults, 4, 'Type a name or public key to search.');
      return loadInfraList();
    })
    .then(function () {
      document.body.classList.add('authed');
    })
    .catch(function (err) {
      if (err.message !== 'not logged in') {
        console.error('[admin-infra]', err);
        document.body.classList.add('authed');
      }
    });
})();
