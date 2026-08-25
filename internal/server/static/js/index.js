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

    // ---- Local history (browser localStorage, per-device) ----

    var HISTORY_KEY = 'glimmer.history.v1';
    var HISTORY_MAX = 100;

    function loadHistory() {
        try {
            var raw = window.localStorage.getItem(HISTORY_KEY);
            var list = raw ? JSON.parse(raw) : [];
            return Array.isArray(list) ? list : [];
        } catch (e) {
            return [];
        }
    }

    function saveHistory(list) {
        try {
            window.localStorage.setItem(HISTORY_KEY, JSON.stringify(list));
        } catch (e) { /* storage full or blocked — history is best-effort */ }
    }

    // Move an entry to the top (or insert it), capped at HISTORY_MAX.
    function upsertEntry(shortURL, originalURL, svg) {
        var list = loadHistory().filter(function(e) { return e.s !== shortURL; });
        list.unshift({ s: shortURL, o: originalURL, q: svg, t: Date.now() });
        if (list.length > HISTORY_MAX) list = list.slice(0, HISTORY_MAX);
        saveHistory(list);
    }

    // The stored SVG was rendered by our own server, but re-validate before
    // injecting it back into the DOM (localStorage is user-tamperable).
    function safeSVG(s) {
        if (typeof s !== 'string') return '';
        if (!/^<svg[\s\S]*<\/svg>$/.test(s)) return '';
        if (/<script|on\w+\s*=/i.test(s)) return '';
        return s;
    }

    // ---- Share image rendering (scoped to a single result card) ----

    function roundRect(ctx, x, y, w, h, r) {
        ctx.beginPath();
        ctx.moveTo(x + r, y);
        ctx.arcTo(x + w, y, x + w, y + h, r);
        ctx.arcTo(x + w, y + h, x, y + h, r);
        ctx.arcTo(x, y + h, x, y, r);
        ctx.arcTo(x, y, x + w, y, r);
        ctx.closePath();
    }

    function ellipsize(ctx, line, maxW) {
        if (ctx.measureText(line).width <= maxW) return line;
        while (line.length > 1 && ctx.measureText(line + '\u2026').width > maxW) {
            line = line.substring(0, line.length - 1);
        }
        return line + '\u2026';
    }

    // Greedy word wrap (for the name), capped at maxLines with an ellipsis.
    function wrapWords(ctx, text, maxW, maxLines) {
        var words = String(text).trim().split(/\s+/).filter(Boolean);
        if (!words.length) return [];
        var lines = [];
        var cur = words[0];
        for (var i = 1; i < words.length; i++) {
            var probe = cur + ' ' + words[i];
            if (ctx.measureText(probe).width <= maxW) {
                cur = probe;
            } else {
                lines.push(cur);
                cur = words[i];
            }
        }
        lines.push(cur);
        if (lines.length > maxLines) {
            lines = lines.slice(0, maxLines);
            lines[maxLines - 1] = ellipsize(ctx, lines[maxLines - 1], maxW);
        }
        return lines;
    }

    // Shrink the font until text fits on one line; returns the final px size.
    function fitOneLine(ctx, text, fontTpl, basePx, minPx, maxW) {
        var px = basePx;
        ctx.font = fontTpl.replace('{px}', String(px));
        while (px > minPx && ctx.measureText(text).width > maxW) {
            px -= 2;
            ctx.font = fontTpl.replace('{px}', String(px));
        }
        return px;
    }

    var FONT_SANS = 'Outfit, -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
    var FONT_MONO = 'ui-monospace, "SF Mono", Menlo, Consolas, monospace';

    function drawShareImage(img, name, url) {
        var W = 2520, H = 1080; // 21:9
        var canvas = document.createElement('canvas');
        canvas.width = W;
        canvas.height = H;
        var ctx = canvas.getContext('2d');

        // Cream background matching the design system.
        ctx.fillStyle = '#fafaf9';
        ctx.fillRect(0, 0, W, H);

        // Left: white card holding the QR code.
        var pad = 88;
        var cardS = H - pad * 2; // 904
        ctx.fillStyle = '#ffffff';
        roundRect(ctx, pad, pad, cardS, cardS, 36);
        ctx.fill();
        ctx.drawImage(img, pad + 32, pad + 32, cardS - 64, cardS - 64);

        // Right: accent bar, name (up to 2 lines), short URL (always one line).
        var tx = pad + cardS + 96; // 1088
        var tw = W - tx - pad;     // 1344

        var nameFont = '700 84px ' + FONT_SANS;
        ctx.font = nameFont;
        var nameLines = wrapWords(ctx, name, tw, 2);

        var urlFontTpl = '500 {px}px ' + FONT_MONO;
        var urlPx = fitOneLine(ctx, url, urlFontTpl, 58, 24, tw);
        var urlFont = urlFontTpl.replace('{px}', String(urlPx));
        ctx.font = urlFont;
        url = ellipsize(ctx, url, tw); // safety net if even the minimum overflows

        var barH = 14, barGap = 48, nameLH = 102, urlGap = 40, urlLH = Math.round(urlPx * 1.3);
        var blockH = barH + barGap + nameLines.length * nameLH + urlGap + urlLH;
        var y = (H - blockH) / 2;

        ctx.fillStyle = '#d97706';
        roundRect(ctx, tx, y, 128, barH, 7);
        ctx.fill();
        y += barH + barGap;

        ctx.textBaseline = 'top';
        ctx.fillStyle = '#1c1917';
        ctx.font = nameFont;
        for (var i = 0; i < nameLines.length; i++) {
            ctx.fillText(nameLines[i], tx, y);
            y += nameLH;
        }

        y += urlGap;
        ctx.fillStyle = '#d97706';
        ctx.font = urlFont;
        ctx.fillText(url, tx, y);

        return canvas;
    }

    // Make sure the webfont is available to the canvas before drawing.
    function preloadFonts(cb) {
        if (!(document.fonts && document.fonts.load)) { cb(); return; }
        Promise.all([
            document.fonts.load('700 84px Outfit'),
            document.fonts.load('500 58px Outfit')
        ]).then(cb, cb);
    }

    // Serialize the card's QR SVG into a data: URL drawable on a canvas
    // (img-src allows data:, and data: images never taint the canvas).
    function buildCanvas(card, cb) {
        var url = card.querySelector('.qr-share-url').textContent.trim();
        var nameInput = card.querySelector('.qr-caption');
        var name = (nameInput && nameInput.value.trim()) || 'Scan me';
        var svgEl = card.querySelector('.qr-share-code svg');
        if (!svgEl || !url) return;
        var clone = svgEl.cloneNode(true);
        clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
        var src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(new XMLSerializer().serializeToString(clone));
        var img = new Image();
        img.onload = function() {
            preloadFonts(function() { cb(drawShareImage(img, name, url)); });
        };
        img.src = src;
    }

    // Wire copy + generate/save behaviour into one result card (fresh or
    // restored from history).
    function wireCard(card) {
        var copyBtn = card.querySelector('.js-copy');
        if (copyBtn) {
            copyBtn.addEventListener('click', function() {
                window.glimmerCopy(card.querySelector('.qr-share-url').textContent.trim(), copyBtn);
            });
        }

        var imgBtn = card.querySelector('.js-image-btn');
        var preview = card.querySelector('.qr-preview');
        var previewImg = card.querySelector('.qr-preview img');
        if (!imgBtn || !preview) return;
        var generated = false;

        function playPrint() {
            preview.hidden = false;
            preview.classList.remove('print');
            void preview.offsetWidth; // restart the animation
            preview.classList.add('print');
        }

        imgBtn.addEventListener('click', function() {
            if (!generated) {
                buildCanvas(card, function(canvas) {
                    previewImg.src = canvas.toDataURL('image/png');
                    playPrint();
                    generated = true;
                    imgBtn.textContent = 'Save Image';
                });
                return;
            }
            buildCanvas(card, function(canvas) {
                // Keep the preview in sync in case the name changed since generating.
                previewImg.src = canvas.toDataURL('image/png');
                canvas.toBlob(function(blob) {
                    if (!blob) return;
                    var a = document.createElement('a');
                    a.href = URL.createObjectURL(blob);
                    var url = card.querySelector('.qr-share-url').textContent.trim();
                    var slugPart = url.split('/').filter(Boolean).pop() || 'link';
                    a.download = 'qr-' + slugPart + '.png';
                    document.body.appendChild(a);
                    a.click();
                    setTimeout(function() {
                        URL.revokeObjectURL(a.href);
                        a.parentNode.removeChild(a);
                    }, 0);
                    imgBtn.textContent = 'Saved!';
                    setTimeout(function() { imgBtn.textContent = 'Save Image'; }, 1500);
                }, 'image/png');
            });
        });
    }

    // ---- History rendering ----

    var idSeq = 0;

    function buildHistoryCard(entry) {
        var tpl = document.getElementById('qr-history-tpl');
        var card = tpl.content.firstElementChild.cloneNode(true);

        // Unique input id so the label's `for` stays valid across cards.
        idSeq++;
        var input = card.querySelector('.qr-caption');
        var label = card.querySelector('label.form-label');
        var id = 'qr-caption-h' + idSeq;
        input.id = id;
        if (label) label.setAttribute('for', id);

        card.querySelector('.qr-collapsed-original').textContent = entry.o || '';
        card.querySelector('.qr-collapsed-short').textContent = entry.s || '';

        var link = card.querySelector('.qr-original-link');
        if (entry.o) {
            link.textContent = entry.o;
            link.href = entry.o;
        }

        var svg = safeSVG(entry.q);
        if (svg) card.querySelector('.qr-share-code').innerHTML = svg;
        card.querySelector('.qr-share-url').textContent = entry.s || '';

        function toggle() {
            card.classList.toggle('collapsed');
        }
        card.querySelector('.qr-collapsed').addEventListener('click', toggle);
        card.querySelector('.qr-chev').addEventListener('click', function(e) {
            e.stopPropagation();
            toggle();
        });

        wireCard(card);
        return card;
    }

    function renderHistory(excludeShort) {
        var host = document.getElementById('qr-history');
        if (!host) return;
        host.innerHTML = '';
        loadHistory().forEach(function(entry) {
            if (entry.s === excludeShort) return;
            host.appendChild(buildHistoryCard(entry));
        });
    }

    // ---- Init ----

    var active = document.getElementById('qr-result');
    if (active) {
        var shortURL = active.querySelector('.qr-share-url').textContent.trim();
        var originalURL = active.querySelector('.qr-original-link').textContent.trim();
        var svgHTML = active.querySelector('.qr-share-code').innerHTML.trim();
        if (shortURL) upsertEntry(shortURL, originalURL, svgHTML);
        wireCard(active);
        renderHistory(shortURL);
    } else {
        renderHistory('');
    }
})();
