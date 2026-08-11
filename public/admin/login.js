(function () {
  'use strict';

  var form = document.getElementById('login-form');
  var errorBox = document.getElementById('error');
  var submitBtn = document.getElementById('submit-btn');

  function showError(msg) {
    errorBox.textContent = msg;
    errorBox.style.display = 'block';
  }

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    errorBox.style.display = 'none';
    submitBtn.disabled = true;
    submitBtn.textContent = 'Logging in…';

    var username = document.getElementById('username').value;
    var password = document.getElementById('password').value;

    fetch('/api/admin/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: username, password: password })
    })
      .then(function (res) {
        if (res.ok) {
          window.location.href = '/admin/';
          return;
        }
        return res.json().catch(function () { return {}; }).then(function (body) {
          throw new Error(body.error || ('login failed (' + res.status + ')'));
        });
      })
      .catch(function (err) {
        showError(err.message || 'Login failed');
        submitBtn.disabled = false;
        submitBtn.textContent = 'Log in';
      });
  });
})();
