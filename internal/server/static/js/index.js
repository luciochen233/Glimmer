// Public URL shortener page.
(function() {
    'use strict';

    var form = document.getElementById('shorten-form');
    if (form) {
        form.addEventListener('submit', function() {
            var input = document.getElementById('url');
            if (input.value && input.value.indexOf('://') === -1) {
                input.value = 'https://' + input.value;
            }
        });
    }

    var copyBtn = document.getElementById('copy-short-url');
    if (copyBtn) {
        copyBtn.addEventListener('click', function() {
            window.glimmerCopy(document.getElementById('short-url').textContent, copyBtn);
        });
    }
})();
