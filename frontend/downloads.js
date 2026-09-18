// Wails inyecta dos objetos globales:
//   window.go.main.App  -> los métodos exportados de App (app.go), como Promises
//   window.runtime      -> utilidades: eventos, diálogos, ventana...
const App = () => window.go.main.App;
const $ = s => document.querySelector(s);
const esc = s => String(s).replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
const fmtSize = b => b > 1e6 ? (b/1e6).toFixed(1) + ' MB' : Math.round(b/1e3) + ' kB';
const label = { running: 'descargando', done: 'terminado', failed: 'con errores', canceled: 'cancelado' };
const openJobs = new Set(); // ids con el log desplegado

let settings = {};

// ---- herramientas --------------------------------------------------------
function renderTools(t) {
  const box = $('#tools');
  box.className = 'tools ' + t.state;
  $('#toolsMsg').textContent = t.message;
  $('#toolsBar').hidden = t.state !== 'downloading';
  $('#toolsBar i').style.width = (t.progress || 0) + '%';
  $('#retryTools').hidden = t.state !== 'error';
  $('#updateYtdlp').hidden = t.state !== 'ready';
  $('#toolsVer').textContent = t.state === 'ready' && t.tools
    ? t.tools.map(x => `${x.name} ${x.version}`).join(' · ') : '';
  $('#go').disabled = t.state !== 'ready';
}
$('#retryTools').onclick = () => App().RetryTools();
$('#updateYtdlp').onclick = async () => {
  $('#updateYtdlp').disabled = true;
  $('#toolsMsg').textContent = 'Actualizando yt-dlp...';
  try { const msg = await App().UpdateYtdlp(); $('#toolsMsg').textContent = msg.split('\n').pop(); }
  catch (e) { $('#toolsMsg').textContent = 'Error: ' + e; }
  finally { $('#updateYtdlp').disabled = false; }
};

// ---- envío ---------------------------------------------------------------
async function submit() {
  $('#err').textContent = '';
  $('#go').disabled = true;
  try {
    const id = await App().Submit($('#urls').value, $('#format').value, $('#quality').value);
    openJobs.add(id);
    $('#urls').value = '';
  } catch (e) {
    $('#err').textContent = e;
  } finally {
    $('#go').disabled = false;
  }
}
$('#go').onclick = submit;
$('#urls').addEventListener('keydown', e => { if (e.ctrlKey && e.key === 'Enter') submit(); });

// ---- ajustes -------------------------------------------------------------
function renderSettings() {
  $('#outDirLabel').textContent = settings.outDir;
  $('#outDirLabel').title = settings.outDir;
  $('#workers').value = settings.workers;
  $('#format').value = settings.format;
  $('#quality').value = settings.quality;
  $('#discogsToken').value = settings.discogsToken || '';
  $('#folderIcons').checked = settings.folderIcons !== false;
}
async function saveSettings(patch) {
  const next = { ...settings, ...patch };
  try {
    await App().SaveSettings(next);
    settings = next;
    $('#settingsMsg').textContent = 'guardado';
    setTimeout(() => $('#settingsMsg').textContent = '', 1500);
    refreshFiles();
  } catch (e) {
    $('#settingsMsg').textContent = 'Error: ' + e;
  }
}
$('#toggleSettings').onclick = () => {
  const s = $('#settings'); s.hidden = !s.hidden;
  $('#toggleSettings').textContent = s.hidden ? 'mostrar' : 'ocultar';
};
$('#chooseDir').onclick = async () => {
  $('#err').textContent = '';
  try {
    const dir = await App().ChooseFolder();
    if (dir) { settings.outDir = dir; renderSettings(); saveSettings({ outDir: dir }); }
  } catch (e) { $('#err').textContent = 'No pude abrir el diálogo: ' + e; }
};
$('#openDir').onclick = $('#openDir2').onclick = async () => {
  try { await App().OpenFolder(); } catch (e) { $('#err').textContent = e; }
};
$('#workers').onchange = e => saveSettings({ workers: Number(e.target.value) });
$('#format').onchange = e => saveSettings({ format: e.target.value });
$('#quality').onchange = e => saveSettings({ quality: e.target.value });

// ---- trabajos ------------------------------------------------------------
window.cancelJob = async id => { await App().Cancel(id); };
window.toggleLog = id => { openJobs.has(id) ? openJobs.delete(id) : openJobs.add(id); refreshJobs(); };

