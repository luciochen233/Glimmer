// Shared upload helper for the admin uploads page and the paste editor.
// Wires up an "Upload Image" and "Upload File" button pair to their hidden
// file inputs and posts to /admin/upload (images) or /admin/upload-file
// (any file, 5 MB).
//
// Images at or above the configured size limit cannot be uploaded at full
// size; the helper offers to compress them in the browser (canvas downscale +
// JPEG re-encode), iteratively halving resolution / lowering quality until the
// result fits just under the limit. Only the compressed result is uploaded —
// the oversized original is never sent.
//
// Usage: call window.GlimmerUpload.init({
//   imgBtn, fileBtn, imgInput, fileInput, statusEl,
//   maxImageMB,                                       // server image limit (MB)
//   onUploaded: function(data, isImage) { ... }       // required
// });

(function(window) {
	var FILE_MAX = 5 * 1024 * 1024;
	// Images larger than this (but still under the limit) offer optional
	// compression for a faster upload.
	var COMPRESS_THRESHOLD = 10 * 1024 * 1024;
	// Aim for the compressed result to land at this fraction of the hard limit,
	// leaving headroom for multipart overhead so it's reliably accepted.
	var COMPRESS_SAFETY = 0.95;
	// Initial longest-edge cap; resolution is halved from here on each retry.
	var COMPRESS_MAX_DIMENSION = 4000;
	// Stop halving once the longest edge drops to this — give up rather than
	// produce an unusably tiny image.
	var COMPRESS_MIN_DIMENSION = 320;
	// JPEG qualities tried at each resolution, best first.
	var COMPRESS_QUALITIES = [0.85, 0.7, 0.55, 0.4];

	function mb(bytes) {
		return (bytes / (1024 * 1024)).toFixed(1);
	}

	// renameToJpg replaces a filename's extension with .jpg (compression always
	// outputs JPEG). Falls back to "image.jpg" if there's no usable base name.
	function renameToJpg(name) {
		if (!name) return 'image.jpg';
		var dot = name.lastIndexOf('.');
		var base = dot > 0 ? name.slice(0, dot) : name;
		return base + '.jpg';
	}

	// compressToTarget downscales + re-encodes an image File to a JPEG Blob no
	// larger than targetBytes. It starts at COMPRESS_MAX_DIMENSION and, for each
	// resolution, tries the quality ladder; if nothing fits it halves the
	// resolution and retries, down to COMPRESS_MIN_DIMENSION.
	//   onAttempt(width, height, size) — progress callback (optional)
	//   cb(blob, err, smallestBytes)   — blob on success; on failure blob is
	//       null, err is set, and smallestBytes is the smallest size achieved.
	function compressToTarget(file, targetBytes, onAttempt, cb) {
		if (!window.URL || !window.URL.createObjectURL) { cb(null, 'no object URL support'); return; }
		var url = window.URL.createObjectURL(file);
		var img = new Image();
		img.onload = function() {
			var ow = img.naturalWidth || img.width;
			var oh = img.naturalHeight || img.height;
			window.URL.revokeObjectURL(url);
			if (!ow || !oh) { cb(null, 'bad image dimensions'); return; }

			var smallest = 0; // smallest blob size seen, for failure messaging

			function attempt(scale) {
				var cw = Math.max(1, Math.round(ow * scale));
				var ch = Math.max(1, Math.round(oh * scale));
				var canvas = document.createElement('canvas');
				canvas.width = cw;
				canvas.height = ch;
				var ctx = canvas.getContext('2d');
				if (!ctx) { cb(null, 'no canvas context'); return; }
				ctx.drawImage(img, 0, 0, cw, ch);
				if (!canvas.toBlob) { cb(null, 'no toBlob support'); return; }

				var qi = 0;
				function tryQuality() {
					if (qi >= COMPRESS_QUALITIES.length) {
						// Nothing fit at this resolution. Halve and retry, unless
						// we've already shrunk as far as we're willing to.
						if (Math.max(cw, ch) <= COMPRESS_MIN_DIMENSION) {
							cb(null, 'too large', smallest);
							return;
						}
						attempt(scale * 0.5);
						return;
					}
					var q = COMPRESS_QUALITIES[qi++];
					canvas.toBlob(function(blob) {
						if (!blob) { tryQuality(); return; }
						if (!smallest || blob.size < smallest) smallest = blob.size;
						if (onAttempt) onAttempt(cw, ch, blob.size);
						if (blob.size <= targetBytes) { cb(blob, null); return; }
						tryQuality();
					}, 'image/jpeg', q);
				}
				tryQuality();
			}

			attempt(Math.min(1, COMPRESS_MAX_DIMENSION / Math.max(ow, oh)));
		};
		img.onerror = function() {
			window.URL.revokeObjectURL(url);
			cb(null, 'could not load image');
		};
		img.src = url;
	}

	function getCsrfToken() {
		var match = document.cookie.match(/(?:^|;\s*)csrf=([^;]+)/);
		return match ? match[1] : '';
	}

	function init(opts) {
		var imgBtn = opts.imgBtn;
		var fileBtn = opts.fileBtn;
		var imgInput = opts.imgInput;
		var fileInput = opts.fileInput;
		var statusEl = opts.statusEl;
		var onUploaded = opts.onUploaded;
		var imgMaxBytes = (opts.maxImageMB ? opts.maxImageMB : 50) * 1024 * 1024;
		var imgMaxMB = Math.round(imgMaxBytes / (1024 * 1024));

		function setDisabled(v) {
			if (imgBtn) imgBtn.disabled = v;
			if (fileBtn) fileBtn.disabled = v;
		}

		function setStatus(msg) {
			if (statusEl) statusEl.textContent = msg;
		}

		function doUpload(file, isImage) {
			if (isImage) {
				if (!file || !file.type || file.type.indexOf('image/') !== 0) {
					setStatus('Not an image file');
					return;
				}
				var fileMB = mb(file.size);

				if (file.size > imgMaxBytes) {
					// Over the limit — the original cannot be uploaded. Compress to fit.
					var ok = window.confirm('This image is ' + fileMB + ' MB, over the ' + imgMaxMB + ' MB limit.\n\nCompress it in your browser to fit under the limit? It cannot be uploaded at full size — press Cancel to abort.');
					if (!ok) {
						setStatus('Cancelled — image exceeds the ' + imgMaxMB + ' MB limit.');
						return;
					}
					runCompression(file, Math.floor(imgMaxBytes * COMPRESS_SAFETY), true);
					return;
				}

				if (file.size > COMPRESS_THRESHOLD) {
					// Under the limit but large — offer optional compression.
					var ok2 = window.confirm('This image is ' + fileMB + ' MB. Compress it in your browser for a faster upload?\n\nPress Cancel to upload the original.');
					if (ok2) {
						runCompression(file, COMPRESS_THRESHOLD, false);
						return;
					}
				}
			}
			proceedUpload(file, isImage, null);
		}

		// runCompression drives compressToTarget and then uploads the result.
		// When mandatory (original is over the limit), a failure to reach the
		// target aborts; otherwise it falls back to uploading the original.
		function runCompression(file, targetBytes, mandatory) {
			setStatus('Compressing…');
			setDisabled(true);
			compressToTarget(file, targetBytes, function(cw, ch, size) {
				setStatus('Compressing… ' + cw + '×' + ch + ' → ' + mb(size) + ' MB');
			}, function(blob, err, smallest) {
				if (err || !blob) {
					if (mandatory) {
						setDisabled(false);
						var extra = smallest ? (' Smallest achievable was ' + mb(smallest) + ' MB.') : '';
						setStatus('Could not compress below the ' + imgMaxMB + ' MB limit.' + extra + ' Try a smaller image.');
					} else {
						// Optional path: just upload the original.
						proceedUpload(file, true, null);
					}
					return;
				}
				setStatus('Compressed to ' + mb(blob.size) + ' MB, uploading…');
				proceedUpload(blob, true, renameToJpg(file.name));
			});
		}

		// proceedUpload performs the actual POST. filename, when set, is sent as
		// the multipart part filename (used for compressed blobs that have none).
		function proceedUpload(file, isImage, filename) {
			if (isImage) {
				if (file.size > imgMaxBytes) {
					setDisabled(false);
					setStatus('File too large (max ' + imgMaxMB + ' MB)');
					return;
				}
			} else if (file.size > FILE_MAX) {
				setDisabled(false);
				setStatus('File too large (max 5 MB)');
				return;
			}
			setStatus('Uploading…');
			setDisabled(true);

			var fd = new FormData();
			if (filename) {
				fd.append('file', file, filename);
			} else {
				fd.append('file', file);
			}
			fd.append('csrf_token', getCsrfToken());
			var endpoint = isImage ? '/admin/upload' : '/admin/upload-file';

			fetch(endpoint, { method: 'POST', body: fd, credentials: 'same-origin' })
				.then(function(r) { return r.json(); })
				.then(function(data) {
					setDisabled(false);
					if (data.error) {
						setStatus('Error: ' + data.error);
						return;
					}
					onUploaded(data, isImage);
				})
				.catch(function() {
					setDisabled(false);
					setStatus('Upload failed');
				});
		}

		if (imgBtn && imgInput) {
			imgBtn.addEventListener('click', function() { imgInput.click(); });
			imgInput.addEventListener('change', function() {
				if (imgInput.files.length > 0) doUpload(imgInput.files[0], true);
				imgInput.value = '';
			});
		}
		if (fileBtn && fileInput) {
			fileBtn.addEventListener('click', function() { fileInput.click(); });
			fileInput.addEventListener('change', function() {
				if (fileInput.files.length > 0) doUpload(fileInput.files[0], false);
				fileInput.value = '';
			});
		}

		return { doUpload: doUpload, setStatus: setStatus };
	}

	window.GlimmerUpload = { init: init, getCsrfToken: getCsrfToken };
})(window);
