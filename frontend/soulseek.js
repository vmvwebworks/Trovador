// Pestaña Soulseek: la interfaz sobre el motor slskd que gestiona Go.
// Usa App, $, esc, fmtSize (downloads.js) y fmtDur (library.js).

const slsk = { state: null, searchId: '', poll: null, results: null, tpoll: null };

// ---- estado y cabecera ---------------------------------------------------
async function slskRefreshState() {
  try { slsk.state = await App().SoulseekState(); } catch (e) { $('#slskErr').textContent = e; return; }
  renderSlskState();
}

function renderSlskState() {
  const s = slsk.state;
  const box = $('#slskStatus');
  box.className = 'tools ' + (s.connected ? 'ready' : s.installed ? '' : 'error');
  $('#slskMsg').textContent = s.message;
  $('#slskInstall').hidden = s.installed;
  $('#slskConnect').hidden = !s.installed || s.connected;
  $('#slskDisconnect').hidden = !s.running;
  $('#slskQuery').disabled = $('#slskSearch').disabled = !s.connected;
  $('#slskUser').value = $('#slskUser').value || s.user;
  $('#slskShare').textContent = $('#slskShare').title = s.share;
  $('#slskPort').value = $('#slskPort').value || s.port;
  if (!s.user) $('#slskSettings').hidden = false; // primera vez: enseñar los ajustes
}

window.slskOnShow = () => { slskRefreshState(); slskStartTransfersPoll(); };
window.runtime.EventsOn('slsk', slskRefreshState);
window.runtime.EventsOn('slsk-progress', p => {
  $('#slskProgress').hidden = false;
  $('#slskProgress i').style.width = (p.pct || 0) + '%';
  if (p.name === 'compartidos') {
    $('#slskProgressMsg').textContent = p.done ? 'compartidos leídos' : `leyendo la carpeta compartida (primera vez: varios minutos): ${Math.round(p.pct)}%`;
    if (p.done) setTimeout(() => $('#slskProgress').hidden = true, 1500);
  } else {
    $('#slskProgressMsg').textContent = `descargando ${p.name}: ${fmtSize(p.done)}${p.total > 0 ? ' de ' + fmtSize(p.total) : ''}`;
  }
});

$('#slskSettingsBtn').onclick = () => $('#slskSettings').hidden = !$('#slskSettings').hidden;
$('#slskShareBtn').onclick = async () => {
  try { const d = await App().ChooseShareFolder(); if (d) { $('#slskShare').textContent = $('#slskShare').title = d; } }
  catch (e) { $('#slskErr').textContent = e; }
};
$('#slskSave').onclick = async () => {
  $('#slskErr').textContent = '';
  try {
    await App().SaveSoulseekSettings($('#slskUser').value, $('#slskPass').value, $('#slskShare').title, Number($('#slskPort').value) || 0);
    $('#slskSettings').hidden = true;
    slskRefreshState();
  } catch (e) { $('#slskErr').textContent = e; }
};
$('#slskInstall').onclick = async () => {
  $('#slskErr').textContent = '';
  $('#slskInstall').disabled = true;
  try { await App().SoulseekInstall(); }
  catch (e) { $('#slskErr').textContent = e; }
  finally { $('#slskInstall').disabled = false; $('#slskProgress').hidden = true; slskRefreshState(); }
};
$('#slskConnect').onclick = async () => {
  $('#slskErr').textContent = '';
  $('#slskConnect').disabled = true; $('#slskMsg').textContent = 'arrancando el motor y entrando en Soulseek… (la primera vez lee toda la carpeta compartida)';
  try { slsk.state = await App().SoulseekConnect(); renderSlskState(); }
  catch (e) { $('#slskErr').textContent = e; slskRefreshState(); }
  finally { $('#slskConnect').disabled = false; }
};
$('#slskDisconnect').onclick = async () => { slsk.state = await App().SoulseekDisconnect(); renderSlskState(); };

// ---- búsqueda ------------------------------------------------------------
$('#slskSearch').onclick = slskSearch;
$('#slskQuery').addEventListener('keydown', e => { if (e.key === 'Enter') slskSearch(); });
$('#slskFilter').onchange = () => slsk.results && renderSlskResults(slsk.results);

