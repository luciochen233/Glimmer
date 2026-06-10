// Admin dashboard: hostname compression for tiles and QR modal buttons.
(function() {
    'use strict';

    // Compress URL displays to hostname for visual cleanliness
    document.querySelectorAll('[data-hostname]').forEach(function(el) {
        try {
            var u = new URL(el.textContent.trim());
            el.textContent = u.hostname + (u.pathname !== '/' ? u.pathname : '');
        } catch (e) {}
    });

    document.querySelectorAll('.js-qr').forEach(function(btn) {
        btn.addEventListener('click', function() {
            var slug = btn.getAttribute('data-slug');
            window.openQRModal(slug, slug);
        });
    });
})();
