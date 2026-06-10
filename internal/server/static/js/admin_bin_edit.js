// Paste editor: token radio toggling, name preview, and upload wiring.
(function() {
    'use strict';

    function toggleCustomToken() {
        var wrap = document.getElementById('custom-token-wrap');
        if (!wrap) return;
        var checked = document.querySelector('input[name="token_action"][value="custom"]:checked') ||
                      document.querySelector('input[name="enable_token"][value="custom"]:checked');
        wrap.style.display = checked ? 'block' : 'none';
    }

    document.querySelectorAll('input[name="token_action"], input[name="enable_token"]').forEach(function(radio) {
        radio.addEventListener('change', toggleCustomToken);
    });
    toggleCustomToken();

    // Slug preview updater
    var nameInput = document.getElementById('name');
    var preview = document.getElementById('name-preview');
    if (nameInput && preview) {
        nameInput.addEventListener('input', function() {
            preview.textContent = this.value || '...';
        });
    }

    var textarea = document.querySelector('textarea[name="content"]');
    var status = document.getElementById('upload-status');
    var imgBtn = document.getElementById('upload-img-btn');

    function insertAtCursor(text) {
        var start = textarea.selectionStart;
        var end = textarea.selectionEnd;
        var before = textarea.value.substring(0, start);
        var after = textarea.value.substring(end);
        textarea.value = before + text + '\n' + after;
        textarea.selectionStart = textarea.selectionEnd = start + text.length + 1;
        textarea.focus();
    }

    var uploader = window.GlimmerUpload.init({
        imgBtn:    imgBtn,
        fileBtn:   document.getElementById('upload-file-btn'),
        imgInput:  document.getElementById('img-input'),
        fileInput: document.getElementById('file-input'),
        statusEl:  status,
        maxImageMB: parseInt(imgBtn.getAttribute('data-max-mb'), 10) || 50,
        onUploaded: function(data) {
            insertAtCursor(data.markdown);
            status.textContent = 'Uploaded';
            setTimeout(function() { status.textContent = ''; }, 3000);
        }
    });

    textarea.addEventListener('paste', function(e) {
        var items = (e.clipboardData || {}).items || [];
        for (var i = 0; i < items.length; i++) {
            if (items[i].type.indexOf('image') !== -1) {
                e.preventDefault();
                uploader.doUpload(items[i].getAsFile(), true);
                return;
            }
        }
    });
})();
