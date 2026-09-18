// Pestaña Biblioteca: detectar los discos de una carpeta, analizarlos todos
// contra MusicBrainz y aplicar arreglos en lote; el detalle de un disco
// permite elegir otra edición a mano.
// Usa App, $, esc definidos en downloads.js (los scripts clásicos comparten
// el ámbito global).

const lib = {
  root: '',        // carpeta raíz con los discos
  albums: [],      // filas de la tabla (albumInfo)
  running: false,  // hay análisis/aplicación en marcha
  expanded: new Set(), // discos con las candidatas desplegadas
  view: 'grid',    // 'grid' (miniaturas) o 'list'
  cards: {},       // dir -> {cover, artist, album} (caché de la cuadrícula)
  dir: '',         // disco abierto en el detalle
  scan: null,      // resultado de ScanFolder
  release: null,   // mbReleaseDetail seleccionado
  tolerance: 3,    // segundos de diferencia que damos por buenos
};

const fmtDur = s => { s = Math.round(s || 0); return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`; };
const libLog = line => {
  const el = $('#libLog');
  el.textContent += (el.textContent ? '\n' : '') + line;
  el.scrollTop = el.scrollHeight;
};
window.runtime.EventsOn('lib', libLog);
$('#libClearLog').onclick = () => $('#libLog').textContent = '';

const stateLabel = {
  pending: 'sin analizar', analyzing: 'analizando…', working: 'aplicando…',
  ok: 'íntegro', splittable: 'separable', partial: 'parcial', extra: 'con extras', single: 'pista suelta',
  mismatch: 'no cuadra', notfound: 'no encontrado', error: 'error',
};
const FITS = new Set(['ok', 'splittable', 'partial', 'extra']);
const IDENTIFIED = new Set([...FITS, 'single']);

// ---- carpeta raíz y tabla de discos --------------------------------------
window.libOnShow = async () => {
  if (!lib.root) { lib.root = settings.outDir; await loadAlbums(); }
};

$('#libChoose').onclick = async () => {
  $('#libBatchErr').textContent = '';
  try {
    const d = await App().ChooseLibraryFolder();
    if (d) { lib.root = d; closeDetail(); await loadAlbums(); }
  } catch (e) { $('#libBatchErr').textContent = 'No pude abrir el diálogo: ' + e; }
};
$('#libRefresh').onclick = loadAlbums;

async function loadAlbums() {
  $('#libRoot').textContent = $('#libRoot').title = lib.root;
  $('#libBatchErr').textContent = $('#libListErr').textContent = '';
  const box = $('#libAlbums');
  if (!box.querySelector('.grid, table')) box.innerHTML = `<p class="empty">Leyendo ${esc(lib.root)}…</p>`;
  const t0 = Date.now();
  try { lib.albums = await App().ListAlbums(lib.root) || []; }
  catch (e) { lib.albums = []; $('#libListErr').textContent = 'No pude leer la carpeta: ' + e; libLog('Biblioteca: ' + e); }
  try { renderAlbums(); }
  catch (e) { $('#libListErr').textContent = 'Error al pintar la lista: ' + e; libLog('Biblioteca: error al pintar: ' + (e.stack || e)); }
  if (Date.now() - t0 > 3000) libLog(`Biblioteca: ${lib.albums.length} discos en ${((Date.now() - t0) / 1000).toFixed(1)} s`);
}

// Conmutador de vista; se recuerda entre sesiones.
try { lib.view = localStorage.getItem('libView') || 'grid'; } catch (e) {}
$('#libView').querySelectorAll('button').forEach(b => b.onclick = () => {
  lib.view = b.dataset.view;
  try { localStorage.setItem('libView', lib.view); } catch (e) {}
  renderAlbums();
});

function renderAlbums() {
  const box = $('#libAlbums');
  $('#libView').querySelectorAll('button').forEach(b => b.classList.toggle('active', b.dataset.view === lib.view));
  $('#libAnalyze').disabled = !lib.albums.length || lib.running;
  $('#libApply').hidden = !lib.albums.some(a => a.release);
  if (!lib.albums.length) { box.innerHTML = '<p class="empty">No hay discos en esta carpeta (busco subcarpetas con audio).</p>'; return; }
  if (lib.view === 'grid') { renderGrid(box); return; }

  const checked = new Set([...box.querySelectorAll('input.sel:checked')].map(i => i.dataset.dir));
  // Conservar lo escrito en las cajas de URL al redibujar.
  const typed = {};
  box.querySelectorAll('tr.cands').forEach(tr => { const i = tr.querySelector('input.mbid'); if (i && i.value) typed[tr.dataset.dir] = i.value; });
  const firstRender = !box.querySelector('table') && lib.albums.length <= 50;
  const relLabel = r => `${srcTag(r.source)} <b>${esc(r.artist)} — ${esc(r.title)}</b> (${esc((r.date || '').slice(0, 4))}${r.country ? ', ' + esc(r.country) : ''}${r.format ? ', ' + esc(r.format) : ''})`;

  box.innerHTML = `<table><thead><tr>
      <th><input type="checkbox" id="selAll" title="marcar todos"></th>
      <th>Disco</th><th class="num">Ficheros</th><th class="num">Duración</th><th>Carátula</th><th>Estado</th><th>Edición</th>
    </tr></thead><tbody>${lib.albums.map(a => {
      const proposal = a.release && !a.confirmed;
      const stateCell = `<span class="state ${a.state}">${stateLabel[a.state] || a.state}</span>` +
        (a.confirmed ? ' <span class="ok" title="edición confirmada">✓</span>' : proposal ? ' <span class="dim">(propuesta)</span>' : '');
      const editionCell = (a.release ? relLabel(a.release) + '<br>' : '') + esc(a.message || '') +
        (a.candidates && a.candidates.length || a.state === 'notfound' ? `<br><span class="links">` +
          (proposal ? `<button class="ghost act" data-act="confirm">Confirmar</button> ` : '') +
          (a.candidates && a.candidates.length ? `<button class="ghost act" data-act="cands">${lib.expanded.has(a.dir) ? 'ocultar' : 'ver'} ${a.candidates.length} candidata(s)</button> ` : '') +
          `<button class="ghost act" data-act="web" title="Abrir en el navegador">navegador</button></span>` : '');
      const cands = lib.expanded.has(a.dir) ? `<tr class="cands" data-dir="${esc(a.dir)}"><td></td><td colspan="6">${renderCandidates(a)}</td></tr>` : '';
      return `<tr data-dir="${esc(a.dir)}">
        <td><input type="checkbox" class="sel" data-dir="${esc(a.dir)}" ${firstRender || checked.has(a.dir) ? 'checked' : ''}></td>
        <td class="name" title="abrir detalle">${esc(a.name)}</td>
        <td class="num">${a.files}</td>
        <td class="num">${a.total ? fmtDur(a.total) : ''}</td>
        <td class="${a.hasCover ? 'ok' : 'dim'}">${a.hasCover ? 'sí' : 'no'}</td>
        <td>${stateCell}</td>
        <td class="msg">${editionCell}</td>
      </tr>${cands}`;
    }).join('')}</tbody></table>`;

  box.querySelectorAll('td.name').forEach(td => td.onclick = () => openDetail(td.parentElement.dataset.dir));
  box.querySelectorAll('button.act').forEach(b => b.onclick = () => rowAction(b.closest('tr').dataset.dir, b.dataset.act, b));
  box.querySelectorAll('tr.cands').forEach(tr => { const i = tr.querySelector('input.mbid'); if (i && typed[tr.dataset.dir]) i.value = typed[tr.dataset.dir]; });
  $('#selAll').onclick = e => box.querySelectorAll('input.sel').forEach(i => i.checked = e.target.checked);
  updateApplyHint();
}

// Vista de miniaturas: una tarjeta por disco con su carátula. Las carátulas
// se piden a Go después de pintar (extraerlas cuesta un ffmpeg cada una) y
// se guardan en lib.cards para no repetirlo.
function renderGrid(box) {
  const checked = new Set([...box.querySelectorAll('input.sel:checked')].map(i => i.dataset.dir));
  const firstRender = !box.querySelector('.grid') && lib.albums.length <= 50;
  box.innerHTML = `<div class="grid">${lib.albums.map(a => {
    const card = lib.cards[a.dir];
    const cover = card ? card.cover : undefined;
    const state = a.state && a.state !== 'pending' ? `<span class="badge ${a.state}">${stateLabel[a.state] || a.state}${a.confirmed ? ' ✓' : ''}</span>` : '';
    return `<div class="card" data-dir="${esc(a.dir)}" title="${esc(a.message || a.name)}">
      <input type="checkbox" class="sel" data-dir="${esc(a.dir)}" ${firstRender || checked.has(a.dir) ? 'checked' : ''}>
      ${cover ? `<img class="thumb" src="${cover}" alt="">` : `<div class="thumb none">${cover === '' ? '♪' : '…'}</div>`}
      ${state}
      <div class="title">${cardTitle(a, card)}</div>
      <div class="sub">${cardSub(a, card)}</div>
    </div>`;
  }).join('')}</div>`;
  box.querySelectorAll('.card').forEach(c => c.onclick = e => { if (e.target.tagName !== 'INPUT') openDetail(c.dataset.dir); });
  updateApplyHint();
  loadCards();
}

// Título de la tarjeta: la edición confirmada/propuesta si la hay; si no, lo
// que se deduce de etiquetas y nombre de carpeta ("Frost — Under the
// Hungarian Blackmoon"); y si aún no se ha leído, el nombre de la carpeta.
function cardTitle(a, card) {
  if (a.name === '(ficheros sueltos)') return 'Ficheros sueltos'; // el audio que cuelga de la propia carpeta raíz
  if (a.release) return `${esc(a.release.artist)} — ${esc(a.release.title)}`;
  if (card && (card.artist || card.album)) return (card.artist ? esc(card.artist) + ' — ' : '') + esc(card.album || a.name);
  return esc(a.name);
}
function cardSub(a, card) {
  const n = `${a.files} pista(s)${a.total ? ' · ' + fmtDur(a.total) : ''}`;
  // Si el título ya no es el nombre de la carpeta, lo enseñamos debajo en pequeño.
  const showFolder = a.release || (card && (card.artist || card.album));
  return showFolder ? `<span title="${esc(a.name)}">${esc(a.name)}</span> · ${n}` : n;
}

let cardsLoading = false;
async function loadCards() {
  if (cardsLoading) return;
  cardsLoading = true;
  try {
    for (const a of lib.albums) {
      if (lib.cards[a.dir] !== undefined) continue;
      let card;
      try { card = await App().AlbumCard(a.dir); } catch (e) { card = { cover: '', artist: '', album: '' }; }
      lib.cards[a.dir] = card;
      const el = $('#libAlbums').querySelector(`.card[data-dir="${CSS.escape(a.dir)}"]`);
      if (!el) continue;
      const thumb = card.cover ? Object.assign(document.createElement('img'), { className: 'thumb', src: card.cover, alt: '' })
                               : Object.assign(document.createElement('div'), { className: 'thumb none', textContent: '♪' });
      el.querySelector('.thumb').replaceWith(thumb);
      el.querySelector('.title').innerHTML = cardTitle(a, card);
      el.querySelector('.sub').innerHTML = cardSub(a, card);
    }
  } finally { cardsLoading = false; }
}

// Sub-tabla con todo lo que encontró la búsqueda para un disco.
function renderCandidates(a) {
  const evalLabel = c => !c.evaluated ? '<span class="dim">sin evaluar</span>'
    : c.state === 'ok' ? '<span class="ok">íntegro</span>'
    : c.state === 'splittable' ? '<span class="warn">separable</span>'
    : c.state === 'partial' ? `<span class="warn">parcial (${c.matched}/${c.release.trackCount || '?'})</span>`
    : c.state === 'extra' ? '<span class="ok">con extras</span>'
    : c.state === 'single' ? '<span class="warn">pista suelta</span>'
    : `<span class="bad">no cuadra${c.matched ? ` (${c.matched}/${a.files})` : ''}</span>`;
  return `<table class="inner"><thead><tr><th></th><th>Fuente</th><th>Artista</th><th>Título</th><th>Año</th><th>País</th><th>Formato</th><th class="num">Pistas</th><th>Evaluación</th><th></th></tr></thead>
    <tbody>${a.candidates.map(c => `<tr class="${a.release && c.release.id === a.release.id ? 'selected' : ''}">
      ${thumbCell(c.release)}<td>${srcTag(c.release.source)}</td><td>${esc(c.release.artist)}</td><td>${esc(c.release.title)}</td>
      <td class="dim">${esc((c.release.date || '').slice(0, 4))}</td><td class="dim">${esc(c.release.country || '')}</td>
      <td class="dim">${esc(c.release.format || '')}</td>
      <td class="num ${c.release.trackCount === a.files ? 'ok' : ''}">${c.release.trackCount || '?'}</td>
      <td>${evalLabel(c)}</td>
      <td><button class="ghost act" data-act="choose" data-id="${c.release.id}">${c.evaluated ? 'usar esta' : 'evaluar y usar'}</button></td>
    </tr>`).join('')}</tbody></table>
    <div class="row" style="margin-top:8px">
      <span class="meta">¿No está? Pega una URL de MusicBrainz:</span>
      <input type="text" class="mbid" placeholder="https://musicbrainz.org/release/…" style="max-width:360px">
      <button class="ghost act" data-act="paste">usar</button>
    </div>`;
}

async function rowAction(dir, act, btn) {
  $('#libBatchErr').textContent = '';
  try {
    const a = lib.albums.find(x => x.dir === dir);
    switch (act) {
      case 'confirm': await App().ConfirmAlbum(dir); break;
      case 'cands': lib.expanded.has(dir) ? lib.expanded.delete(dir) : lib.expanded.add(dir); renderAlbums(); break;
      case 'choose': await App().ChooseCandidate(dir, btn.dataset.id); break;
      case 'paste': {
        const input = btn.closest('tr').querySelector('input.mbid');
        if (!input || !input.value.trim()) return;
        await App().ChooseCandidate(dir, input.value); break;
      }
      case 'web': openWebSearch(a ? a.name : ''); break;
    }
  } catch (e) { $('#libBatchErr').textContent = e; }
}

// Búsqueda externa: abre el navegador con el nombre del disco. Se pregunta
// el sitio con un pequeño menú improvisado (prompt del sistema no hay).
function openWebSearch(query) {
  const sites = [['google', 'Google'], ['discogs', 'Discogs'], ['bandcamp', 'Bandcamp'], ['musicbrainz', 'MusicBrainz (web)']];
  const menu = document.createElement('div');
  menu.className = 'menu';
  menu.innerHTML = sites.map(([k, n]) => `<button class="ghost" data-site="${k}">${n}</button>`).join('') + `<button class="ghost">cerrar</button>`;
  menu.querySelectorAll('button').forEach(b => b.onclick = () => { if (b.dataset.site) App().OpenSearch(b.dataset.site, query); menu.remove(); });
  document.body.appendChild(menu);
}

function selectedDirs() {
  return [...$('#libAlbums').querySelectorAll('input.sel:checked')].map(i => i.dataset.dir);
}

function updateApplyHint() {
  const sel = new Set(selectedDirs());
  $('#libSelAll').textContent = lib.albums.length && sel.size === lib.albums.length ? 'ninguno' : 'marcar todos';
  const rows = lib.albums.filter(a => sel.has(a.dir));
  const conf = rows.filter(a => a.confirmed);
  const pending = rows.filter(a => a.release && !a.confirmed).length;
  const cover = conf.filter(a => !a.hasCover).length;
  const split = conf.filter(a => a.state === 'splittable').length;
  const tag = conf.filter(a => a.state === 'ok' || a.state === 'partial' || a.state === 'extra').length;
  const singles = conf.filter(a => a.state === 'single').length;
  $('#libApplyHint').textContent = `${conf.length} confirmados (${cover} sin carátula · ${split} separables · ${tag} etiquetables${singles ? ` · ${singles} pistas sueltas` : ''})` + (pending ? ` · ${pending} propuestas por confirmar` : '');
  $('#libAccept').disabled = !rows.some(a => a.release && !a.confirmed && IDENTIFIED.has(a.state));
}
$('#libAlbums').addEventListener('change', updateApplyHint);

// Un evento "album" por cada disco que cambia de estado.
window.runtime.EventsOn('album', info => {
  const i = lib.albums.findIndex(a => a.dir === info.dir);
  // Tras aplicar cambios la carátula puede haber cambiado: olvidar la cacheada.
  if (i >= 0 && lib.albums[i].state === 'working' && info.state !== 'working') delete lib.cards[info.dir];
  if (i >= 0) lib.albums[i] = info; else lib.albums.push(info);
  renderAlbums();
  if (lib.dir === info.dir && !['analyzing', 'working'].includes(info.state)) refreshDetail();
});
window.runtime.EventsOn('batch', b => {
  lib.running = b.running;
  $('#libAnalyze').disabled = b.running || !lib.albums.length;
  $('#libApplyBtn').disabled = b.running;
  $('#libCancel').hidden = !b.running;
  if (!b.running) loadAlbums();
});

$('#libAnalyze').onclick = async () => {
  $('#libBatchErr').textContent = '';
  const dirs = selectedDirs();
  if (!dirs.length) { $('#libBatchErr').textContent = 'marca los discos que quieras analizar (la casilla de cada uno, o la de la cabecera para todos)'; return; }
  libLog(`Analizando ${dirs.length} disco(s)…`);
  try { await App().AnalyzeAlbums(dirs); } catch (e) { $('#libBatchErr').textContent = e; }
};
$('#libCancel').onclick = () => App().CancelBatch();
$('#libAccept').onclick = async () => {
  const n = await App().ConfirmMatching(selectedDirs());
  libLog(`${n} propuesta(s) aceptada(s)`);
};
$('#libApplyBtn').onclick = async () => {
  $('#libBatchErr').textContent = '';
  const dirs = selectedDirs().filter(d => lib.albums.find(a => a.dir === d && a.confirmed));
  if (!dirs.length) { $('#libBatchErr').textContent = 'ninguno de los marcados tiene edición confirmada'; return; }
  try { await App().ApplyAlbums(dirs, $('#apCover').checked, $('#apSplit').checked, $('#apTag').checked); }
  catch (e) { $('#libBatchErr').textContent = e; }
};

// ---- detalle de un disco -------------------------------------------------
// Entrar en un disco: la lista se oculta y se muestra la ficha del disco con
// la información de cada fichero y la edición de MusicBrainz.
async function openDetail(dir) {
  lib.dir = dir; lib.scan = null; lib.release = null;
  $('#libErr').textContent = '';
  $('#libResults').innerHTML = '';
  $('#libCompare').hidden = true;
  $('#libList').hidden = true;
  $('#libBody').hidden = false;
  $('#libDetailName').textContent = dir.split(/[\\/]/).pop();
  $('#libDetailPath').textContent = dir;
  $('#libFacts').textContent = 'leyendo…';
  $('#libFiles').innerHTML = '';
  renderCover(dir);
  loadIcon(dir);
  try { lib.scan = await App().ScanFolder(dir); }
  catch (e) { $('#libErr').textContent = e; $('#libFacts').textContent = ''; return; }
  const s = lib.scan;
  $('#libArtist').value = s.artist || '';
  $('#libAlbum').value = s.album || '';
  renderFacts();
  renderDetailFiles();
  window.scrollTo({ top: 0, behavior: 'smooth' });
  // Si el análisis en lote ya eligió edición, la mostramos directamente.
  const relID = await App().AlbumRelease(dir);
  if (relID) {
    await pickRelease(relID, null);
  }
}

function closeDetail() {
  lib.dir = ''; lib.scan = null; lib.release = null;
  $('#libBody').hidden = true;
  $('#libList').hidden = false;
}
$('#libBack').onclick = () => { closeDetail(); loadAlbums(); };
$('#libOpenDir').onclick = () => lib.dir && App().OpenPath(lib.dir).catch(e => $('#libErr').textContent = e);

// Carátula del disco (cover.jpg o la incrustada), pedida a Go como data URL.
async function renderCover(dir) {
  const img = $('#libCover');
  let src = '';
  try { src = await App().AlbumCover(dir); } catch (e) { src = ''; }
  if (src) { img.src = src; img.hidden = false; $('#libCoverPh').hidden = true; }
  else { img.removeAttribute('src'); img.hidden = true; $('#libCoverPh').hidden = false; }
}

// Cabecera: lo que dicen las etiquetas de los ficheros, resumido.
function renderFacts() {
  const s = lib.scan, f = s.files || [];
  // Carpeta recién creada por «descargar»: todavía no tiene ninguna pista.
  if (!f.length) {
    $('#libFacts').innerHTML = `<i>carpeta vacía</i><br>sin pistas todavía${s.cover ? ' · cover.jpg' : ''}`;
    $('#libLocalSummary').textContent = '';
    return;
  }
  const uniq = key => [...new Set(f.map(x => x[key]).filter(Boolean))];
  const artists = uniq('albumArtist').length ? uniq('albumArtist') : uniq('artist');
  const albums = uniq('album'), years = uniq('date').map(d => d.slice(0, 4));
  const codecs = uniq('codec'), covers = f.filter(x => x.hasCover).length;
  const bitrates = f.map(x => x.bitrate).filter(Boolean);
  const br = bitrates.length ? `${Math.min(...bitrates)}${Math.min(...bitrates) !== Math.max(...bitrates) ? '–' + Math.max(...bitrates) : ''} kbps` : '';
  const line1 = [artists.join(', ') || '<i>sin artista</i>', albums.join(' / ') || '<i>sin álbum</i>', years.join('/')].filter(Boolean).join(' · ');
  const line2 = [`${f.length} pista(s)`, fmtDur(s.total), codecs.join('/'), br,
    `${fmtSize(f.reduce((a, x) => a + x.size, 0))}`,
    `carátula incrustada en ${covers}/${f.length}`, s.cover ? 'cover.jpg' : ''].filter(Boolean).join(' · ');
  $('#libFacts').innerHTML = `${line1}<br>${line2}`;
  $('#libLocalSummary').textContent = '';
}

// Tabla con la información real de cada fichero.
// Tabla con la información real de cada fichero. Las columnas que dicen lo
// mismo en todas las filas (álbum, año, artista en un disco de un solo
// artista) sobran: ya están en la cabecera del disco. Así el nombre y el
// título tienen sitio para leerse enteros.
function renderDetailFiles() {
  const s = lib.scan, files = s.files || [];
  if (!files.length) { $('#libFiles').innerHTML = '<p class="empty">No hay ficheros de audio en esta carpeta.</p>'; return; }
  const miss = v => v ? esc(v) : '<span class="missing">—</span>';
  const uniform = key => files.length > 1 && files.every(f => (f[key] || '') === (files[0][key] || '')) && files[0][key];
  const showArtist = !uniform('artist'), showAlbum = !uniform('album'), showYear = !uniform('date');

  const headers = ['', '#', 'Fichero', 'Título'];
  if (showArtist) headers.push('Artista');
  if (showAlbum) headers.push('Álbum');
  headers.push('Pista');
  if (showYear) headers.push('Año');
  headers.push('Duración', 'Formato', 'Carátula');

  $('#libFiles').innerHTML = table(headers, files.map((f, i) => {
    const cells = [
      `<td><button class="ghost play" data-i="${i}" title="escuchar">▶</button></td>`,
      `<td class="num">${i + 1}</td>`,
      `<td class="text">${esc(f.name)}</td>`,
      `<td class="text">${miss(f.title)}</td>`];
    if (showArtist) cells.push(`<td class="text">${miss(f.artist)}</td>`);
    if (showAlbum) cells.push(`<td class="text">${miss(f.album)}</td>`);
    cells.push(`<td class="num">${miss(f.track)}</td>`);
    if (showYear) cells.push(`<td class="num">${miss((f.date || '').slice(0, 4))}</td>`);
    cells.push(
      `<td class="num">${fmtDur(f.duration)}</td>`,
      `<td class="num dim" title="${f.sampleRate} Hz, ${f.channels} canal(es), ${fmtSize(f.size)}">${esc(f.codec || '?')} ${f.bitrate ? f.bitrate + 'k' : ''}</td>`,
      `<td class="${f.hasCover ? 'ok' : 'missing'}">${f.hasCover ? 'sí' : 'no'}</td>`);
    return cells;
  }));
  $('#libFiles').querySelectorAll('button.play').forEach(b => b.onclick = () => playFile(files[Number(b.dataset.i)], lib.dir));
}

function table(headers, rows, rowAttrs = () => '') {
  return `<table><thead><tr>${headers.map(h => `<th class="${/Duración|Δ|#|Pistas|Canciones/.test(h) ? 'num' : ''}">${h}</th>`).join('')}</tr></thead>
    <tbody>${rows.map((r, i) => `<tr ${rowAttrs(i)}>${r.join('')}</tr>`).join('')}</tbody></table>`;
}

// ---- búsqueda manual -----------------------------------------------------
$('#libSearch').onclick = search;
$('#libArtist').onkeydown = $('#libAlbum').onkeydown = $('#libSong').onkeydown = e => { if (e.key === 'Enter') search(); };

const sourceLabel = { musicbrainz: 'MusicBrainz', deezer: 'Deezer', bandcamp: 'Bandcamp', discogs: 'Discogs' };
const srcTag = s => `<span class="src ${s}">${sourceLabel[s] || s}</span>`;

async function search() {
  $('#libErr').textContent = '';
  // Si han pegado una URL o ID de MusicBrainz, ir directos a esa edición.
  const mbid = ($('#libAlbum').value + ' ' + $('#libArtist').value).match(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i);
  if (mbid) { $('#libResults').innerHTML = ''; return pickRelease(mbid[0], null); }
  const sources = 'MusicBrainz, Deezer, Bandcamp' + (settings.discogsToken ? ', Discogs' : '');
  const status = msg => $('#libResults').innerHTML = `<p class="empty">${msg}</p>`;
  const artist = $('#libArtist').value, album = $('#libAlbum').value, song = $('#libSong').value.trim();
  try {
    // Con canción: en qué discos aparece esa canción (y luego se compara como siempre).
    if (song) {
      status(`Buscando la canción «${esc(song)}» en MusicBrainz, Deezer${settings.discogsToken ? ', Discogs' : ''}…`);
      const bySong = await App().SearchBySong(artist, song);
      return renderSearchResults(bySong, `Ninguna edición contiene una canción llamada «${esc(song)}»${artist ? ' de ' + esc(artist) : ''}. ${webLink(artist + ' ' + song)}`);
    }
    // 1. Por nombre de artista/álbum.
    status(`Buscando «${esc(album)}» en ${sources}…`);
    const byName = await App().SearchReleases(artist, album);
    if ((byName.results || []).length) return renderSearchResults(byName, '');

    // 2. Por las canciones: alguna edición que contenga varios de los títulos.
    status('Nada con ese nombre. Buscando por las canciones… (unos segundos)');
    const byTracks = await App().SearchByTracks(lib.dir);
    if ((byTracks.results || []).some(r => r.hits >= 2 || r.hits === r.hitsOf)) return renderSearchResults(byTracks, '');

    // 3. Orígenes: no es un disco, es una selección; de dónde viene cada fichero.
    status('Ningún disco contiene estas canciones. Buscando el origen de cada fichero… (unos segundos por álbum)');
    const origins = await App().Origins(lib.dir) || [];
    if (origins.some(o => o.release)) return renderOrigins(origins);

    $('#libResults').innerHTML = `<p class="empty">No lo encuentro ni por nombre, ni por canciones, ni fichero a fichero. ${webLink(artist + ' ' + album)}</p>`;
  } catch (e) {
    $('#libResults').innerHTML = ''; $('#libErr').textContent = e;
  }
}

// Último recurso: abrir la búsqueda en el navegador.
function webLink(query) {
  return `<a href="#" class="weblink" data-q="${esc(query.trim())}">Buscar en el navegador</a>`;
}
document.addEventListener('click', e => {
  const a = e.target.closest('a.weblink');
  if (a) { e.preventDefault(); openWebSearch(a.dataset.q); }
});

function renderSearchResults(res, emptyMsg) {
  const rels = res.results || [];
  const warn = (res.warnings || []).length ? `<p class="empty warn">Sin respuesta de: ${res.warnings.map(esc).join(' · ')}</p>` : '';
  if (!rels.length) { $('#libResults').innerHTML = warn + `<p class="empty">${emptyMsg}</p>`; return; }

  const n = lib.scan && lib.scan.files ? lib.scan.files.length : 0;
  const byTracks = rels.some(r => r.hitsOf);
  const headers = ['', 'Fuente', 'Artista', 'Título', 'Año', 'País', 'Formato', 'Pistas'];
  if (byTracks) headers.push('Canciones');
  headers.push('');
  $('#libResults').innerHTML = warn + table(headers,
    rels.map(r => {
      const cells = [
        thumbCell(r),
        `<td>${srcTag(r.source)}</td>`,
        `<td>${esc(r.artist)}</td>`, `<td>${esc(r.title)}</td>`,
        `<td class="dim">${esc((r.date || '').slice(0, 4))}</td>`, `<td class="dim">${esc(r.country || '')}</td>`,
        `<td class="dim">${esc(r.format || '')}</td>`,
        `<td class="num ${n && r.trackCount === n ? 'ok' : ''}">${r.trackCount || '?'}</td>`];
      if (byTracks) cells.push(`<td class="num ${r.hits === r.hitsOf ? 'ok' : r.hits >= 2 ? 'warn' : 'dim'}" title="títulos de tus ficheros que contiene esta edición">${r.hits}/${r.hitsOf}</td>`);
      cells.push(`<td class="links"><button class="ghost usecover" data-id="${esc(r.id)}" title="Bajar esta carátula a la carpeta e incrustarla, sin cambiar de edición">carátula</button> <button class="ghost getalbum" data-id="${esc(r.id)}" title="Traerse este disco: crea su carpeta en la de descargas con la carátula y busca cada pista en Soulseek, Bandcamp y YouTube">descargar</button></td>`);
      return cells;
    }),
    i => `class="pick" data-id="${esc(rels[i].id)}"`);
  $('#libResults').querySelectorAll('tr.pick').forEach(tr => tr.onclick = () => pickRelease(tr.dataset.id, tr));
  $('#libResults').querySelectorAll('button.getalbum').forEach(b => b.onclick = async e => {
    e.stopPropagation(); b.disabled = true; b.textContent = 'creando…'; $('#libErr').textContent = '';
    try {
      const rel = rels.find(x => x.id === b.dataset.id) || {};
      const res = await App().CreateAlbum(b.dataset.id);
      libLog(`Disco creado en ${res.dir}: faltan ${res.missing ? res.missing.length : 0} pista(s)`);
      delete lib.cards[res.dir];
      // Si la ficha del disco falla, da igual: lo que importa es el panel de
      // Completar, que es lo que baja las pistas.
      try { await openDetail(res.dir); await pickRelease(b.dataset.id, null); }
      catch (e) { libLog('aviso: ficha del disco: ' + e); }
      showCompletion(res.dir, b.dataset.id, res.missing || [], rel.artist, rel.title);
      loadAlbums();
    } catch (err) { $('#libErr').textContent = err; b.disabled = false; b.textContent = 'descargar'; }
  });
  $('#libResults').querySelectorAll('button.usecover').forEach(b => b.onclick = async e => {
    e.stopPropagation(); b.disabled = true; b.textContent = 'bajando…'; $('#libErr').textContent = '';
    try { await App().FetchCover(lib.dir, b.dataset.id); b.textContent = 'puesta'; delete lib.cards[lib.dir]; renderCover(lib.dir); loadIcon(lib.dir); }
    catch (err) { $('#libErr').textContent = err; b.disabled = false; b.textContent = 'carátula'; }
  });
}

// Elegir una edición: Go la trae y empareja cada pista con el fichero que
// más se le parece (título + duración), sin fiarse del orden.
// `tr` es la fila pulsada por el usuario; null cuando se re-evalúa sola
// (al abrir la ficha o tras una acción). Elegir a mano una edición que
// cuadra decide el nombre del disco: la carpeta se renombra ya.
async function pickRelease(id, tr) {
  $('#libResults').querySelectorAll('tr').forEach(x => x.classList.toggle('selected', x === tr));
  $('#libErr').textContent = '';
  let res;
  try {
    res = await App().CompareRelease(lib.dir, id);
    lib.release = res.release; lib.pairs = res.pairs; lib.cmpState = res.state;
  } catch (e) { $('#libErr').textContent = e; return; }
  renderCompare();

  if (tr && FITS.has(res.state)) {
    try {
      const newDir = await App().RenameToEdition(lib.dir, id);
      if (newDir && newDir !== lib.dir) {
        delete lib.cards[lib.dir]; lib.dir = newDir;
        $('#libDetailName').textContent = newDir.split(/[\\/]/).pop();
        $('#libDetailPath').textContent = newDir;
        lib.scan = await App().ScanFolder(newDir);
        loadAlbums();
      }
    } catch (e) { libLog('carpeta: ' + e); }
  }
}

// ---- comparación ---------------------------------------------------------
function renderCompare() {
  const rel = lib.release, files = (lib.scan && lib.scan.files) || [], pairs = lib.pairs || [];
  const r = rel.release, tracks = rel.tracks || [];
  $('#libCompare').hidden = false;
  $('#libRelTitle').innerHTML = `${srcTag(r.source)} ${esc(r.artist)} — ${esc(r.title)}`;
  const relCover = $('#libRelCover'), relURL = coverURL(r, true);
  relCover.hidden = !relURL; relCover.dataset.alt = r.source === 'musicbrainz' && r.releaseGroupId ? `https://coverartarchive.org/release-group/${r.releaseGroupId}/front-500` : '';
  relCover.onerror = () => { if (relCover.dataset.alt) { relCover.src = relCover.dataset.alt; relCover.dataset.alt = ''; } else relCover.hidden = true; };
  if (relURL) relCover.src = relURL;
  $('#libRelMeta').textContent = [r.date, r.country, r.format, `${tracks.length} pistas`, fmtDur(rel.totalLength)].filter(Boolean).join(' · ');

  const single = files.length === 1 && tracks.length > 1;
  const singleFits = single && rel.totalLength > 0 && Math.abs(files[0].duration - rel.totalLength) <= 15;
  const singleTrack = single && !singleFits && lib.cmpState === 'single';

  let okCount = 0, assigned = 0, reorder = false;
  const rows = tracks.map((t, i) => {
    const p = pairs[i] || { file: -1 };
    const f = p.file >= 0 ? files[p.file] : null;
    let diffCell = '<td class="num dim">—</td>', nameCell = '<td class="missing">sin fichero</td>';
    if (f) {
      assigned++;
      if (p.file !== i) reorder = true;
      const d = p.diff;
      const cls = !t.length ? 'dim' : Math.abs(d) <= lib.tolerance ? 'ok' : Math.abs(d) <= 10 ? 'warn' : 'bad';
      if (cls === 'ok') okCount++;
      diffCell = `<td class="num ${cls}">${t.length ? (d >= 0 ? '+' : '') + d.toFixed(1) + ' s' : '?'}</td>`;
      const simCls = p.sim >= 0.8 ? 'ok' : p.sim >= 0.5 ? 'warn' : 'bad';
      nameCell = `<td class="dim" title="parecido de título: ${Math.round(p.sim * 100)}%"><button class="ghost play" data-f="${p.file}" title="escuchar">▶</button> <span class="${simCls}">●</span> ${esc(f.name)}</td>`;
    }
    return [
      `<td class="num">${t.number}</td>`,
      `<td>${esc(t.title)}</td>`,
      `<td class="num">${t.length ? fmtDur(t.length) : '?'}</td>`,
      nameCell,
      `<td class="num">${f ? fmtDur(f.duration) : ''}</td>`,
      diffCell];
  });
  // Ficheros que no casan con ninguna pista.
  const usedFiles = new Set(pairs.filter(p => p.file >= 0).map(p => p.file));
  const leftover = files.filter((f, i) => !usedFiles.has(i));
  let html = table(['#', 'Pista (edición)', 'Duración', 'Fichero local', 'Duración', 'Δ'], rows);
  if (leftover.length && !single) {
    html += `<p class="empty" style="margin:8px 0 0">Sin pista en la edición: ${leftover.map(f => esc(f.name)).join(', ')}</p>`;
  }
  $('#libTable').innerHTML = html;
  $('#libTable').querySelectorAll('button.play').forEach(b => b.onclick = () => playFile(files[Number(b.dataset.f)], lib.dir));

  const n = files.length, m = tracks.length;
  let summary;
  if (!n) summary = `La carpeta está vacía: <b>faltan las ${m} pistas</b>. <b>Completar</b> las busca en Soulseek, Bandcamp y YouTube.`;
  else if (singleFits) summary = `Un solo fichero de ${fmtDur(files[0].duration)} que coincide con la duración total (${fmtDur(rel.totalLength)}): se puede separar en ${m} pistas.`;
  else if (singleTrack) {
    const p = pairs.find(x => x.file === 0), t = p ? tracks[p.track] : null;
    summary = `Es una <b>pista suelta</b> de este disco${t ? `: la ${t.number}, «${esc(t.title)}»` : ''}. <b>Crear álbum con esta pista</b> la lleva a su carpeta curada y busca el resto.`;
  }
  else if (single && !rel.totalLength) summary = `Un solo fichero de ${fmtDur(files[0].duration)} y una edición de ${m} pistas <b>sin duraciones</b> (ni en ${srcTag(r.source)} ni en las otras fuentes). Se puede separar igualmente: <b>Detectar silencios</b> propone los cortes, o pega las marcas de tiempo de la descripción del vídeo.`;
  else if (single) summary = `Un solo fichero de ${fmtDur(files[0].duration)}, pero la edición dura ${fmtDur(rel.totalLength)}: no parece la misma edición.`;
  else if (okCount === m && n === m) summary = `Las ${okCount} pistas coinciden en duración (±${lib.tolerance} s). Disco íntegro.` + (reorder ? ' El orden o los nombres de los ficheros no siguen la edición: <b>Etiquetar</b> los fija.' : '');
  else if (n < m && okCount === n) summary = `Tus ${n} ficheros cuadran con ${n} de las ${m} pistas: es este disco, pero <b>faltan ${m - n}</b>. <b>Completar</b> las busca.`;
  else if (n > m && okCount === m) summary = `Las ${m} pistas de la edición están y cuadran; sobran ${n - m} fichero(s) que no son de este disco (extras o duplicados; se dejan como están).`;
  else if (n !== m) summary = `La carpeta tiene ${n} ficheros y la edición ${m} pistas (${okCount} cuadran, ${assigned} emparejadas por parecido). Elige otra edición o revisa la carpeta.`;
  else summary = `${okCount} de ${m} pistas coinciden en duración (${assigned} emparejadas por parecido); revisa las marcadas.`;
  $('#libSummary').innerHTML = summary;

  $('#actCover').disabled = false;
  $('#actSplit').disabled = !singleFits;
  $('#libSplitPanel').hidden = !(single && !rel.totalLength && m > 1);
  if (r.lengthsFrom) $('#libRelMeta').textContent += ` · duraciones de ${r.lengthsFrom}`;
  $('#actTag').disabled = single || assigned === 0;
  $('#actComplete').disabled = single || assigned >= m; // solo si faltan pistas
  $('#actUpgrade').disabled = single || assigned === 0;
  $('#actBuild').hidden = !singleTrack;
  $('#libComplete').hidden = true;
  $('#libUpgrade').hidden = true;
}