async function slskSearch() {
  $('#slskErr').textContent = '';
  clearInterval(slsk.poll);
  $('#slskResults').innerHTML = '';
  $('#slskSearchMeta').textContent = 'buscando…';
  try { slsk.searchId = await App().SoulseekSearch($('#slskQuery').value); }
  catch (e) { $('#slskErr').textContent = e; $('#slskSearchMeta').textContent = ''; return; }
  // Los resultados van llegando durante ~20 s: se repinta cada 2 s hasta que
  // slskd da la búsqueda por terminada.
  let ticks = 0;
  const tick = async () => {
    try {
      const r = await App().SoulseekResults(slsk.searchId);
      r.responses = (r.responses || []).map(u => ({ ...u, files: u.files || [] }));
      slsk.results = r;
      $('#slskSearchMeta').textContent = `${r.responses.length} usuario(s) · ${r.files} fichero(s)${r.complete ? '' : ' · buscando…'}`;
      renderSlskResults(r);
      if (r.complete || ++ticks > 30) clearInterval(slsk.poll);
    } catch (e) { $('#slskErr').textContent = e; clearInterval(slsk.poll); }
  };
  slsk.poll = setInterval(tick, 2000);
  setTimeout(tick, 800);
}

const isAudio = n => /\.(mp3|flac|m4a|ogg|opus|wav|wv|ape|aac)$/i.test(n);

// Cada usuario es un bloque; dentro, sus ficheros agrupados por carpeta
// remota (= disco), con "descargar carpeta" y descarga por fichero.
function renderSlskResults(r) {
  const filter = $('#slskFilter').value;
  const keepFile = f => {
    if (!isAudio(f.name) || f.locked) return false;
    if (filter === 'flac') return /\.flac$/i.test(f.name);
    if (filter === '320') return /\.mp3$/i.test(f.name) && f.bitRate >= 320;
    return true;
  };
  const speed = b => b >= 1e6 ? (b / 1e6).toFixed(1) + ' MB/s' : Math.round(b / 1e3) + ' kB/s';
  let html = '';
  for (const u of r.responses) {
    const files = u.files.filter(keepFile);
    if (!files.length) continue;
    const byFolder = new Map();
    files.forEach(f => { if (!byFolder.has(f.folder)) byFolder.set(f.folder, []); byFolder.get(f.folder).push(f); });
    if (filter === 'album' && ![...byFolder.values()].some(l => l.length >= 3)) continue;
    const slot = u.freeSlot ? '<span class="slot free">hueco libre</span>' : `<span class="slot busy">cola ${u.queueLength}</span>`;
    html += `<div class="suser"><div class="uhead"><b>${esc(u.username)}</b> ${slot} <span class="dim">${speed(u.uploadSpeed)}</span> <span class="dim">· ${files.length} fichero(s)</span></div>`;
    for (const [folder, list] of byFolder) {
      if (filter === 'album' && list.length < 3) continue;
      const total = list.reduce((a, f) => a + f.size, 0);
      const br = [...new Set(list.map(f => f.bitRate).filter(Boolean))].sort((a, b) => a - b);
      const idx = list.map(f => u.files.indexOf(f));
      html += `<div class="sfolder"><div class="fhead">📁 <span title="${esc(folder)}">${esc(folder || '(raíz)')}</span> · ${list.length} · ${fmtSize(total)}${br.length ? ' · ' + br.join('/') + 'k' : ''}
        <button class="ghost dlf" data-user="${esc(u.username)}" data-idx="${idx.join(',')}">descargar carpeta</button></div>
        <table>${list.map(f => `<tr><td class="text">${esc(f.name)}</td><td class="num">${f.length ? fmtDur(f.length) : ''}</td><td class="num dim">${f.bitRate ? f.bitRate + 'k' : ''}</td><td class="num dim">${fmtSize(f.size)}</td>
          <td><button class="ghost dl1" data-user="${esc(u.username)}" data-idx="${u.files.indexOf(f)}" title="descargar este fichero">↓</button></td></tr>`).join('')}</table></div>`;
    }
    html += '</div>';
  }
  $('#slskResults').innerHTML = html || '<p class="empty">Sin resultados (con este filtro).</p>';
  $('#slskResults').querySelectorAll('button.dlf, button.dl1').forEach(b => b.onclick = () => slskDownload(b));
}

