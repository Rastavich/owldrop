package main

// dropPageHTML is the public upload page served at /drop/<token>. The URL
// token is embedded in the page's own origin; nothing else is. Mirrors the
// desktop app's embedded page (drops.go).
const dropPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Send a file</title>
<style>
:root{--bg:#0b0e14;--panel:#12161f;--panel2:#181e2c;--border:#232b3d;--text:#e8ebf3;--muted:#8a93a8;--accent:#5f6cff;--green:#34d399;--red:#f87171}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;min-height:100vh;display:flex;flex-direction:column;align-items:center;justify-content:center;padding:24px}
.card{width:100%;max-width:460px;background:var(--panel);border:1px solid var(--border);border-radius:14px;padding:26px;text-align:center}
h1{margin:0 0 6px;font-size:20px}
.meta{color:var(--muted);font-size:13px;margin-bottom:22px}
.drop{border:1.5px dashed #2c3550;border-radius:12px;padding:36px 18px;cursor:pointer;transition:border-color .15s,background .15s}
.drop.over{border-color:var(--accent);background:rgba(95,108,255,.08)}
.drop p{margin:0;color:var(--muted)}
.drop .t{color:var(--text);font-weight:600;margin-bottom:4px}
#status{margin-top:18px;font-size:13.5px;min-height:22px}
#status.ok{color:var(--green)}
#status.err{color:var(--red)}
.bar{height:6px;border-radius:99px;background:var(--panel2);overflow:hidden;margin-top:10px;display:none}
.bar.on{display:block}
.bar .fill{height:100%;width:0;background:linear-gradient(90deg,var(--accent),#8b5cf6);transition:width .15s}
</style>
</head>
<body>
<div class="card">
  <h1>Send a file</h1>
  <p class="meta" id="meta">to <b>__NAME__</b> · link expires __EXPIRES__</p>
  <div class="drop" id="drop">
    <p class="t">Drop files or a folder here</p>
    <p>or click to choose</p>
  </div>
  <input type="file" id="file" multiple hidden>
  <div class="bar" id="bar"><div class="fill" id="fill"></div></div>
  <div id="status"></div>
</div>
<script>
const token = location.pathname.split('/')[2];
const drop = document.getElementById('drop');
const status = document.getElementById('status');
const bar = document.getElementById('bar');
const fill = document.getElementById('fill');
function setStatus(msg, kind) { status.textContent = msg; status.className = kind || ''; }
drop.addEventListener('click', () => document.getElementById('file').click());
document.getElementById('file').addEventListener('change', e => { if (e.target.files.length) send(Array.from(e.target.files)); e.target.value = ''; });
drop.addEventListener('dragover', e => { e.preventDefault(); drop.classList.add('over'); });
drop.addEventListener('dragleave', () => drop.classList.remove('over'));
drop.addEventListener('drop', e => { e.preventDefault(); drop.classList.remove('over'); if (e.dataTransfer.files.length) send(Array.from(e.dataTransfer.files)); });
async function send(files) {
  if (!files.length) return;
  const fd = new FormData();
  for (const file of files) {
    fd.append('file', file, file.name);
    fd.append('path', file.webkitRelativePath || file.name);
  }
  const first = files[0];
  const what = files.length > 1 ? files.length + ' files'
    : (first.webkitRelativePath ? first.webkitRelativePath.split('/')[0] + ' folder' : first.name);
  setStatus('Uploading ' + what + '…');
  bar.classList.add('on');
  fill.style.width = '0';
  let data = null;
  try {
    await new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/drop/' + token + '/upload');
      xhr.upload.onprogress = e => { if (e.lengthComputable) fill.style.width = Math.round(e.loaded * 100 / e.total) + '%'; };
      xhr.onload = () => { if (xhr.status === 200) { try { data = JSON.parse(xhr.responseText); } catch {} resolve(); } else { try { reject(new Error(JSON.parse(xhr.responseText).error || 'upload failed')); } catch { reject(new Error('upload failed (' + xhr.status + ')')); } } };
      xhr.onerror = () => reject(new Error('network error'));
      xhr.send(fd);
    });
    bar.classList.remove('on');
    const n = data && data.names ? data.names.length : 1;
    setStatus('Sent! ' + (n > 1 ? n + ' files arrived' : what + ' arrived') + '. You can close this page.', 'ok');
  } catch (e) {
    bar.classList.remove('on');
    setStatus(e.message, 'err');
  }
}
</script>
</body>
</html>`

// dropPaywallHTML is served when the link's owner has no active Premium
// subscription. Billing happens in the app, so the page carries no links.
const dropPaywallHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Drop link paused</title>
<body style="margin:0;background:#0b0e14;color:#e8ebf3;font-family:system-ui;display:grid;place-items:center;height:100vh">
<main style="max-width:26rem;text-align:center;padding:2rem">
<p style="font-size:15px;line-height:1.6">This drop link is paused.</p>
<p style="font-size:13px;color:#8b93a8;line-height:1.6">Public drop links are a Premium feature. The owner can enable them from the app's Settings.</p>
</main>`