// ---- acciones del detalle ------------------------------------------------
async function action(fn) {
  ['#actCover', '#actSplit', '#actTag'].forEach(s => $(s).disabled = true);
  $('#libErr').textContent = '';
  try {
    await fn();
    // Tras aplicar algo con una edición, la carpeta pasa a llamarse como ella.
    if (lib.release && FITS.has(lib.cmpState)) {
      try {
        const newDir = await App().RenameToEdition(lib.dir, lib.release.release.id);
        if (newDir && newDir !== lib.dir) { delete lib.cards[lib.dir]; lib.dir = newDir; }
      } catch (e) { libLog('carpeta: ' + e); }
    }
  }
  catch (e) { $('#libErr').textContent = e; libLog('ERROR: ' + e); }
  finally {
    delete lib.cards[lib.dir]; // nombre y carátula pueden haber cambiado
    await refreshDetail();
    loadAlbums();
  }
}

// Releer la carpeta del detalle sin perder la edición elegida.
async function refreshDetail() {
  if (!lib.dir) return;
  try { lib.scan = await App().ScanFolder(lib.dir); } catch (e) { $('#libErr').textContent = e; return; }
  $('#libDetailName').textContent = lib.dir.split(/[\\/]/).pop();
  $('#libDetailPath').textContent = lib.dir;
  renderCover(lib.dir);
  loadIcon(lib.dir);
  renderFacts();
  renderDetailFiles();
  if (lib.release) await pickRelease(lib.release.release.id, null); else $('#libCompare').hidden = true;
}