async function slskDownload(btn) {
  const u = slsk.results.responses.find(x => x.username === btn.dataset.user);
  if (!u) return;
  const files = btn.dataset.idx.split(',').map(Number).map(i => u.files[i]).filter(Boolean);
  btn.disabled = true; const old = btn.textContent; btn.textContent = '…';
  try { await App().SoulseekDownload(u.username, files); btn.textContent = files.length > 1 ? 'pedida' : '✓'; slskRefreshTransfers(); }
  catch (e) { $('#slskErr').textContent = e; btn.disabled = false; btn.textContent = old; }
}

// ---- transferencias ------------------------------------------------------
function slskStartTransfersPoll() {
  clearInterval(slsk.tpoll);
  slskRefreshTransfers();
  slsk.tpoll = setInterval(() => { if (document.querySelector('#tab-soulseek').classList.contains('active')) slskRefreshTransfers(); }, 2000);
}

async function slskRefreshTransfers() {
  if (!slsk.state || !slsk.state.running) { $('#slskTransfers').innerHTML = '<p class="empty">Ninguna descarga.</p>'; return; }
  let list;
  try { list = await App().SoulseekTransfers() || []; } catch (e) { return; }
  const active = list.filter(t => !t.state.startsWith('Completed')).length;
  $('#slskTransfersMeta').textContent = list.length ? `${active} activa(s) · ${list.length} en total` : '';
  if (!list.length) { $('#slskTransfers').innerHTML = '<p class="empty">Ninguna descarga.</p>'; return; }
  const cls = s => s.includes('Succeeded') ? 'ok' : s.includes('Errored') || s.includes('Cancelled') || s.includes('Rejected') ? 'bad' : s.includes('InProgress') ? 'warn' : 'dim';
  const label = s => s.includes('Succeeded') ? 'terminada' : s.includes('InProgress') ? 'bajando' : s.includes('Queued') ? (s.includes('Remotely') ? 'en cola (del otro)' : 'en cola') : s.includes('Errored') ? 'error' : s.includes('Cancelled') ? 'cancelada' : s.includes('Rejected') ? 'rechazada' : s;
  $('#slskTransfers').innerHTML = `<table><thead><tr><th>Fichero</th><th>Carpeta</th><th>Usuario</th><th class="num">Tamaño</th><th>Estado</th><th></th></tr></thead><tbody>` +
    list.map(t => `<tr>
      <td class="text" title="${esc(t.error || '')}">${esc(t.name)}</td><td class="dim">${esc(t.folder)}</td><td class="dim">${esc(t.username)}</td>
      <td class="num">${fmtSize(t.size)}</td>
      <td><span class="${cls(t.state)}">${label(t.state)}</span>${t.state.includes('InProgress') ? ` <span class="num">${Math.round(t.percent)}% · ${Math.round(t.speed / 1e3)} kB/s</span>` : ''}${t.error ? ` <span class="bad" title="${esc(t.error)}">!</span>` : ''}</td>
      <td>${t.state.startsWith('Completed') ? '' : `<button class="ghost tcancel" data-user="${esc(t.username)}" data-id="${esc(t.id)}">cancelar</button>`}</td>
    </tr>`).join('') + '</tbody></table>';
  $('#slskTransfers').querySelectorAll('button.tcancel').forEach(b => b.onclick = async () => {
    try { await App().SoulseekCancel(b.dataset.user, b.dataset.id); slskRefreshTransfers(); } catch (e) { $('#slskErr').textContent = e; }
  });
}
$('#slskClearDone').onclick = async () => { try { await App().SoulseekClearDone(); slskRefreshTransfers(); } catch (e) { $('#slskErr').textContent = e; } };
$('#slskOpenDl').onclick = () => App().OpenFolder().catch(e => $('#slskErr').textContent = e);