function lastPercent(lines) {
  for (let i = lines.length - 1; i >= Math.max(0, lines.length - 20); i--) {
    const m = lines[i].match(/(\d{1,3}\.\d)%/);
    if (m) return m[1] + '%';
  }
  return '';
}

function renderJobs(jobs) {
  const box = $('#jobs');
  if (!jobs.length) { box.innerHTML = '<p class="empty">Nada por ahora.</p>'; return; }
  box.innerHTML = jobs.map(j => {
    const res = j.status === 'running' ? '' : ` · ${j.ok} ok${j.failed ? `, ${j.failed} fallidas` : ''}`;
    const pct = j.status === 'running' ? lastPercent(j.lines) : '';
    return `
    <div class="job ${j.status} ${openJobs.has(j.id) ? 'open' : ''}">
      <header>
        <span class="dot"></span>
        <span class="urls" title="${esc(j.urls.join('\n'))}">${esc(j.urls.join('  '))}</span>
        <span class="pct">${pct}</span>
        <span class="meta">${label[j.status]} · ${j.format}${res}</span>
        <button class="ghost" onclick="toggleLog(${j.id})">log</button>
        ${j.status === 'running' ? `<button class="ghost" onclick="cancelJob(${j.id})">cancelar</button>` : ''}
      </header>
      <div class="log">${esc(j.lines.slice(-80).join('\n')) || '…'}</div>
    </div>`;
  }).join('');
  box.querySelectorAll('.job.open .log').forEach(el => el.scrollTop = el.scrollHeight);
}

function renderFiles(files) {
  const ul = $('#files');
  if (!files || !files.length) { ul.innerHTML = '<li class="empty">Carpeta vacía.</li>'; return; }
  ul.innerHTML = files.map(f =>
    `<li><span>${esc(f.path)}</span><span class="size">${fmtSize(f.size)}</span></li>`).join('');
}

async function refreshJobs() { renderJobs(await App().Jobs()); }
async function refreshFiles() { try { renderFiles(await App().Files()); } catch (e) { renderFiles([]); } }

// Go emite "jobs" en cada línea de log; agrupamos las ráfagas para no
// redibujar cien veces por segundo.
let pending = null;
function scheduleRefresh() {
  if (pending) return;
  pending = setTimeout(async () => { pending = null; await refreshJobs(); refreshFiles(); }, 250);
}

// ---- arranque ------------------------------------------------------------
(async () => {
  const st = await App().State();
  settings = st.settings;
  $("#appVer").textContent = st.version && st.version !== "dev" ? "v" + st.version : "";
  renderSettings();
  renderTools(st.tools);
  refreshJobs();
  refreshFiles();
  window.runtime.EventsOn('tools', renderTools);
  window.runtime.EventsOn('jobs', scheduleRefresh);
})();

// ---- pestañas ------------------------------------------------------------
document.querySelectorAll('.tabs button').forEach(b => b.onclick = () => {
  document.querySelectorAll('.tabs button').forEach(x => x.classList.toggle('active', x === b));
  document.querySelectorAll('.tab').forEach(t => t.classList.toggle('active', t.id === 'tab-' + b.dataset.tab));
  if (b.dataset.tab === 'library' && window.libOnShow) window.libOnShow();
  if (b.dataset.tab === 'soulseek' && window.slskOnShow) window.slskOnShow();
  document.body.classList.toggle('wide', b.dataset.tab === 'player'); // el reproductor aprovecha el ancho
  if (b.dataset.tab === 'player' && window.plOnShow) window.plOnShow();
});

// ---- token de Discogs (Biblioteca) ---------------------------------------
$('#saveDiscogs').onclick = () => saveSettings({ discogsToken: $('#discogsToken').value.trim() });
$('#folderIcons').onchange = () => saveSettings({ folderIcons: $('#folderIcons').checked });

// Cualquier error de JavaScript que se escape acaba en el registro de la
// Biblioteca en vez de perderse en silencio.
window.addEventListener('error', e => { if (window.libLog) libLog('error: ' + (e.message || e.error)); });
window.addEventListener('unhandledrejection', e => { if (window.libLog) libLog('error: ' + (e.reason && e.reason.stack ? e.reason.stack : e.reason)); });