// Go avisa cuando renombra una carpeta (también desde el lote): la tarjeta
// cambia de sitio sin esperar a releer toda la lista.
window.runtime.EventsOn('album-moved', m => {
  const a = lib.albums.find(x => x.dir === m.from);
  if (a) { a.dir = m.to; a.name = m.to.split(/[\\/]/).pop(); }
  delete lib.cards[m.from];
  if (lib.dir === m.from) lib.dir = m.to;
  if (cmp.dir === m.from) cmp.dir = m.to;
  renderAlbums();
});

$('#actCover').onclick = () => action(async () => {
  const r = lib.release.release;
  const n = await App().FetchCover(lib.dir, r.id);
  libLog(`Carátula aplicada a ${n} fichero(s)`);
});
$('#actSplit').onclick = () => action(async () => {
  const f = lib.scan.files[0];
  libLog(`Separando ${f.name} en ${lib.release.tracks.length} pistas…`);
  await App().SplitFile(f.path, lib.release.release.id);
});
$('#actTag').onclick = () => action(() => App().TagFolder(lib.dir, lib.release.release.id));

// Marcar / desmarcar todos los discos (vale para lista y miniaturas).
$('#libSelAll').onclick = () => {
  const boxes = [...$('#libAlbums').querySelectorAll('input.sel')];
  const all = boxes.length && boxes.every(b => b.checked);
  boxes.forEach(b => b.checked = !all);
  updateApplyHint();
};

