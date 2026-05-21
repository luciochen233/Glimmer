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
				if (file.size > IMG_MAX) {
					setStatus('File too large (max 50 MB)');
					return;
				}
			} else {
				if (file.size > FILE_MAX) {
					setStatus('File too large (max 5 MB)');
					return;
				}
			}
			setStatus('Uploading…');
			setDisabled(true);

			var fd = new FormData();
			fd.append('file', file);
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
