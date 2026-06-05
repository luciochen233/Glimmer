// Shared upload helper for the admin uploads page and the paste editor.
// Wires up an "Upload Image" and "Upload File" button pair to their hidden
// file inputs and posts to /admin/upload (images, 50 MB) or
// /admin/upload-file (any file, 5 MB).
//
// Usage: call window.GlimmerUpload.init({
//   imgBtn, fileBtn, imgInput, fileInput, statusEl,
//   onUploaded: function(data, isImage) { ... }     // required
// });

(function(window) {
	var IMG_MAX = 50 * 1024 * 1024;
	var FILE_MAX = 5 * 1024 * 1024;
	// Images larger than this offer in-browser compression before upload.
	var COMPRESS_THRESHOLD = 10 * 1024 * 1024;
	// Downscale so the longest edge is at most this many pixels, then re-encode
	// as JPEG at this quality. Done entirely client-side via <canvas>.
	var COMPRESS_MAX_DIMENSION = 3000;
	var COMPRESS_QUALITY = 0.85;

	// renameToJpg replaces a filename's extension with .jpg (compression always
	// outputs JPEG). Falls back to "image.jpg" if there's no usable base name.
	function renameToJpg(name) {
		if (!name) return 'image.jpg';
		var dot = name.lastIndexOf('.');
		var base = dot > 0 ? name.slice(0, dot) : name;
		return base + '.jpg';
	}

	// compressImage downscales + re-encodes an image File to a JPEG Blob using a
	// canvas. cb(blob, err): on any failure cb is called with a null blob and an
	// error string so the caller can fall back to uploading the original.
	function compressImage(file, cb) {
		if (!window.URL || !window.URL.createObjectURL) { cb(null, 'no object URL support'); return; }
		var url = window.URL.createObjectURL(file);
		var img = new Image();
		img.onload = function() {
			var w = img.naturalWidth || img.width;
			var h = img.naturalHeight || img.height;
			window.URL.revokeObjectURL(url);
			if (!w || !h) { cb(null, 'bad image dimensions'); return; }
			var scale = Math.min(1, COMPRESS_MAX_DIMENSION / Math.max(w, h));
			var cw = Math.max(1, Math.round(w * scale));
			var ch = Math.max(1, Math.round(h * scale));
			var canvas = document.createElement('canvas');
			canvas.width = cw;
			canvas.height = ch;
			var ctx = canvas.getContext('2d');
			if (!ctx) { cb(null, 'no canvas context'); return; }
			ctx.drawImage(img, 0, 0, cw, ch);
			if (!canvas.toBlob) { cb(null, 'no toBlob support'); return; }
			canvas.toBlob(function(blob) {
				cb(blob, blob ? null : 'compression produced no data');
			}, 'image/jpeg', COMPRESS_QUALITY);
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
				// Offer in-browser compression for large images before the size
				// guard, so a >50 MB photo can still be uploaded once shrunk.
				if (file.size > COMPRESS_THRESHOLD) {
					var mb = (file.size / (1024 * 1024)).toFixed(1);
					var ok = window.confirm('This image is ' + mb + ' MB. Compress it in your browser before uploading?\n\nRecommended — this resizes and re-encodes it as JPEG for a much faster upload. Cancel to upload the original.');
					if (ok) {
						setStatus('Compressing…');
						setDisabled(true);
						compressImage(file, function(blob, err) {
							if (err || !blob) {
								// Compression failed — fall back to the original.
								proceedUpload(file, true, null);
								return;
							}
							setStatus('Compressed ' + mb + ' MB → ' + (blob.size / (1024 * 1024)).toFixed(1) + ' MB');
							proceedUpload(blob, true, renameToJpg(file.name));
						});
						return;
					}
				}
			}
			proceedUpload(file, isImage, null);
		}

		// proceedUpload performs the actual POST. filename, when set, is sent as
		// the multipart part filename (used for compressed blobs that have none).
		function proceedUpload(file, isImage, filename) {
			if (isImage) {
				if (file.size > IMG_MAX) {
					setDisabled(false);
					setStatus('File too large (max 50 MB)');
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