// ---- orígenes: de qué disco viene cada fichero ---------------------------
function renderOrigins(origins) {
  // Agrupar por edición de origen; los no encontrados, al final.
  const groups = new Map();
  for (const o of origins) {
    const key = o.release ? o.release.id : '';
    if (!groups.has(key)) groups.set(key, { release: o.release, cover: o.coverUrl, items: [] });
    groups.get(key).items.push(o);
  }
  const ordered = [...groups.values()].sort((a, b) => (a.release ? 0 : 1) - (b.release ? 0 : 1));
  const ok = origins.filter(o => o.state === 'ok').length, doubt = origins.filter(o => o.state === 'doubt').length;
  let html = `<p class="summary">${ok} de ${origins.length} ficheros localizados en su disco de origen con título y duración correctos${doubt ? `; ${doubt} dudoso(s): cuadra la duración pero no el título` : ''}.</p>`;
  for (const g of ordered) {
    const r = g.release;
    const usable = g.items.filter(o => o.state === 'ok' || o.state === 'doubt').map(o => o.file);
    const head = r
      ? `${g.cover ? `<img class="ocover" src="${esc(g.cover)}" alt="">` : '<div class="ocover none">♪</div>'}
         <div style="flex:1"><div>${srcTag(r.source)} <b>${esc(r.artist)} — ${esc(r.title)}</b></div>
         <div class="meta">${[r.date, r.country, r.format, r.trackCount ? r.trackCount + ' pistas' : ''].filter(Boolean).join(' · ')}</div></div>
         <button class="ghost build" data-key="${esc(r.id)}" data-artist="${esc(r.artist)}" data-album="${esc(r.title)}" data-files="${usable.join(',')}" ${usable.length ? '' : 'disabled'} title="Crea la carpeta «${esc(r.artist)} - ${esc(r.title)}» junto a esta y mueve allí los ficheros curados (nombre, etiquetas, carátula)">Crear álbum y mover ${usable.length}</button>`
      : `<div class="ocover none">?</div><div><b>Sin disco de origen encontrado</b></div>`;
    html += `<div class="origin"><div class="ohead">${head}</div>` + table(['Fichero', 'Pista en el disco', 'Duración', 'Δ', 'Estado'],
      g.items.map(o => [
        `<td class="text">${esc(o.name)}</td>`,
        `<td class="text">${o.track ? `${o.track.number}. ${esc(o.track.title)}` : '<span class="dim">—</span>'}</td>`,
        `<td class="num">${o.track && o.track.length ? fmtDur(o.track.length) : ''}</td>`,
        `<td class="num ${o.state === 'ok' ? 'ok' : o.track ? 'bad' : 'dim'}">${o.track && o.track.length ? (o.diff >= 0 ? '+' : '') + o.diff.toFixed(1) + ' s' : ''}</td>`,
        `<td class="${o.state === 'ok' ? 'ok' : o.state === 'doubt' || o.state === 'mismatch' ? 'warn' : 'bad'}" title="${esc(o.message)}">${o.state === 'ok' ? 'íntegra' : o.state === 'doubt' ? 'dudoso' : o.state === 'mismatch' ? 'no cuadra' : 'no encontrado'}${o.message.startsWith('por la canción') ? ' <span class="dim">(por la canción)</span>' : ''}</td>`])) + '</div>';
  }
  $('#libResults').innerHTML = html;
  $('#libResults').querySelectorAll('button.build').forEach(b => b.onclick = () => buildAlbum(b.dataset.key, b.dataset.files.split(',').filter(Boolean).map(Number), b));
}

