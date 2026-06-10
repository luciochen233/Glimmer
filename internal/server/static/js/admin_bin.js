// Admin pastes grid: preview dialog, copy-link, QR, and card click handling.
(function() {
    'use strict';

    function previewURL(name, token) {
        var u = '/bin/' + encodeURIComponent(name);
        if (token) u += '/' + encodeURIComponent(token);
        return u;
    }

    function openPreview(name, token, title) {
        var dlg = document.getElementById('preview-dialog');
        var fullURL = previewURL(name, token);
        // iframe uses the embed view so it strips the sidebar / header / toolbar —
        // only the paste body is rendered inside the dialog.
        var embedURL = fullURL + '?embed=1';
        document.getElementById('preview-frame').src = embedURL;
        // The "Open ↗" link goes to the full URL (with chrome) so a new tab shows the
        // normal public page.
        document.getElementById('preview-open').href = fullURL;
        document.getElementById('preview-title').textContent = title || name;
        dlg.classList.remove('expanded');
        document.getElementById('preview-expand').innerHTML = '⤢';
        document.getElementById('preview-expand').setAttribute('aria-label', 'Expand');
        dlg.showModal();
    }

    function closePreview() {
        document.getElementById('preview-dialog').close();
    }

    function toggleExpand() {
        var dlg = document.getElementById('preview-dialog');
        var expanded = dlg.classList.toggle('expanded');
        var btn = document.getElementById('preview-expand');
        btn.innerHTML = expanded ? '⤡' : '⤢';
        btn.setAttribute('aria-label', expanded ? 'Shrink' : 'Expand');
    }

    function showBinQR(name, full) {
        var url = location.origin + '/bin/' + encodeURIComponent(name);
        var fetchUrl = '/admin/bin/qr/' + encodeURIComponent(name);
        if (full) fetchUrl += '?full=1';
        var overlay = document.getElementById('qr-modal-overlay');
        document.getElementById('qr-modal-image').src = fetchUrl;
        document.getElementById('qr-modal-link').textContent = url + (full ? ' (with token)' : '');
        var dl = document.getElementById('qr-modal-download');
        dl.href = fetchUrl;
        dl.download = name + '_QR.svg';
        overlay.classList.add('show');
    }

    // Per-card wiring: action buttons read name/token/title from the card's
    // data attributes; clicking the card itself (not an action) opens the
    // preview. Clicks on links/buttons/forms are ignored by the card handler,
    // so the action buttons need no stopPropagation.
    document.querySelectorAll('.js-preview').forEach(function(el) {
        var copyBtn = el.querySelector('.js-copy-link');
        if (copyBtn) {
            copyBtn.addEventListener('click', function() {
                var url = location.origin + previewURL(el.dataset.name, el.dataset.token);
                window.glimmerCopy(url, copyBtn);
            });
        }
        var qrBtn = el.querySelector('.js-bin-qr');
        if (qrBtn) {
            qrBtn.addEventListener('click', function() {
                showBinQR(el.dataset.name, false);
            });
        }

        el.addEventListener('click', function(e) {
            if (e.target.closest('.paste-card-actions')) return;
            if (e.target.closest('a, button, form, input')) return;
            openPreview(el.dataset.name, el.dataset.token, el.dataset.title);
        });
        el.setAttribute('tabindex', '0');
        el.setAttribute('role', 'button');
        el.addEventListener('keydown', function(e) {
            if ((e.key === 'Enter' || e.key === ' ') && !e.target.closest('.paste-card-actions')) {
                e.preventDefault();
                openPreview(el.dataset.name, el.dataset.token, el.dataset.title);
            }
        });
    });

    var expandBtn = document.getElementById('preview-expand');
    if (expandBtn) expandBtn.addEventListener('click', toggleExpand);
    var closeBtn = document.getElementById('preview-close');
    if (closeBtn) closeBtn.addEventListener('click', closePreview);

    // Clear iframe when dialog closes
    var dlg = document.getElementById('preview-dialog');
    if (dlg) {
        dlg.addEventListener('close', function() {
            document.getElementById('preview-frame').src = 'about:blank';
        });
    }
})();
