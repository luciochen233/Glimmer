// Shared chrome behaviour for every layout page: QR modal, confirm-on-submit
// forms, and a clipboard helper. The CSP blocks inline scripts, so all page
// behaviour lives in files under /static/js/.
(function() {
    'use strict';

    // Copy text to the clipboard and flash the button label. Used by the
    // per-page scripts.
    window.glimmerCopy = function(text, btn) {
        var orig = btn.textContent;
        navigator.clipboard.writeText(text).then(function() {
            btn.textContent = 'Copied!';
            setTimeout(function() { btn.textContent = orig; }, 1500);
        });
    };

    window.openQRModal = function(slug, name) {
        var overlay = document.getElementById('qr-modal-overlay');
        document.getElementById('qr-modal-image').src = '/admin/qr/' + slug;
        document.getElementById('qr-modal-link').textContent = '/admin/qr/' + slug;
        var dl = document.getElementById('qr-modal-download');
        dl.href = '/admin/qr/' + slug;
        dl.download = (name || slug) + '_QR.svg';
        overlay.classList.add('show');
    };

    window.closeQRModal = function() {
        document.getElementById('qr-modal-overlay').classList.remove('show');
        document.getElementById('qr-modal-image').src = '';
    };

    var overlay = document.getElementById('qr-modal-overlay');
    if (overlay) {
        overlay.addEventListener('click', function(e) {
            if (e.target === overlay) window.closeQRModal();
        });
    }
    var closeBtn = document.getElementById('qr-modal-close');
    if (closeBtn) {
        closeBtn.addEventListener('click', function() { window.closeQRModal(); });
    }
    document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape') window.closeQRModal();
    });

    // Forms with data-confirm ask before submitting (replaces inline
    // onsubmit="return confirm(...)"). Delegated so it works for any form.
    document.addEventListener('submit', function(e) {
        var form = e.target;
        if (form && form.getAttribute && form.getAttribute('data-confirm')) {
            if (!window.confirm(form.getAttribute('data-confirm'))) {
                e.preventDefault();
            }
        }
    });
})();