async function buildAlbum(key, files, btn) {
  $('#libErr').textContent = '';
  btn.disabled = true; btn.textContent = 'creando…';
  try {
    const res = await App().BuildAlbum(lib.dir, key, files, true);
    btn.textContent = `creado (${res.moved} movidos)`;
    delete lib.cards[lib.dir]; // se han ido ficheros: nombre y carátula pueden cambiar
    libLog(`Álbum creado: ${res.dir} (${res.moved} ficheros; faltan ${res.missing ? res.missing.length : 0} pistas)`);
    showCompletion(res.dir, key, res.missing || [], btn.dataset.artist, btn.dataset.album);
    refreshDetail(); loadAlbums();
  } catch (e) { $('#libErr').textContent = e; btn.disabled = false; btn.textContent = 'Crear álbum'; }
}

// ---- completar un álbum: buscar en YouTube las pistas que faltan ---------
const cmp = { dir: '', key: '', artist: '', missing: [], cands: {} }; // cands: número de pista -> candidatos

function showCompletion(albumDir, key, missing, artist, album) {
  cmp.dir = albumDir; cmp.key = key; cmp.artist = artist || ''; cmp.album = album || ''; cmp.missing = missing; cmp.cands = {}; cmp.slskPending = false; cmp.bcPending = false;
  $('#libComplete').hidden = false;
  $('#cmpMeta').textContent = `${albumDir.split(/[\/]/).pop()} · faltan ${missing.length} pista(s)`;
  $('#cmpSummary').textContent = '';
  renderCompletion();
  $('#libComplete').scrollIntoView({ behavior: 'smooth', block: 'start' });
  if (missing.length) findAllCandidates();
}

function renderCompletion() {
  if (!cmp.missing.length) { $('#cmpTable').innerHTML = '<p class="empty">No falta ninguna pista: el álbum está completo.</p>'; return; }
  const srcLabel = c => c.source === 'soulseek'
    ? `<span class="src discogs">Soulseek</span> <span class="dim">${esc(c.username)}</span> · <span class="${c.ext === 'flac' ? 'ok' : ''}">${esc(c.ext.toUpperCase())}${c.bitRate ? ' ' + c.bitRate + 'k' : ''}</span> · ${fmtSize(c.size)} · ${c.freeSlot ? '<span class="ok">hueco libre</span>' : `<span class="warn">cola ${c.queue}</span>`}`
    : c.source === 'bandcamp'
    ? `<span class="src bandcamp">Bandcamp</span> <span class="dim">${esc(c.channel)}</span> · <span class="ok">pista exacta</span>`
    : `<span class="src deezer">YouTube</span> <span class="dim">[${esc(c.channel)}]</span>`;
  $('#cmpTable').innerHTML = cmp.missing.map(t => {
    const cands = cmp.cands[t.number];
    let body;
    if (cands === undefined) body = `<span class="dim">buscando en ${[cmp.slskPending ? 'Soulseek' : '', cmp.bcPending ? 'Bandcamp' : '', 'YouTube'].filter(Boolean).join(', ')}…</span>`;
    else if (!cands.length) body = '<span class="missing">sin candidatos</span>';
    else {
      // Mejor candidato por defecto: el primero que cuadre en duración.
      const def = cands.findIndex(c => Math.abs(c.diff) <= 3);
      body = cands.slice(0, 6).map((c, i) => {
        const cls = Math.abs(c.diff) <= 3 ? 'ok' : Math.abs(c.diff) <= 10 ? 'warn' : 'bad';
        return `<label class="cand"><input type="radio" name="cand-${t.number}" value="${i}" ${i === def ? 'checked' : ''}>
          ${srcLabel(c)} <span>${esc(c.title)}</span> <span class="num ${cls}">${fmtDur(c.duration)} (${c.diff >= 0 ? '+' : ''}${Math.round(c.diff)} s)</span></label>`;
      }).join('') + `<label class="cand"><input type="radio" name="cand-${t.number}" value=""> <span class="dim">ninguno</span></label>`;
    }
    return `<div class="miss"><div class="mhead"><b>${t.number}. ${esc(t.title)}</b> <span class="meta">${t.length ? fmtDur(t.length) : ''}</span></div>${body}</div>`;
  }).join('');
}

// Candidatos: primero Soulseek (una sola búsqueda del disco, si está
// conectado), luego YouTube pista a pista. Se van fundiendo por pista,
// Soulseek delante.
async function findAllCandidates() {
  const artist = cmp.artist;
  const slskFor = {};
  let slskOn = false;
  try { slskOn = (await App().SoulseekState()).connected; } catch (e) {}
  if (slskOn) {
    cmp.slskPending = true; renderCompletion();
    try {
      const found = await App().FindAlbumSourcesSoulseek(artist, cmp.album || '', cmp.missing) || {};
      Object.assign(slskFor, found);
      const n = Object.values(found).filter(l => l && l.length).length;
      libLog(`Soulseek: candidatos para ${n} de ${cmp.missing.length} pista(s) que faltan`);
    } catch (e) { libLog('Soulseek: ' + e); }
    cmp.slskPending = false;
  }
  // Bandcamp: si el disco está allí, cada pista tiene su URL exacta.
  let bcFor = {};
  cmp.bcPending = true; renderCompletion();
  try {
    bcFor = await App().FindAlbumSourcesBandcamp(cmp.key, artist, cmp.album || '', cmp.missing) || {};
    const n = Object.values(bcFor).filter(l => l && l.length).length;
    if (n) libLog(`Bandcamp: tiene ${n} de ${cmp.missing.length} pista(s) que faltan`);
  } catch (e) { libLog('Bandcamp: ' + e); }
  cmp.bcPending = false;
  for (const t of cmp.missing) {
    if (cmp.cands[t.number] !== undefined) continue;
    let yt = [];
    try { yt = await App().FindTrackSources(t.artist || artist, t.title, t.length) || []; }
    catch (e) { libLog('YouTube: ' + e); }
    cmp.cands[t.number] = [...(slskFor[t.number] || []), ...(bcFor[t.number] || []), ...yt];
    renderCompletion();
  }
}

