// Public paste viewer: copy-raw-content button.
(function() {
    'use strict';

    var btn = document.getElementById('copy-paste-btn');
    if (btn) {
        btn.addEventListener('click', function() {
            window.glimmerCopy(document.getElementById('paste-raw').innerText, btn);
        });
    }
})();
