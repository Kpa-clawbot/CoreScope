/* Push relay enrollment in the admin Notifications tab. */
(function () {
  'use strict';
  var endpoint = '/api/admin/notifications/push-relay';
  var sequence = 0;

  function mount(container) {
    var mounted = ++sequence;
    var enrollment;
    var busy = null;

    function render(message) {
      var detail = enrollment || {};
      var descriptions = {
        available: 'Enable watched-node alerts on phones through a push relay.',
        connected: 'Users can enable watched-node alerts in the mobile app that uses this relay.',
        manual: 'This enrollment is managed in the server configuration.',
        notifications_disabled: 'Node notifications are disabled in the server configuration.',
        unavailable: detail.relayOrigin
          ? 'Self-service enrollment requires a publicly reachable HTTPS analyzer.'
          : 'Set the server relay override to an official NodeScope HTTPS relay.'
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
        if (detail.relayOrigin) html += '<div><dt>Relay</dt><dd>' + escapeHtml(detail.relayOrigin) + '</dd></div>';
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
      if (detail.state === 'available') {
        html += '<p><button type="button" class="account-btn account-btn-primary" id="adminEnablePushRelay"' + (busy ? ' disabled' : '') + '>Enable</button></p>';
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
        container.querySelector('#adminEnablePushRelay').addEventListener('click', function () { request('POST'); });
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