$('#cmpOpen').onclick = () => cmp.dir && App().OpenPath(cmp.dir).catch(e => $('#libErr').textContent = e);
$('#cmpDownload').onclick = async () => {
  const picks = cmp.missing.map(t => {
    const sel = $('#cmpTable').querySelector(`input[name="cand-${t.number}"]:checked`);
    if (!sel || sel.value === '') return null;
    const c = (cmp.cands[t.number] || [])[Number(sel.value)];
    return c ? { number: t.number, cand: c } : null;
  }).filter(Boolean);
  if (!picks.length) { $('#cmpSummary').textContent = 'No hay ninguna pista marcada.'; return; }
  $('#cmpDownload').disabled = true;
  let done = 0;
  for (const p of picks) {
    $('#cmpSummary').textContent = `Descargando pista ${p.number} (${p.cand.source === 'soulseek' ? 'Soulseek, puede tardar si hay cola' : p.cand.source === 'bandcamp' ? 'Bandcamp' : 'YouTube'})… (${done}/${picks.length})`;
    try {
      if (p.cand.source === 'soulseek') await App().DownloadTrackSoulseek(p.cand.username, p.cand, cmp.dir, cmp.key, p.number);
      else await App().DownloadTrack(p.cand.url, cmp.dir, cmp.key, p.number);
      done++;
    }
    catch (e) { libLog(`pista ${p.number}: ${e}`); }
  }
  $('#cmpDownload').disabled = false;
  try {
    cmp.missing = await App().MissingTracks(cmp.dir, cmp.key) || [];
    $('#cmpMeta').textContent = `${cmp.dir.split(/[\/]/).pop()} · faltan ${cmp.missing.length} pista(s)`;
  } catch (e) {}
  $('#cmpSummary').textContent = `${done} de ${picks.length} descargadas. ${cmp.missing.length ? 'Faltan ' + cmp.missing.length + '.' : 'Álbum completo.'}`;
  renderCompletion();
  if (cmp.dir === lib.dir) refreshDetail();
  loadAlbums();
};

// Desde la comparación de un disco normal: completar la carpeta actual.
$('#actComplete').onclick = async () => {
  if (!lib.release) return;
  $('#libErr').textContent = '';
  try {
    const missing = await App().MissingTracks(lib.dir, lib.release.release.id) || [];
    // Si no hay cover.jpg todavía, la traemos para que las descargas la lleven.
    if (!lib.scan.cover) { try { await App().FetchCover(lib.dir, lib.release.release.id); } catch (e) {} }
    showCompletion(lib.dir, lib.release.release.id, missing, lib.release.release.artist, lib.release.release.title);
  } catch (e) { $('#libErr').textContent = e; }
};

// ---- duplicados: varias carpetas con el mismo disco -----------------------
$('#libDupes').onclick = async () => {
  $('#libBatchErr').textContent = '';
  closeDetail();
  $('#libList').hidden = true;
  $('#libDupesView').hidden = false;
  $('#dupMeta').textContent = 'buscando… (lee las etiquetas de cada carpeta; con muchas tarda)';
  $('#dupGroups').innerHTML = '';
  try {
    const groups = await App().FindDuplicates(lib.root) || [];
    renderDupes(groups);
  } catch (e) { $('#dupMeta').textContent = ''; $('#libBatchErr').textContent = e; }
};
$('#dupBack').onclick = () => { $('#libDupesView').hidden = true; $('#libList').hidden = false; loadAlbums(); };

function renderDupes(groups) {
  $('#dupMeta').textContent = groups.length ? `${groups.length} disco(s) con más de una copia` : 'ninguna carpeta repetida';
  if (!groups.length) { $('#dupGroups').innerHTML = '<p class="empty">No hay discos duplicados en esta carpeta.</p>'; return; }
  $('#dupGroups').innerHTML = groups.map((g, gi) => {
    const r = g.release;
    const head = `<b>${esc(g.artist ? g.artist + ' — ' : '')}${esc(g.album)}</b>` +
      (r ? ` <span class="meta">${srcTag(r.source)} ${esc(r.title)} · ${esc((r.date || '').slice(0, 4))} · ${r.trackCount || '?'} pistas</span>` : ' <span class="meta">edición no identificada</span>') +
      ` <span class="meta">· ${g.union} pistas distintas entre las copias</span>`;
    const rows = g.folders.map((f, fi) => [
      `<td class="keep"><label><input type="radio" name="keep-${gi}" value="${fi}" ${f.recommend ? 'checked' : ''}> ${f.recommend ? '<span class="ok">recomendada</span>' : 'conservar'}</label></td>`,
      `<td class="text">${esc(f.name)}${f.canonical ? ' <span class="dim" title="nombre canónico">✓</span>' : ''}</td>`,
      `<td class="num">${f.files}</td>`,
      `<td class="num">${fmtDur(f.total)}</td>`,
      `<td class="num">${f.bitrate ? f.bitrate + 'k' : ''}</td>`,
      `<td class="${f.cover ? 'ok' : 'missing'}">${f.cover ? 'sí' : 'no'}</td>`,
      `<td class="num ${f.tagged === 100 ? 'ok' : f.tagged ? 'warn' : 'bad'}">${f.tagged}%</td>`,
      `<td><span class="state ${f.state || 'pending'}">${f.state ? (stateLabel[f.state] || f.state) + (f.matched ? ` (${f.matched})` : '') : '—'}</span></td>`]);
    return `<div class="panel dgroup" data-g="${gi}">
      <div class="dhead">${head}<span class="spacer"></span><button class="ghost merge">Fusionar en la marcada</button></div>
      ${table(['', 'Carpeta', 'Pistas', 'Duración', 'Bitrate', 'Carátula', 'Etiquetas', 'Estado'], rows)}
      <div class="summary dim">Al fusionar: las pistas que le falten a la marcada se traen de las otras; las otras carpetas se apartan enteras a <code>_duplicados\</code> (no se borra nada).</div>
    </div>`;
  }).join('');
  $('#dupGroups').querySelectorAll('.dgroup').forEach(el => {
    const g = groups[Number(el.dataset.g)];
    el.querySelector('button.merge').onclick = async btn => {
      const sel = el.querySelector('input[type=radio]:checked');
      if (!sel) return;
      const keep = g.folders[Number(sel.value)].dir;
      const others = g.folders.filter((f, i) => i !== Number(sel.value)).map(f => f.dir);
      const b = el.querySelector('button.merge');
      b.disabled = true; b.textContent = 'fusionando…';
      try {
        const res = await App().MergeDuplicates(keep, others);
        b.textContent = `hecho: ${res.moved} pista(s) traídas, ${(res.archived || []).length} carpeta(s) apartadas`;
        others.forEach(d => delete lib.cards[d]); delete lib.cards[keep];
      } catch (e) { b.disabled = false; b.textContent = 'Fusionar en la marcada'; $('#libBatchErr').textContent = e; }
    };
  });
}

// ---- mejorar calidad desde Soulseek --------------------------------------
const up = { dir: '', key: '', items: [] };

$('#actUpgrade').onclick = async () => {
  if (!lib.release) return;
  $('#libErr').textContent = '';
  $('#libUpgrade').hidden = false;
  $('#libComplete').hidden = true;
  $('#upMeta').textContent = 'buscando el disco en Soulseek… (unos 15 s)';
  $('#upTable').innerHTML = ''; $('#upSummary').textContent = '';
  $('#libUpgrade').scrollIntoView({ behavior: 'smooth', block: 'start' });
  try {
    up.dir = lib.dir; up.key = lib.release.release.id;
    up.items = await App().FindUpgradesSoulseek(lib.dir, lib.release.release.id) || [];
  } catch (e) { $('#upMeta').textContent = ''; $('#libErr').textContent = e; $('#libUpgrade').hidden = true; return; }
  renderUpgrades();
};

function renderUpgrades() {
  const improvable = up.items.filter(u => u.candidates && u.candidates.length);
  $('#upMeta').textContent = improvable.length ? `${improvable.length} de ${up.items.length} pistas tienen una versión mejor` : `ninguna de tus ${up.items.length} pistas tiene una versión mejor en Soulseek ahora mismo`;
  $('#upReplace').disabled = !improvable.length;
  const cur = u => `<span class="${qualityCls(u.codec, u.bitrate)}">${esc((u.codec || '?').toUpperCase())}${u.bitrate ? ' ' + u.bitrate + 'k' : ''}</span>`;
  $('#upTable').innerHTML = up.items.map(u => {
    const cands = u.candidates || [];
    const body = !cands.length ? '<span class="dim">ya está en su mejor versión disponible</span>'
      : cands.map((c, i) => `<label class="cand"><input type="radio" name="up-${u.file}" value="${i}" ${i === 0 ? 'checked' : ''}>
          <span class="src discogs">Soulseek</span> <span class="dim">${esc(c.username)}</span> · <span class="${c.ext === 'flac' ? 'ok' : 'warn'}">${esc(c.ext.toUpperCase())}${c.bitRate ? ' ' + c.bitRate + 'k' : ''}</span> · ${fmtSize(c.size)} · ${c.freeSlot ? '<span class="ok">hueco libre</span>' : `<span class="warn">cola ${c.queue}</span>`}
          <span class="dim">${esc(c.title)}</span> <span class="num ${Math.abs(c.diff) <= 3 ? 'ok' : 'warn'}">(${c.diff >= 0 ? '+' : ''}${Math.round(c.diff)} s)</span></label>`).join('') +
        `<label class="cand"><input type="radio" name="up-${u.file}" value=""> <span class="dim">dejar como está</span></label>`;
    return `<div class="miss"><div class="mhead"><b>${u.track ? u.track.number + '. ' + esc(u.track.title) : esc(u.name)}</b> <span class="meta">${esc(u.name)} · ${cur(u)} · ${fmtDur(u.duration)}</span></div>${body}</div>`;
  }).join('');
}

function qualityCls(codec, bitrate) {
  if (/flac|wv|ape|alac|wav|pcm/i.test(codec || '')) return 'ok';
  return bitrate >= 256 ? '' : bitrate >= 192 ? 'warn' : 'bad';
}

