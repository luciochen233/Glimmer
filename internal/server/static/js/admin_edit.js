// Edit-link page: live slug preview.
(function() {
    'use strict';

    var slugInput = document.getElementById('slug');
    var preview = document.getElementById('slug-preview');
    if (slugInput && preview) {
        slugInput.addEventListener('input', function() {
            preview.textContent = this.value || '...';
        });
    }
})();
