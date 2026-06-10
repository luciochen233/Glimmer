// Uploads admin page: copy-markdown, resize dialog, and upload wiring.
(function() {
    'use strict';

    var currentResizeFile = '';

    document.querySelectorAll('.js-copy-md').forEach(function(btn) {
        btn.addEventListener('click', function() {
            var filename = btn.getAttribute('data-filename');
            var url = '/uploads/' + filename;
            var label = btn.getAttribute('data-original') || filename;
            var md = btn.getAttribute('data-image') ? '![](' + url + ')' : '[' + label + '](' + url + ')';
            window.glimmerCopy(md, btn);
        });
    });

    document.querySelectorAll('.js-resize').forEach(function(btn) {
        btn.addEventListener('click', function() {
            currentResizeFile = btn.getAttribute('data-filename');
            document.getElementById('resize-current').textContent =
                'Current: ' + btn.getAttribute('data-width') + '×' + btn.getAttribute('data-height');
            document.getElementById('resize-status').textContent = '';
            document.getElementById('resize-confirm').disabled = false;
            document.getElementById('resize-dialog').showModal();
        });
    });

    document.querySelectorAll('.js-resize-cancel').forEach(function(btn) {
        btn.addEventListener('click', function() {
            document.getElementById('resize-dialog').close();
        });
    });

    document.getElementById('resize-confirm').addEventListener('click', function() {
        var dim = document.getElementById('resize-dim').value;
        var status = document.getElementById('resize-status');
        var btn = this;
        status.textContent = 'Resizing…';
        btn.disabled = true;

        var fd = new FormData();
        fd.append('csrf_token', window.GlimmerUpload.getCsrfToken());
        fd.append('max_dim', dim);

        fetch('/admin/uploads/resize/' + currentResizeFile, { method: 'POST', body: fd, credentials: 'same-origin' })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                btn.disabled = false;
                if (data.error) {
                    status.textContent = 'Error: ' + data.error;
                    return;
                }
                var sizeEl = document.getElementById('size-' + currentResizeFile);
                var dimEl = document.getElementById('dim-' + currentResizeFile);
                if (sizeEl) sizeEl.textContent = data.size_human;
                if (dimEl) dimEl.textContent = data.width + '×' + data.height;
                status.textContent = 'Done — ' + data.size_human;
                var imgs = document.querySelectorAll('img[src="/uploads/' + currentResizeFile + '"]');
                imgs.forEach(function(img) { img.src = '/uploads/' + currentResizeFile + '?t=' + Date.now(); });
            })
            .catch(function() {
                btn.disabled = false;
                status.textContent = 'Failed';
            });
    });

    var imgBtn = document.getElementById('upload-img-btn');
    window.GlimmerUpload.init({
        imgBtn:    imgBtn,
        fileBtn:   document.getElementById('upload-file-btn'),
        imgInput:  document.getElementById('img-input'),
        fileInput: document.getElementById('file-input'),
        statusEl:  document.getElementById('upload-status'),
        maxImageMB: parseInt(imgBtn.getAttribute('data-max-mb'), 10) || 50,
        onUploaded: function() { location.reload(); }
    });
})();