$('#upReplace').onclick = async () => {
  const picks = up.items.map(u => {
    const sel = $('#upTable').querySelector(`input[name="up-${u.file}"]:checked`);
    if (!sel || sel.value === '') return null;
    const c = (u.candidates || [])[Number(sel.value)];
    return c ? { file: u.file, name: u.name, cand: c } : null;
  }).filter(Boolean);
  if (!picks.length) { $('#upSummary').textContent = 'No hay ninguna pista marcada.'; return; }
  $('#upReplace').disabled = true;
  let done = 0;
  // De la última a la primera: al sustituir, los índices de los ficheros
  // que quedan por delante no cambian.
  for (const p of [...picks].sort((a, b) => b.file - a.file)) {
    $('#upSummary').textContent = `Sustituyendo ${p.name}… (${done}/${picks.length}; puede tardar si hay cola)`;
    try { await App().ReplaceTrackSoulseek(up.dir, up.key, p.file, p.cand); done++; }
    catch (e) { libLog(`${p.name}: ${e}`); }
  }
  $('#upSummary').textContent = `${done} de ${picks.length} sustituidas. Las originales están en _original\.`;
  $('#upReplace').disabled = false;
  delete lib.cards[lib.dir];
  await refreshDetail();
  loadAlbums();
  // Volver a evaluar lo que queda por mejorar.
  try { up.items = await App().FindUpgradesSoulseek(up.dir, up.key) || []; renderUpgrades(); } catch (e) {}
};

// ---- pista suelta: un solo fichero que es una pista de la edición ---------
// Crea la carpeta del álbum con el fichero curado, borra la carpeta de origen
// si queda vacía y abre el panel de Completar para traer el resto.
$('#actBuild').onclick = async () => {
  if (!lib.release) return;
  $('#libErr').textContent = '';
  $('#actBuild').disabled = true;
  const from = lib.dir, key = lib.release.release.id, r = lib.release.release;
  try {
    const res = await App().BuildAlbum(from, key, [0], true);
    await App().RemoveIfEmptyAlbum(from);
    libLog(`Álbum creado: ${res.dir} (faltan ${res.missing ? res.missing.length : 0} pistas)`);
    delete lib.cards[from]; delete lib.cards[res.dir];
    await openDetail(res.dir);
    await pickRelease(key, null); // muestra la comparación del nuevo álbum
    showCompletion(res.dir, key, res.missing || [], r.artist, r.title);
    loadAlbums();
  } catch (e) { $('#libErr').textContent = e; }
  finally { $('#actBuild').disabled = false; }
};

// ---- icono de carpeta para el Explorador ---------------------------------
// La ficha enseña cómo se dibujaría la carpeta (CD, vinilo, cinta o funda)
// y deja crearlo, fijar el tipo a mano o quitarlo.
const KIND_LABEL = { cd: 'CD', vinyl: 'vinilo', cassette: 'cassette', digital: 'funda' };
const SOURCE_LABEL = { fijado: 'fijado a mano', 'edición': 'según la edición', etiquetas: 'según las etiquetas', desconocido: 'formato desconocido' };

async function loadIcon(dir, kind = '') {
  const img = $('#iconPreview'), meta = $('#iconMeta');
  img.hidden = true; meta.textContent = '…';
  $('#iconRemove').hidden = true;
  try {
    const info = await App().FolderIcon(dir, kind);
    if (lib.dir !== dir) return; // el usuario ya abrió otro disco
    if (info.preview) { img.src = info.preview; img.hidden = false; }
    if (!kind) $('#iconKind').value = info.fixed ? info.kind : '';
    meta.textContent = `${KIND_LABEL[info.kind] || info.kind} · ${SOURCE_LABEL[info.source] || info.source}${info.format ? ` (${info.format})` : ''}${info.exists ? ' · la carpeta ya tiene icono' : ''}`;
    $('#iconRemove').hidden = !info.exists;
    $('#iconApply').textContent = info.exists ? 'actualizar icono' : 'crear icono';
  } catch (e) { meta.textContent = e; }
}
$('#iconKind').onchange = () => loadIcon(lib.dir, $('#iconKind').value);
$('#iconApply').onclick = async () => {
  $('#libErr').textContent = ''; $('#iconApply').disabled = true;
  try { await App().SetFolderIcon(lib.dir, $('#iconKind').value); await loadIcon(lib.dir); }
  catch (e) { $('#libErr').textContent = e; }
  finally { $('#iconApply').disabled = false; }
};
$('#iconRemove').onclick = async () => {
  $('#libErr').textContent = '';
  try { await App().RemoveFolderIcon(lib.dir); await loadIcon(lib.dir); }
  catch (e) { $('#libErr').textContent = e; }
};

// Toda la carpeta de golpe (discos a cualquier profundidad), con el tipo
// automático de cada uno; Go va contando por el evento "icons".
$('#libIcons').onclick = async () => {
  const btn = $('#libIcons');
  if (btn.dataset.running) { await App().CancelIcons(); return; }
  $('#libBatchErr').textContent = '';
  btn.dataset.running = '1'; btn.textContent = 'iconos: buscando discos… (cancelar)';
  try { await App().MakeFolderIcons(lib.root); }
  catch (e) { $('#libBatchErr').textContent = e; }
  finally { delete btn.dataset.running; btn.textContent = 'iconos Explorador'; }
};
window.runtime.EventsOn('icons', p => {
  const btn = $('#libIcons');
  if (!btn.dataset.running || !p.running) return;
  btn.textContent = `iconos: ${p.done + p.skipped}/${p.total} (cancelar)`;
});

// ---- renombrar carpetas en lote ------------------------------------------
// Go propone cómo debería llamarse cada carpeta y aquí se enseña el cambio
// de una en una. No se toca nada hasta confirmar, y cada fila se puede
// desmarcar: renombrar media colección a ciegas no tiene vuelta atrás.
const ren = { dirs: [], items: [] };

$('#libRename').onclick = async () => {
  const sel = selectedDirs();
  ren.dirs = sel.length ? sel : lib.albums.map(a => a.dir);
  if (!ren.dirs.length) { $('#libBatchErr').textContent = 'No hay discos en esta carpeta.'; return; }
  $('#libBatchErr').textContent = '';
  closeDetail();
  $('#libList').hidden = true;
  $('#libRenameView').hidden = false;
  await renPlan();
};
$('#renBack').onclick = () => { $('#libRenameView').hidden = true; $('#libList').hidden = false; loadAlbums(); };

async function renPlan() {
  $('#renErr').textContent = '';
  $('#renMeta').textContent = `mirando cómo debería llamarse cada disco… (${ren.dirs.length})`;
  $('#renTable').innerHTML = '';
  $('#renApply').disabled = true;
  try { ren.items = await App().RenamePlan(ren.dirs) || []; }
  catch (e) { ren.items = []; $('#renMeta').textContent = ''; $('#renErr').textContent = e; return; }
  renderRenamePlan();
}

function renderRenamePlan() {
  const changes = ren.items.filter(it => !it.skip);
  const warned = changes.filter(it => it.warn).length;
  $('#renMeta').textContent = `${changes.length} de ${ren.items.length} carpetas cambiarían de nombre`
    + (warned ? ` · ${warned} con aviso` : '');
  $('#renDropWarned').hidden = !warned;
  $('#renDropWarned').textContent = `desmarcar las ${warned} con aviso`;
  if (!ren.items.length) { $('#renTable').innerHTML = '<p class="empty">No hay discos que mirar.</p>'; $('#renApply').disabled = true; return; }
  $('#renTable').innerHTML = table(
    [changes.length ? '<input type="checkbox" id="renAll" checked title="marcar o desmarcar todas">' : '', 'Carpeta', 'Pasaría a llamarse', 'Según'],
    ren.items.map((it, i) => [
      `<td>${it.skip ? '' : `<input type="checkbox" class="ren${it.warn ? ' warned' : ''}" data-i="${i}" checked>`}</td>`,
      `<td>${esc(it.name)}<br><span class="dim mono">${esc(it.dir)}</span></td>`,
      it.skip ? `<td class="dim">se queda: ${esc(it.skip)}</td>`
        : `<td class="ok">${esc(it.target)}${it.warn ? `<br><span class="warn">⚠ ${esc(it.warn)}</span>` : ''}</td>`,
      `<td class="dim">${esc(it.source || '')}</td>`,
    ]));
  const all = $('#renAll');
  if (all) all.onclick = () => {
    $('#renTable').querySelectorAll('input.ren').forEach(c => c.checked = all.checked);
    updateRenApply();
  };
  $('#renTable').querySelectorAll('input.ren').forEach(c => c.onchange = updateRenApply);
  updateRenApply();
}

// Las avisadas son las que hay que mirar una a una; poder quitarlas todas de
// golpe permite aplicar lo evidente y dejarlas para después.
$('#renDropWarned').onclick = () => {
  $('#renTable').querySelectorAll('input.ren.warned').forEach(c => c.checked = false);
  const all = $('#renAll');
  if (all) all.checked = false;
  updateRenApply();
};

function updateRenApply() {
  const n = $('#renTable').querySelectorAll('input.ren:checked').length;
  $('#renApply').disabled = !n;
  $('#renApply').textContent = n ? `Renombrar ${n} carpeta(s)` : 'Nada que renombrar';
}

$('#renApply').onclick = async () => {
  const picks = [...$('#renTable').querySelectorAll('input.ren:checked')].map(c => ren.items[Number(c.dataset.i)]);
  if (!picks.length) return;
  const btn = $('#renApply');
  btn.disabled = true; btn.textContent = 'renombrando…';
  $('#renErr').textContent = '';
  try {
    const n = await App().RenameApply(picks);
    lib.cards = {};
    // Las renombradas ya están en otra ruta: se rehace el plan con las
    // nuevas para que la tabla no señale a carpetas que ya no existen.
    // Se pasan la ruta vieja y la nueva de cada una: Go se salta las que ya
    // no existen, así que las que fallaron siguen saliendo en la tabla.
    const chosen = new Set(picks.map(it => it.dir));
    ren.dirs = ren.items.flatMap(it => chosen.has(it.dir)
      ? [it.dir, it.dir.slice(0, Math.max(it.dir.lastIndexOf('\\'), it.dir.lastIndexOf('/')) + 1) + it.target]
      : [it.dir]);
    await renPlan();
    if (!ren.items.some(it => !it.skip)) { $('#renTable').innerHTML = '<p class="empty">Listo: todas las carpetas se llaman como les toca.</p>'; }
    libLog(`Renombradas ${n} carpeta(s)`);
  } catch (e) { $('#renErr').textContent = e; btn.disabled = false; btn.textContent = 'Renombrar'; }
};

// ---- ordenar por artista: Colección\Artista\Disco -------------------------
// Go propone (plan) y aquí se enseña antes de mover nada. La colección es la
// carpeta de la Biblioteca salvo que se cambie.
const org = { root: '', items: [] };

