// Token prompt for protected pastes: redirect to /bin/{name}/{token}.
(function() {
    'use strict';

    var form = document.getElementById('token-form');
    if (form) {
        form.addEventListener('submit', function(e) {
            e.preventDefault();
            var token = document.getElementById('token-input').value.trim();
            if (token) {
                window.location.href = window.location.pathname + '/' + encodeURIComponent(token);
            }
        });
    }
})();
