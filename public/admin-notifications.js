/* Push relay enrollment in the admin Notifications tab. */
(function () {
  'use strict';
  var endpoint = '/api/admin/notifications/push-relay';
  var sequence = 0;

  function mount(container) {
    var mounted = ++sequence;
    var enrollment;
    var busy = null;
    // The admin's relay pick survives re-renders: a preset origin, 'custom' or ''.
    var choice = '';
    var customOrigin = '';

    function render(message) {
      var detail = enrollment || {};
      var descriptions = {
        available: 'Enable watched-node alerts on phones through a push relay.',
        connected: 'Users can enable watched-node alerts in the mobile app that uses this relay.',
        manual: 'This enrollment is managed in the server configuration.',
        notifications_disabled: 'Node notifications are disabled in the server configuration.',
        unavailable: 'Self-service enrollment requires a publicly reachable HTTPS analyzer.'
      };
      var connected = detail.state === 'connected' || detail.state === 'manual';
      var paused = connected && detail.paused === true;
      var disconnectPending = detail.state === 'connected' && detail.disconnectPending === true;
      var description = descriptions[detail.state] || '';
      if (disconnectPending) {
        description = 'Push delivery is paused while disconnect is pending. Retry disconnect to finish removing the enrollment.';
      } else if (paused) {
        description = 'Push delivery is paused. Device links are kept, and alerts during the pause are skipped.';
      }
      var statuses = {
        available: 'Not connected',
        connected: 'Connected',
        manual: 'Connected',
        notifications_disabled: 'Notifications disabled',
        unavailable: 'Unavailable'
      };
      var status = disconnectPending ? 'Disconnect pending' : (paused ? 'Paused' : statuses[detail.state]);
      var statusClass = paused || disconnectPending ? ' is-paused' : (connected ? ' is-connected' : '');
      var html = '<section class="admin-card admin-notification-card" aria-labelledby="adminPushRelayTitle" aria-busy="' + !!busy + '">' +
        '<header class="admin-notification-heading"><h3 id="adminPushRelayTitle">Mobile push</h3>' +
        (status ? '<span class="admin-notification-status' + statusClass + '">' + status + '</span>' : '') +
        '</header>' +
        (description ? '<p class="admin-notification-description">' + description + '</p>' : '');
      if (detail.publicOrigin || detail.relayOrigin || detail.analyzerId) {
        html += '<dl class="admin-notification-details">';
        if (detail.publicOrigin) html += '<div><dt>Analyzer</dt><dd>' + escapeHtml(detail.publicOrigin) + '</dd></div>';
        if (detail.relayOrigin) {
          html += '<div><dt>Relay</dt><dd>' + (detail.relayName ? escapeHtml(detail.relayName) + ' · ' : '') +
            escapeHtml(detail.relayOrigin) + '</dd></div>';
        }
        if (detail.analyzerId) html += '<div><dt>Enrollment ID</dt><dd>' + escapeHtml(detail.analyzerId) + '</dd></div>';
        html += '</dl>';
      }
      if (busy) {
        var progress = {
          GET: 'Loading enrollment…',
          POST: 'Verifying analyzer and connecting to the relay…',
          PUT: paused ? 'Resuming delivery…' : 'Pausing delivery…',
          DELETE: 'Disconnecting from the relay…'
        };
        html += '<p role="status">' + progress[busy] + '</p>';
      }
      if (message) html += '<p class="account-msg err" role="alert">' + escapeHtml(message) + '</p>';
      var presets = detail.state === 'available' && !detail.originLocked && Array.isArray(detail.presets) ? detail.presets : [];
      var preset = presets.filter(function (p) { return p.origin === choice; })[0];
      if (detail.state === 'available') {
        if (!detail.originLocked) html += relayPicker(presets, preset);
        var ready = detail.originLocked || preset || (choice === 'custom' && customOrigin.trim() !== '');
        html += '<div class="admin-notification-actions"><button type="button" class="account-btn account-btn-primary" id="adminEnablePushRelay"' +
          (busy || !ready ? ' disabled' : '') + '>Enable</button></div>';
      } else if (connected) {
        var disabled = busy ? ' disabled' : '';
        html += '<div class="admin-notification-actions">';
        if (!disconnectPending) {
          html += '<button type="button" class="account-btn ' + (paused ? 'account-btn-primary' : 'account-btn-secondary') +
            '" id="adminPausePushRelay"' + disabled + '>' + (paused ? 'Resume delivery' : 'Pause delivery') + '</button>';
        }
        if (detail.state === 'connected') {
          html += '<button type="button" class="account-btn account-btn-secondary admin-notification-disconnect" id="adminDisconnectPushRelay"' +
            disabled + '>' + (disconnectPending ? 'Retry disconnect' : 'Disconnect relay') + '</button>';
        }
        html += '</div>';
        if (detail.state === 'manual') {
          html += '<p class="admin-notification-description">To disconnect this relay, revoke the enrollment and remove its server configuration.</p>';
        }
      } else if (!enrollment && !busy) {
        html += '<button type="button" class="account-btn account-btn-secondary" id="adminRetryPushRelay">Retry</button>';
      }
      container.innerHTML = html + '</section>';
      if (detail.state === 'available' && !busy) {
        if (!detail.originLocked) {
          container.querySelector('#adminPushRelayChoice').addEventListener('change', function (e) {
            choice = e.target.value;
            render();
          });
          if (choice === 'custom') {
            // Toggle Enable in place: re-rendering would replace the field and drop focus.
            container.querySelector('#adminPushRelayOrigin').addEventListener('input', function (e) {
              customOrigin = e.target.value;
              container.querySelector('#adminEnablePushRelay').disabled = customOrigin.trim() === '';
            });
          }
        }
        container.querySelector('#adminEnablePushRelay').addEventListener('click', function () {
          var origin = detail.originLocked ? detail.relayOrigin : (choice === 'custom' ? customOrigin.trim() : choice);
          if (origin) request('POST', { relayOrigin: origin });
        });
      } else if (connected && !busy) {
        if (!disconnectPending) {
          container.querySelector('#adminPausePushRelay').addEventListener('click', function () {
            request('PUT', { paused: !paused });
          });
        }
        if (detail.state === 'connected') {
          container.querySelector('#adminDisconnectPushRelay').addEventListener('click', function () {
            if (busy || mounted !== sequence) return;
            if (disconnectPending || window.confirm('Disconnect the push relay? This removes device links for this analyzer. Users will need to enable alerts again after you reconnect.')) {
              request('DELETE');
            }
          });
        }
      } else if (!enrollment && !busy) {
        container.querySelector('#adminRetryPushRelay').addEventListener('click', function () { request('GET'); });
      }
    }

    // Presets are relays known to the server; Custom takes any HTTPS relay
    // that implements the push relay contract. Nothing is picked by default.
    function relayPicker(presets, preset) {
      var options = '<option value=""' + (choice === '' ? ' selected' : '') + ' disabled>Choose a relay…</option>';
      presets.forEach(function (p) {
        options += '<option value="' + escapeHtml(p.origin) + '"' + (choice === p.origin ? ' selected' : '') + '>' +
          escapeHtml(p.name) + '</option>';
      });
      options += '<option value="custom"' + (choice === 'custom' ? ' selected' : '') + '>Custom relay</option>';
      var html = '<label class="account-field admin-notification-relay">Relay' +
        '<select id="adminPushRelayChoice"' + (busy ? ' disabled' : '') + '>' + options + '</select></label>';
      if (preset) {
        html += '<p class="admin-notification-description">Alerts go to users of the ' + escapeHtml(preset.name) +
          ' app, through ' + escapeHtml(preset.origin) + '.' +
          (preset.appUrl ? ' <a href="' + escapeHtml(preset.appUrl) + '" target="_blank" rel="noopener">About ' + escapeHtml(preset.name) + '</a>' : '') +
          '</p>';
      } else if (choice === 'custom') {
        html += '<label class="account-field admin-notification-relay">Relay origin' +
          '<input type="url" id="adminPushRelayOrigin" placeholder="https://push.example.org" autocomplete="off" spellcheck="false" value="' +
          escapeHtml(customOrigin) + '"' + (busy ? ' disabled' : '') + '></label>' +
          '<p class="admin-notification-description">A relay reaches only the users of the app whose push credentials it holds.</p>';
      }
      return html;
    }

    function request(method, body, previousError) {
      if (busy || mounted !== sequence) return;
      busy = method;
      render(previousError);

      function showError(error) {
        busy = null;
        // A lifecycle request can save a new state before reporting a cleanup failure.
        if (method === 'DELETE' || method === 'PUT') request('GET', undefined, error);
        else render(previousError || error);
      }

      CSAuth.request(method, endpoint, body).then(function (response) {
        if (mounted !== sequence) return;
        if (!response.ok) { showError(CSAuth.errText(response)); return; }
        busy = null;
        enrollment = response.data;
        if (enrollment && enrollment.state !== 'available') { choice = ''; customOrigin = ''; }
        render(previousError);
      }).catch(function () {
        if (mounted !== sequence) return;
        showError('Could not reach CoreScope. Try again.');
      });
    }

    request('GET');
  }

  window.CSAdminNotifications = { mount: mount, unmount: function () { sequence++; } };
})();