$('#libOrganize').onclick = async () => {
  const dirs = selectedDirs();
  if (!dirs.length) { $('#libBatchErr').textContent = 'Marca los discos que quieres ordenar.'; return; }
  $('#libBatchErr').textContent = '';
  closeDetail();
  $('#libList').hidden = true;
  $('#libOrgView').hidden = false;
  org.root = org.root || lib.root;
  org.dirs = dirs;
  await orgPlan();
};
$('#orgBack').onclick = () => { $('#libOrgView').hidden = true; $('#libList').hidden = false; loadAlbums(); };
$('#orgChoose').onclick = async () => {
  try {
    const d = await App().ChooseLibraryFolder();
    if (d) { org.root = d; await orgPlan(); }
  } catch (e) { $('#orgErr').textContent = 'No pude abrir el diálogo: ' + e; }
};

async function orgPlan() {
  $('#orgRoot').textContent = $('#orgRoot').title = org.root;
  $('#orgErr').textContent = '';
  $('#orgMeta').textContent = 'mirando de qué artista es cada disco…';
  $('#orgTable').innerHTML = '';
  $('#orgApply').disabled = true;
  try { org.items = await App().OrganizePlan(org.dirs, org.root) || []; }
  catch (e) { org.items = []; $('#orgMeta').textContent = ''; $('#orgErr').textContent = e; return; }
  renderOrgPlan();
}

function renderOrgPlan() {
  const moves = org.items.filter(it => !it.skip);
  const newArtists = new Set(moves.filter(it => it.newArtist).map(it => it.artist));
  $('#orgMeta').textContent = `${moves.length} de ${org.items.length} discos se moverían · ${newArtists.size} carpeta(s) de artista nueva(s)`;
  $('#orgApply').disabled = !moves.length;
  $('#orgApply').textContent = moves.length ? `Mover ${moves.length} disco(s)` : 'Nada que mover';
  if (!org.items.length) { $('#orgTable').innerHTML = '<p class="empty">Nada que ordenar.</p>'; return; }
  $('#orgTable').innerHTML = table(['Disco', 'Artista', 'Destino'], org.items.map(it => [
    `<td>${esc(it.name)}<br><span class="dim mono">${esc(it.dir)}</span></td>`,
    `<td>${it.artist ? `<b>${esc(it.artist)}</b> <span class="dim">(${esc(it.source)})</span>` : '<span class="dim">?</span>'}${it.newArtist && !it.skip ? '<br><span class="warn">carpeta nueva</span>' : ''}</td>`,
    it.skip ? `<td class="dim">se queda: ${esc(it.skip)}</td>` : `<td class="ok">${esc(it.target)}</td>`,
  ]));
}

$('#orgApply').onclick = async () => {
  const btn = $('#orgApply');
  btn.disabled = true; btn.textContent = 'moviendo…';
  $('#orgErr').textContent = '';
  try {
    const n = await App().OrganizeApply(org.items);
    // Los movidos ya no cuelgan de la carpeta de la Biblioteca: se
    // reordena el plan con lo que quede.
    org.dirs = org.items.filter(it => it.skip).map(it => it.dir);
    lib.cards = {};
    if (org.dirs.length) await orgPlan(); else { $('#orgMeta').textContent = `${n} disco(s) movidos`; $('#orgTable').innerHTML = '<p class="empty">Todo ordenado.</p>'; btn.textContent = 'Nada que mover'; }
  } catch (e) { $('#orgErr').textContent = e; btn.disabled = false; btn.textContent = 'Mover'; }
};

// ---- carátulas en las listas de ediciones --------------------------------
// Deezer, Bandcamp y Discogs traen la URL de su carátula; para MusicBrainz
// se pide a Cover Art Archive por el ID (si no la tiene, por el grupo).
function coverURL(r, big) {
  if (r.coverUrl) return big ? r.coverUrl : r.coverUrl.replace(/1000x1000/, '250x250').replace(/_10\.jpg$/, '_7.jpg');
  if (r.source === 'musicbrainz' && r.id) return `https://coverartarchive.org/release/${r.id}/front-${big ? 500 : 250}`;
  return '';
}
function thumbCell(r) {
  const u = coverURL(r, false);
  if (!u) return '<td class="thumbcell"><span class="mini none">♪</span></td>';
  const alt = r.source === 'musicbrainz' && r.releaseGroupId ? `https://coverartarchive.org/release-group/${r.releaseGroupId}/front-250` : '';
  return `<td class="thumbcell"><img class="mini" src="${esc(u)}" loading="lazy" alt="" data-alt="${esc(alt)}" onerror="miniFail(this)"></td>`;
}
// Sin imagen: probar la del grupo de ediciones y, si tampoco, una nota.
window.miniFail = img => {
  if (img.dataset.alt) { img.src = img.dataset.alt; img.dataset.alt = ''; return; }
  img.replaceWith(Object.assign(document.createElement('span'), { className: 'mini none', textContent: '♪' }));
};

// ---- llevarse discos: copiar al USB del coche ----------------------------
// Con los discos marcados (o todos), a la carpeta que se elija: Go propone
// el plan (carpeta de destino de cada uno, pistas, cuántas se convierten a
// MP3, si cabe) y aquí se enseña antes de copiar. El destino y las opciones
// se recuerdan.
const carry = { dirs: [], plan: null, running: false };
try { Object.assign(carry, JSON.parse(localStorage.getItem('carry') || '{}')); } catch (e) {}

const carryOpts = () => ({
  dirs: carry.dirs, dest: carry.dest || '', layout: $('#carryLayout').value,
  convert: $('#carryConvert').checked, format: $('#carryFormat').value, replace: $('#carryReplace').checked,
});
function carryRemember() {
  const o = carryOpts();
  try { localStorage.setItem('carry', JSON.stringify({ dest: o.dest, layout: o.layout, convert: o.convert, format: o.format })); } catch (e) {}
}
const gb = n => n < 0 ? '?' : n >= 1e9 ? (n / 1e9).toFixed(2) + ' GB' : (n / 1e6).toFixed(0) + ' MB';

$('#libCarry').onclick = async () => {
  const sel = selectedDirs();
  carry.dirs = sel.length ? sel : lib.albums.map(a => a.dir);
  if (!carry.dirs.length) { $('#libBatchErr').textContent = 'No hay discos.'; return; }
  $('#libBatchErr').textContent = '';
  closeDetail();
  $('#libList').hidden = true;
  $('#libCarryView').hidden = false;
  $('#carryLayout').value = carry.layout || 'flat';
  $('#carryConvert').checked = carry.convert !== false;
  $('#carryFormat').value = carry.format || 'mp3-320';
  $('#carryMeta').textContent = `${carry.dirs.length} disco(s)${sel.length ? ' marcados' : ''}`;
  if (carry.dest) await carryPlan(); else $('#carryTable').innerHTML = '<p class="empty">Elige la carpeta de destino.</p>';
};
$('#carryBack').onclick = () => { if (carry.running) return; $('#libCarryView').hidden = true; $('#libList').hidden = false; };
$('#carryChoose').onclick = async () => {
  try {
    const d = await App().ChooseCarryFolder(carry.dest || '');
    if (d) { carry.dest = d; carryRemember(); await carryPlan(); }
  } catch (e) { $('#carryErr').textContent = 'No pude abrir el diálogo: ' + e; }
};
['#carryLayout', '#carryConvert', '#carryFormat', '#carryReplace'].forEach(s => $(s).onchange = () => { carryRemember(); if (carry.dest) carryPlan(); });

async function carryPlan() {
  $('#carryDest').textContent = $('#carryDest').title = carry.dest;
  $('#carryErr').textContent = '';
  $('#carryFree').textContent = '';
  $('#carryTable').innerHTML = '';
  $('#carryApply').disabled = true;
  $('#carryMeta').textContent = 'mirando qué hay que copiar…';
  try { carry.plan = await App().CarryPlan(carryOpts()); }
  catch (e) { carry.plan = null; $('#carryMeta').textContent = ''; $('#carryErr').textContent = e; return; }
  renderCarryPlan();
}

function renderCarryPlan() {
  const p = carry.plan;
  const go = p.items.filter(it => !it.skip);
  const conv = go.reduce((n, it) => n + it.convert, 0);
  $('#carryMeta').textContent = `${go.length} de ${p.items.length} discos · ${p.files} pistas${conv ? ` (${conv} a MP3)` : ''} · ${gb(p.bytes)}${conv ? ' aprox.' : ''}`;
  $('#carryFree').textContent = p.free < 0 ? '' : `libres ${gb(p.free)}` + (p.fits ? '' : ' · NO CABE');
  $('#carryFree').className = 'meta' + (p.fits ? '' : ' warn');
  $('#carryApply').disabled = !go.length || !p.fits;
  $('#carryApply').textContent = go.length ? `Copiar ${go.length} disco(s)` : 'Nada que copiar';
  if (!p.items.length) { $('#carryTable').innerHTML = '<p class="empty">Nada que copiar.</p>'; return; }
  $('#carryTable').innerHTML = table(['Disco', 'Pistas', 'Destino'], p.items.map(it => [
    `<td>${esc(it.name)}<br><span class="dim mono">${esc(it.dir)}</span></td>`,
    `<td>${it.files}${it.convert ? ` <span class="dim">(${it.convert} a MP3)</span>` : ''}<br><span class="dim">${gb(it.bytes)}</span></td>`,
    it.skip ? `<td class="dim">se salta: ${esc(it.skip)}</td>` : `<td class="ok">${esc(it.target)}${it.exists ? ' <span class="warn">(otra vez)</span>' : ''}</td>`,
  ]));
}

$('#carryApply').onclick = async () => {
  const btn = $('#carryApply');
  btn.disabled = true; btn.textContent = 'copiando…';
  $('#carryCancel').hidden = false;
  $('#carryProgress').hidden = false;
  $('#carryErr').textContent = '';
  carry.running = true;
  try {
    const p = await App().CarryApply(carryOpts());
    $('#carryProgressMsg').textContent = `hecho: ${p.done} pistas${p.errors ? `, ${p.errors} error(es) (mira el registro)` : ''}`;
  } catch (e) { $('#carryErr').textContent = e; }
  carry.running = false;
  $('#carryCancel').hidden = true;
  await carryPlan();
};
$('#carryCancel').onclick = () => App().CancelCarry();
window.runtime.EventsOn('carry', p => {
  const bar = $('#carryProgress');
  bar.hidden = false;
  bar.querySelector('i').style.width = (p.total ? p.done * 100 / p.total : 0) + '%';
  if (p.running) $('#carryProgressMsg').textContent = `${p.done} / ${p.total} · ${p.album}${p.file ? ' · ' + p.file : ''}`;
});
