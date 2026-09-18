// Pestaña Reproductor y barra de reproducción. El audio lo pone el <audio>
// de la página: cada pista se pide a Go por /media?p=<ruta> (main.go,
// player.go) con fetch, se guarda en memoria como Blob y se reproduce desde
// ahí. Así no depende de cómo intercepte WebView2 las peticiones del
// elemento <audio>, y saltar dentro de la pista es inmediato.
// La cola vive aquí (y se guarda en localStorage para recuperarla al abrir
// la app). Los demás módulos llaman a playFile(f, dir) para escuchar un
// fichero desde donde estén.
// Usa App, $, esc, fmtDur (definidos en downloads.js y library.js).

const pl = {
  root: '',
  albums: [],   // playerAlbum de Go (con sus pistas por nombre de fichero)
  cards: {},    // dir -> albumCard (carátula y nombres)
  crops: {},    // dir -> encuadre manual de la carátula en la cartulina (fracciones)
  tracks: {},   // dir -> localTrack[] (etiquetas y duraciones, al abrir un disco)
  sel: '',      // disco abierto en el panel lateral
  queue: [],    // pistas: {path, dir, title, artist, album, duration}
  idx: -1,      // pista en curso dentro de la cola
  filter: '',
  loaded: false,
  loadingCards: false,
  favs: new Set(), // carpetas marcadas con ♥ (gramola)
  view: 'grid',   // grid | favs
  fav: 'jukebox', // visualización de favoritos: jukebox | deck
  jb: { list: [], i: 0, busy: false, key: '' }, // estado de la gramola (key: letra pulsada en el teclado)
};
const audio = $('#audio');
let blobURL = '';     // objeto URL de la pista cargada
let loadingPath = ''; // pista que se está pidiendo (para ignorar respuestas tardías)

const mediaURL = p => '/media?p=' + encodeURIComponent(p);
const noExt = n => n.replace(/\.[^.]+$/, '');
const dirOf = p => p.replace(/[\\/][^\\/]+$/, '');
const joinPath = (dir, name) => dir + (dir.includes('/') && !dir.includes('\\') ? '/' : '\\') + name;
// "01 - Título" o "01. Título" en la etiqueta: fuera la numeración.
const cleanTitle = t => (t || '').replace(/^\s*\d{1,3}\s*[-._)]?\s+/, '');
function libLogSafe(line) { if (window.libLog) libLog(line); }
const albumByDir = dir => pl.albums.find(a => a.dir === dir);
const artistOf = a => (pl.cards[a.dir] && pl.cards[a.dir].artist) || a.artist;
const albumOf = a => (pl.cards[a.dir] && pl.cards[a.dir].album) || a.album || a.name;
const coverOfDir = dir => (pl.cards[dir] || {}).cover || '';

// trackOf: de un localTrack de Go (o de un nombre de fichero) a una entrada
// de la cola. La carátula no se guarda: sale de la tarjeta del disco.
function trackOf(f, dir) {
  const a = albumByDir(dir) || { dir, artist: '', album: '' };
  return {
    path: f.path || joinPath(dir, f.name), dir, duration: f.duration || 0,
    title: cleanTitle(f.title) || noExt(f.name),
    artist: f.artist || f.albumArtist || artistOf(a) || '',
    album: f.album || albumOf(a) || '',
  };
}

// ---- reproducción -----------------------------------------------------------

function renderNow() {
  const t = pl.queue[pl.idx];
  $('#nowbar').hidden = !t && !pl.queue.length;
  if (!t) { $('#nbTitle').textContent = ''; $('#nbSub').textContent = ''; document.title = 'Trovador'; return; }
  $('#nbTitle').textContent = t.title;
  $('#nbSub').textContent = t.error ? t.error : [t.artist, t.album].filter(Boolean).join(' — ');
  const cover = coverOfDir(t.dir);
  $('#nbCover').hidden = !cover; $('#nbPh').hidden = !!cover;
  if (cover) $('#nbCover').src = cover;
  $('#nbPlay').textContent = audio.paused ? '▶' : '⏸';
  document.title = `${audio.paused ? '' : '▶ '}${t.title} · Trovador`;
}

// play carga la pista i (fetch → Blob) y la reproduce.
async function play(i) {
  if (i < 0 || i >= pl.queue.length) { audio.pause(); pl.idx = -1; renderNow(); renderQueue(); return; }
  pl.idx = i;
  const t = pl.queue[i];
  t.error = '';
  loadingPath = t.path;
  audio.pause();
  renderNow(); renderQueue(); saveQueue();
  const el = $('#plQueue').querySelector('.q.now');
  if (el) el.scrollIntoView({ block: 'nearest' });
  $('#nbSub').textContent = 'cargando…';
  try {
    const resp = await fetch(mediaURL(t.path));
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const blob = await resp.blob();
    if (loadingPath !== t.path) return; // el usuario ya pidió otra
    if (blobURL) URL.revokeObjectURL(blobURL);
    blobURL = URL.createObjectURL(blob);
    audio.src = blobURL;
    await audio.play();
  } catch (e) {
    if (loadingPath !== t.path) return;
    t.error = 'no se puede reproducir: ' + (e && e.message ? e.message : e);
    libLogSafe(`reproductor: ${t.path}: ${t.error}`);
  }
  renderNow(); renderQueue(); renderAlbumPanel(); renderDeck(); renderDeckState(); renderGramoState(); renderCarouselState();
}
const next = () => play(pl.idx + 1);
const prev = () => (audio.currentTime > 3 ? (audio.currentTime = 0) : play(pl.idx - 1));
function togglePlay() {
  if (pl.idx < 0) { if (pl.queue.length) play(0); return; }
  if (!audio.src) { play(pl.idx); return; }
  if (audio.paused) audio.play(); else audio.pause();
}

let seeking = false;
audio.onended = next;
audio.onplay = audio.onpause = () => { renderNow(); renderQueue(); renderDeck(); renderDeckState(); renderGramoState(); renderCarouselState(); };
audio.onerror = () => {
  const t = pl.queue[pl.idx];
  if (t) { t.error = 'formato no reproducible'; renderNow(); }
};
audio.onloadedmetadata = () => {
  const t = pl.queue[pl.idx];
  if (t && audio.duration && !t.duration) { t.duration = audio.duration; renderQueue(); saveQueue(); }
};
audio.ontimeupdate = () => {
  if (!audio.duration) return;
  if (!seeking) $('#nbSeek').value = Math.round(audio.currentTime / audio.duration * 1000);
  $('#nbCur').textContent = fmtDur(audio.currentTime);
  $('#nbDur').textContent = fmtDur(audio.duration);
};
$('#nbSeek').oninput = () => { seeking = true; if (audio.duration) $('#nbCur').textContent = fmtDur($('#nbSeek').value / 1000 * audio.duration); };
$('#nbSeek').onchange = () => { seeking = false; if (audio.duration) audio.currentTime = $('#nbSeek').value / 1000 * audio.duration; };
$('#nbVol').oninput = () => { audio.volume = $('#nbVol').value / 100; try { localStorage.setItem('plVol', $('#nbVol').value); } catch (e) {} };
$('#nbPlay').onclick = togglePlay;
$('#nbNext').onclick = next;
$('#nbPrev').onclick = prev;
// Espacio = reproducir/pausa cuando no se está escribiendo.
document.addEventListener('keydown', e => {
  if (e.code === 'Space' && !/INPUT|TEXTAREA|SELECT|BUTTON/.test(e.target.tagName) && pl.queue.length) { e.preventDefault(); togglePlay(); }
});
try { $('#nbVol').value = localStorage.getItem('plVol') || 80; } catch (e) {}
audio.volume = $('#nbVol').value / 100;

// ---- la cola ----------------------------------------------------------------

function renderQueue() {
  const box = $('#plQueue');
  const total = pl.queue.reduce((a, t) => a + (t.duration || 0), 0);
  $('#plQueueMeta').textContent = pl.queue.length ? `${pl.queue.length} pista(s) · ${fmtDur(total)}` : 'Vacía. Busca una canción o abre un disco y pulsa +.';
  $('#plClear').disabled = $('#plShuffle').disabled = !pl.queue.length;
  if (!pl.queue.length) { box.innerHTML = ''; return; }
  let lastKey = null;
  box.innerHTML = pl.queue.map((t, i) => {
    const key = t.artist + '|' + t.album;
    const head = key !== lastKey ? `<div class="qh">${esc([t.artist, t.album].filter(Boolean).join(' — ') || 'sueltas')}</div>` : '';
    lastKey = key;
    const now = i === pl.idx;
    return head + `<div class="q ${now ? 'now' : ''} ${now && loadingPath === t.path && !audio.src ? 'loading' : ''}" data-i="${i}" title="${esc(t.path)}">
      <span class="n">${now ? (t.error ? '!' : audio.paused ? '❚❚' : '▶') : i + 1}</span>
      <div class="i"><b>${esc(t.title)}</b><span>${esc(t.error || t.artist)}</span></div>
      ${t.error || !playable(t.path) ? `<button class="conv" title="convertir a ${esc(convLabel())} y volver a intentarlo">convertir</button>` : `<span class="d">${t.duration ? fmtDur(t.duration) : ''}</span>`}
      <button class="x" title="quitar de la lista">✕</button>
    </div>`;
  }).join('');
  box.querySelectorAll('.q').forEach(row => {
    row.onclick = () => play(Number(row.dataset.i));
    row.querySelector('.x').onclick = e => { e.stopPropagation(); removeAt(Number(row.dataset.i)); };
    const conv = row.querySelector('.conv');
    if (conv) conv.onclick = e => { e.stopPropagation(); const t = pl.queue[Number(row.dataset.i)]; convertPath(t.path, t.dir, conv); };
  });
  markInQueue();
}

function removeAt(i) {
  const wasCurrent = i === pl.idx;
  pl.queue.splice(i, 1);
  if (i < pl.idx) pl.idx--;
  if (wasCurrent) {
    audio.pause(); audio.removeAttribute('src');
    pl.idx = Math.min(i, pl.queue.length - 1);
    if (pl.idx >= 0) play(pl.idx);
  }
  renderNow(); renderQueue(); saveQueue();
}

// enqueue añade pistas al final; con playNow van delante de lo que quede
// por sonar y empiezan ya.
function enqueue(tracks, playNow) {
  if (!tracks.length) return;
  const wasEmpty = !pl.queue.length;
  if (playNow) {
    const at = pl.idx + 1;
    pl.queue.splice(at, 0, ...tracks);
    play(at);
  } else {
    pl.queue.push(...tracks);
    if (wasEmpty) play(0); else { renderQueue(); saveQueue(); }
  }
}

$('#plClear').onclick = () => {
  audio.pause(); audio.removeAttribute('src');
  pl.queue = []; pl.idx = -1;
  renderNow(); renderQueue(); saveQueue();
  $('#nowbar').hidden = true; document.title = 'Trovador';
};
$('#plShuffle').onclick = () => {
  const cur = pl.queue[pl.idx];
  const rest = pl.queue.filter((t, i) => i !== pl.idx);
  for (let i = rest.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [rest[i], rest[j]] = [rest[j], rest[i]]; }
  pl.queue = cur ? [cur, ...rest] : rest;
  pl.idx = cur ? 0 : -1;
  renderQueue(); saveQueue();
};

function saveQueue() {
  try {
    const queue = pl.queue.map(({ path, dir, title, artist, album, duration }) => ({ path, dir, title, artist, album, duration }));
    localStorage.setItem('plQueue', JSON.stringify({ queue, idx: pl.idx }));
  } catch (e) {}
}
function restoreQueue() {
  try {
    const s = JSON.parse(localStorage.getItem('plQueue') || 'null');
    if (s && Array.isArray(s.queue)) { pl.queue = s.queue; pl.idx = Math.min(s.idx, pl.queue.length - 1); }
  } catch (e) {}
  if (!pl.queue.length) return;
  // Recuperada la cola, pero sin sonar: se deja preparada en la pista que iba.
  if (pl.idx < 0) pl.idx = 0;
  renderNow(); renderQueue();
}

// ---- escuchar desde otras pestañas ----------------------------------------
// playFile(f, dir): f es un localTrack de Go. Se pone delante de lo que
// quede por sonar y empieza ya; la lista sigue después.
window.playFile = (f, dir) => enqueue([trackOf(f, dir || dirOf(f.path))], true);

// ---- pestaña ------------------------------------------------------------------

window.plOnShow = async () => { if (!pl.loaded) await loadPlayer(); };

async function loadPlayer() {
  $('#plMeta').textContent = 'leyendo…';
  try {
    pl.albums = await App().PlayerAlbums() || [];
    pl.root = (await App().State()).settings.collection || settings.outDir;
  } catch (e) { $('#plGrid').innerHTML = `<p class="empty warn">${esc(String(e))}</p>`; $('#plMeta').textContent = ''; return; }
  pl.loaded = true;
  $('#plRoot').textContent = $('#plRoot').title = pl.root;
  for (const a of pl.albums) if (a.known) pl.cards[a.dir] = { cover: a.cover, artist: a.artist, album: a.album, date: a.date };
  try { pl.favs = new Set(await App().Favorites() || []); } catch (e) {}
  try { pl.crops = await App().CoverCrops() || {}; } catch (e) {}
  render();
  renderSongs();
  loadPlayerCards();
}
$('#plRefresh').onclick = () => { pl.loaded = false; pl.tracks = {}; loadPlayer(); };
$('#plChoose').onclick = async () => {
  try {
    const d = await App().ChooseCollectionFolder();
    if (d) { settings.collection = d; pl.loaded = false; pl.cards = {}; pl.tracks = {}; pl.sel = ''; renderAlbumPanel(); await loadPlayer(); }
  } catch (e) { $('#plMeta').textContent = 'No pude abrir el diálogo: ' + e; }
};

// ---- búsqueda ---------------------------------------------------------------
// Filtra la cuadrícula y, debajo de la caja, propone las canciones que
// cuadran para añadirlas una a una.

const words = () => pl.filter.split(/\s+/).filter(Boolean);
const matches = (hay, ws) => ws.every(w => hay.includes(w));

$('#plFilter').oninput = () => {
  pl.filter = $('#plFilter').value.trim().toLowerCase();
  $('#plFilterClear').hidden = !pl.filter;
  renderPlayerGrid(); renderSongs();
};
$('#plFilter').onkeydown = e => { if (e.key === 'Escape') { clearFilter(); } };
$('#plFilter').onfocus = () => renderSongs();
$('#plFilterClear').onclick = clearFilter;
function clearFilter() { $('#plFilter').value = ''; pl.filter = ''; $('#plFilterClear').hidden = true; renderPlayerGrid(); renderSongs(); }
// El desplegable de canciones se cierra al pulsar fuera.
document.addEventListener('mousedown', e => { if (!e.target.closest('.player-search')) $('#plSongs').hidden = true; });

// visibleAlbums: los discos que pasan el filtro, con las pistas que
// cuadran por título (null si el disco cuadra por nombre).
function visibleAlbums() {
  const ws = words();
  if (!ws.length) return pl.albums.map(a => ({ a, hits: null }));
  const out = [];
  for (const a of pl.albums) {
    const hay = `${artistOf(a)} ${albumOf(a)} ${a.name}`.toLowerCase();
    if (matches(hay, ws)) { out.push({ a, hits: null }); continue; }
    const hits = (a.tracks || []).filter(t => matches(`${t.title} ${artistOf(a)} ${albumOf(a)}`.toLowerCase(), ws));
    if (hits.length) out.push({ a, hits });
  }
  return out;
}

// renderSongs: el desplegable con las canciones que cuadran con lo escrito.
function renderSongs() {
  const box = $('#plSongs');
  const ws = words();
  if (!ws.length) { box.hidden = true; box.innerHTML = ''; return; }
  const rows = [];
  for (const a of pl.albums) {
    const artist = artistOf(a), album = albumOf(a);
    for (const t of a.tracks || []) {
      if (matches(`${t.title} ${artist} ${album}`.toLowerCase(), ws)) rows.push({ a, t, artist, album });
      if (rows.length > 60) break;
    }
    if (rows.length > 60) break;
  }
  if (!rows.length) { box.hidden = true; box.innerHTML = ''; return; }
  const shown = rows.slice(0, 10);
  box.innerHTML = `<div class="sh">Canciones</div>` + shown.map((r, i) => {
    const cover = coverOfDir(r.a.dir);
    return `<div class="s" data-i="${i}">
      ${cover ? `<img src="${cover}" alt="">` : '<div class="ph">♪</div>'}
      <div class="i"><b>${esc(r.t.title)}</b><span>${esc(r.artist)} — ${esc(r.album)}</span></div>
      <div class="acts"><button class="tiny go1" title="reproducir ya">▶</button><button class="tiny add1" title="añadir a la lista">+</button></div>
    </div>`;
  }).join('') + (rows.length > shown.length ? `<div class="more">${rows.length > 60 ? 'más de 60' : rows.length} canciones cuadran: escribe más para acotar</div>` : '');
  box.hidden = false;
  box.querySelectorAll('.s').forEach(el => {
    const r = shown[Number(el.dataset.i)];
    el.querySelector('.go1').onclick = () => enqueue([trackOf(r.t, r.a.dir)], true);
    el.querySelector('.add1').onclick = () => enqueue([trackOf(r.t, r.a.dir)], false);
    el.querySelector('.i').onclick = () => openAlbum(r.a.dir);
  });
}

// ---- cuadrícula -------------------------------------------------------------

function renderPlayerGrid() {
  const list = visibleAlbums();
  $('#plMeta').textContent = `${list.length}${list.length !== pl.albums.length ? ' de ' + pl.albums.length : ''} discos`;
  const box = $('#plGrid');
  if (!list.length) { box.innerHTML = '<p class="empty">Ningún disco cuadra.</p>'; return; }
  box.innerHTML = list.map(({ a, hits }) => {
    const card = pl.cards[a.dir];
    const cover = card ? card.cover : '';
    return `<div class="pcard ${a.dir === pl.sel ? 'sel' : ''} ${pl.favs.has(a.dir) ? 'isfav' : ''}" data-dir="${esc(a.dir)}" title="${esc(a.name)} · ${a.files} pista(s)">
      ${cover ? `<img class="art" src="${cover}" alt="" loading="lazy">` : `<div class="art none">${card ? '♪' : '…'}</div>`}
      <button class="ov go" title="reproducir el disco ya">▶</button>
      <button class="ov add" title="añadir el disco a la lista">+</button>
      <button class="ov fav" title="favorito (gramola)">♥</button>
      <div class="t">${esc(albumOf(a))}</div><div class="a">${esc(artistOf(a))}</div>
      ${hits ? `<div class="hit" title="${esc(hits.map(h => h.title).join(', '))}">♪ ${esc(hits[0].title)}${hits.length > 1 ? ` +${hits.length - 1}` : ''}</div>` : ''}
    </div>`;
  }).join('');
  box.querySelectorAll('.pcard').forEach(c => {
    c.onclick = () => openAlbum(c.dataset.dir);
    c.querySelector('.go').onclick = e => { e.stopPropagation(); addAlbum(c.dataset.dir, true); };
    c.querySelector('.add').onclick = e => { e.stopPropagation(); addAlbum(c.dataset.dir, false); };
    c.querySelector('.fav').onclick = e => { e.stopPropagation(); toggleFav(c.dataset.dir); };
  });
  markInQueue();
}

function markInQueue() {
  const dirs = new Set(pl.queue.map(t => t.dir));
  $('#plGrid').querySelectorAll('.pcard').forEach(c => c.classList.toggle('inqueue', dirs.has(c.dataset.dir)));
}

async function addAlbum(dir, playNow) {
  await ensureTracks(dir);
  const files = pl.tracks[dir] && pl.tracks[dir].length ? pl.tracks[dir] : (albumByDir(dir) || {}).tracks || [];
  enqueue(files.map(f => trackOf(f, dir)), playNow);
}

// ensureTracks trae etiquetas y duraciones de un disco (una vez).
async function ensureTracks(dir) {
  if (pl.tracks[dir]) return;
  try { pl.tracks[dir] = await App().PlayerTracks(dir) || []; }
  catch (e) { pl.tracks[dir] = []; }
  if (pl.sel === dir) renderAlbumPanel();
}

// ---- panel del disco (entre la cuadrícula y la lista) -----------------------

function openAlbum(dir) {
  pl.sel = pl.sel === dir ? '' : dir;
  $('#plGrid').querySelectorAll('.pcard').forEach(c => c.classList.toggle('sel', c.dataset.dir === pl.sel));
  renderAlbumPanel();
  if (pl.sel) ensureTracks(pl.sel);
}

function renderAlbumPanel() {
  const box = $('#plAlbum');
  const a = albumByDir(pl.sel);
  box.hidden = !a;
  if (!a) { box.innerHTML = ''; return; }
  const card = pl.cards[a.dir] || {};
  const tracks = pl.tracks[a.dir];
  const list = tracks || a.tracks || [];
  const hitNames = new Set(((visibleAlbums().find(x => x.a.dir === a.dir) || {}).hits || []).map(h => h.name));
  const nowPath = pl.queue[pl.idx] ? pl.queue[pl.idx].path : '';
  const total = list.reduce((s, t) => s + (t.duration || 0), 0);
  box.innerHTML = `
    <button class="ghost close" title="cerrar">✕</button>
    ${card.cover ? `<img class="cover" src="${card.cover}" alt="">` : '<div class="cover none">♪</div>'}
    <h3>${esc(albumOf(a))}</h3>
    <div class="sub">${esc(artistOf(a))}${card.date ? ' · ' + esc(String(card.date).slice(0, 4)) : ''} · ${a.files} pistas${total ? ' · ' + fmtDur(total) : ''}</div>
    <div class="acts"><button class="goall">▶ Reproducir</button><button class="ghost addall">+ Añadir a la lista</button><button class="ghost favbtn" title="favorito (gramola)" style="flex:0 0 auto;color:${pl.favs.has(a.dir) ? 'var(--accent)' : 'inherit'}">♥</button></div>
    ${list.some(t => !playable(t.name)) ? `<p class="noplay" style="margin:0 0 6px">Hay pistas en un formato que la ventana no reproduce (${esc([...new Set(list.filter(t => !playable(t.name)).map(t => extOf(t.name)))].join(', '))}): <button class="tiny convall">convertir el disco a ${esc(convLabel())}</button></p>` : ''}
    <table class="tl"><tbody>${list.map((t, i) => {
      const path = t.path || joinPath(a.dir, t.name);
      const ok = playable(t.name);
      return `<tr class="${hitNames.has(t.name) ? 'hit' : ''} ${path === nowPath ? 'now' : ''}" data-i="${i}">
        <td class="n">${i + 1}</td><td>${esc(cleanTitle(t.title) || noExt(t.name))}${ok ? '' : ` <span class="noplay" title="la ventana no reproduce este formato">${esc(extOf(t.name))}</span>`}</td>
        <td class="d">${t.duration ? fmtDur(t.duration) : ''}</td>
        <td class="acts"><button class="tiny go1" title="reproducir ya">▶</button> <button class="tiny add1" title="añadir a la lista">+</button> <button class="tiny conv1" title="convertir a ${esc(convLabel())}" ${ok ? '' : 'style="visibility:visible;color:var(--run)"'}>⇄</button></td></tr>`;
    }).join('')}</tbody></table>
    ${tracks ? '' : '<p class="meta" style="margin:6px 0 0">leyendo duraciones…</p>'}`;
  box.querySelector('.close').onclick = () => openAlbum(pl.sel);
  box.querySelector('.goall').onclick = () => addAlbum(a.dir, true);
  box.querySelector('.addall').onclick = () => addAlbum(a.dir, false);
  box.querySelector('.favbtn').onclick = () => toggleFav(a.dir);
  const convall = box.querySelector('.convall');
  if (convall) convall.onclick = () => convertAlbum(a.dir, convall);
  box.querySelectorAll('tr[data-i]').forEach(tr => {
    const t = () => (pl.tracks[a.dir] || a.tracks || [])[Number(tr.dataset.i)];
    tr.querySelector('.go1').onclick = () => enqueue([trackOf(t(), a.dir)], true);
    tr.querySelector('.add1').onclick = () => enqueue([trackOf(t(), a.dir)], false);
    tr.querySelector('.conv1').onclick = () => convertPath(t().path || joinPath(a.dir, t().name), a.dir, tr.querySelector('.conv1'));
    tr.ondblclick = () => enqueue([trackOf(t(), a.dir)], true);
  });
}

// ---- convertir de formato -----------------------------------------------------
// Lo que la ventana (Chromium) sabe reproducir; el resto se convierte con
// ffmpeg desde aquí (Go: convert.go). El original se aparta a _original.
const PLAYABLE = new Set(['mp3', 'flac', 'ogg', 'opus', 'wav', 'm4a']);
const extOf = n => (n.match(/\.([^.]+)$/) || ['', ''])[1].toLowerCase();
const playable = n => PLAYABLE.has(extOf(n));
const convLabel = () => $('#plConvFmt').selectedOptions[0].textContent;
try { $('#plConvFmt').value = localStorage.getItem('plConvFmt') || 'mp3-320'; } catch (e) {}
$('#plConvFmt').onchange = () => { try { localStorage.setItem('plConvFmt', $('#plConvFmt').value); } catch (e) {} renderAlbumPanel(); };

// Tras convertir: la pista cambia de ruta en la cola, y si era la que
// sonaba (o la que no podía sonar), se vuelve a lanzar.
function replacePath(oldPath, newPath) {
  let replay = false;
  pl.queue.forEach((t, i) => {
    if (t.path === oldPath) { t.path = newPath; t.error = ''; if (i === pl.idx) replay = true; }
  });
  saveQueue();
  if (replay) play(pl.idx); else renderQueue();
}

async function convertPath(path, dir, btn) {
  const fmt = $('#plConvFmt').value;
  if (btn) { btn.disabled = true; btn.textContent = '…'; }
  try {
    const out = await App().ConvertTrack(path, fmt);
    delete pl.tracks[dir];
    replacePath(path, out);
    await ensureTracks(dir);
    const a = albumByDir(dir);
    if (a) a.tracks = (pl.tracks[dir] || []).map(t => ({ name: t.name, title: cleanTitle(t.title) || noExt(t.name) }));
    renderAlbumPanel();
  } catch (e) {
    $('#plMeta').textContent = String(e);
    libLogSafe('convertir: ' + e);
    if (btn) { btn.disabled = false; btn.textContent = '⇄'; }
  }
}

async function convertAlbum(dir, btn) {
  const fmt = $('#plConvFmt').value;
  if (btn) { btn.disabled = true; btn.textContent = 'convirtiendo…'; }
  try {
    const outs = await App().ConvertAlbum(dir, fmt) || [];
    delete pl.tracks[dir];
    await ensureTracks(dir);
    const a = albumByDir(dir);
    if (a) a.tracks = (pl.tracks[dir] || []).map(t => ({ name: t.name, title: cleanTitle(t.title) || noExt(t.name) }));
    // Lo que estuviera en la cola con la ruta vieja pasa a la nueva (mismo nombre, otra extensión).
    for (const out of outs) {
      const base = noExt(out.replace(/^.*[\\/]/, ''));
      const old = pl.queue.find(t => t.dir === dir && noExt(t.path.replace(/^.*[\\/]/, '')) === base && t.path !== out);
      if (old) replacePath(old.path, out);
    }
    renderAlbumPanel();
  } catch (e) {
    $('#plMeta').textContent = String(e);
    libLogSafe('convertir: ' + e);
    renderAlbumPanel();
  }
}

// Carátulas y nombres de los discos que no estaban en caché, de uno en uno
// y actualizando la tarjeta según llegan (como en la Biblioteca).
async function loadPlayerCards() {
  if (pl.loadingCards) return;
  pl.loadingCards = true;
  try {
    for (const a of pl.albums) {
      if (pl.cards[a.dir]) continue;
      let card;
      try { card = await App().AlbumCard(a.dir); } catch (e) { card = { cover: '', artist: a.artist, album: a.album }; }
      pl.cards[a.dir] = card;
      const el = $('#plGrid').querySelector(`.pcard[data-dir="${CSS.escape(a.dir)}"]`);
      if (!el) continue;
      const art = card.cover ? Object.assign(document.createElement('img'), { className: 'art', src: card.cover, alt: '', loading: 'lazy' })
                             : Object.assign(document.createElement('div'), { className: 'art none', textContent: '♪' });
      el.querySelector('.art').replaceWith(art);
      if (card.album) el.querySelector('.t').textContent = card.album;
      if (card.artist) el.querySelector('.a').textContent = card.artist;
      if (pl.queue.some(t => t.dir === a.dir)) renderNow();
      if (pl.sel === a.dir) renderAlbumPanel();
      if (jbOn() && pl.favs.has(a.dir)) renderJukebox();
      if (dkOn() && pl.favs.has(a.dir)) renderTapes();
      if (grOn() && pl.favs.has(a.dir)) renderRecords();
      if (cvOn() && pl.favs.has(a.dir)) renderCarousel();
    }
  } finally { pl.loadingCards = false; }
}

restoreQueue();

// ---- favoritos y gramola -----------------------------------------------------
// Los discos con ♥ salen en la gramola: un carrusel de carátulas; elegir uno
// saca el vinilo de detrás de la funda, lo baja al plato, cae el brazo y
// empieza a sonar. El plato enseña siempre lo que está sonando.

// Favoritos es una vista con varias visualizaciones (pl.fav): la gramola, la
// pletina... Se recuerda la última.
const jbOn = () => pl.view === 'favs' && pl.fav === 'jukebox';
const dkOn = () => pl.view === 'favs' && pl.fav === 'deck';
const grOn = () => pl.view === 'favs' && pl.fav === 'gramo';
const cvOn = () => pl.view === 'favs' && pl.fav === 'carousel';
function render() {
  $('#plGrid').hidden = pl.view !== 'grid';
  $('#plFavsView').hidden = pl.view !== 'favs';
  $('#plJukebox').hidden = !jbOn();
  $('#plDeck').hidden = !dkOn();
  $('#plGramo').hidden = !grOn();
  $('#plCarousel').hidden = !cvOn();
  $('#plFavs').classList.toggle('active', pl.view === 'favs');
  $('#fvSwitch').querySelectorAll('button').forEach(b => b.classList.toggle('active', b.dataset.fav === pl.fav));
  if (jbOn()) renderJukebox(); else if (dkOn()) renderTapes(); else if (grOn()) renderRecords(); else if (cvOn()) renderCarousel(); else renderPlayerGrid();
}
$('#plFavs').onclick = () => { pl.view = pl.view === 'favs' ? 'grid' : 'favs'; render(); };
$('#fvSwitch').querySelectorAll('button').forEach(b => b.onclick = () => {
  if (b.disabled) return;
  pl.fav = b.dataset.fav;
  try { localStorage.setItem('plFav', pl.fav); } catch (e) {}
  render();
});
try { pl.fav = localStorage.getItem('plFav') || 'jukebox'; } catch (e) {}

async function toggleFav(dir) {
  const on = !pl.favs.has(dir);
  try { pl.favs = new Set(await App().SetFavorite(dir, on) || []); }
  catch (e) { $('#plMeta').textContent = String(e); return; }
  $('#plGrid').querySelectorAll('.pcard').forEach(c => c.classList.toggle('isfav', pl.favs.has(c.dataset.dir)));
  if (pl.sel === dir) renderAlbumPanel();
  if (jbOn()) renderJukebox();
  if (dkOn()) renderTapes();
  if (grOn()) renderRecords();
  if (cvOn()) renderCarousel();
}

// La lista de la gramola: los favoritos que están en la colección, en el
// orden de la cuadrícula (por artista).
function jbList() {
  return pl.albums.filter(a => pl.favs.has(a.dir));
}

function renderJukebox() {
  const jb = pl.jb;
  jb.list = jbList();
  $('#plMeta').textContent = `${jb.list.length} favorito(s)`;
  if (jb.i >= jb.list.length) jb.i = Math.max(0, jb.list.length - 1);
  const rack = $('#jbRack');
  if (!jb.list.length) {
    rack.innerHTML = '';
    $('#jbTitle').innerHTML = '<div class="i"><b>Sin favoritos</b><span>marca discos con ♥ en la cuadrícula</span></div>';
    $('#jbCaption').innerHTML = '<p class="empty">Marca discos con ♥ (en la carátula o en el panel del disco) y aparecerán aquí.</p>';
    renderKeys();
    renderDeck();
    return;
  }
  rack.innerHTML = jb.list.map((a, i) => {
    const cover = coverOfDir(a.dir);
    return `<div class="jb-slot" data-i="${i}" title="${esc(albumOf(a))}">
      ${cover ? `<img src="${cover}" alt="">` : '<div class="ph">♪</div>'}
      <div class="disc"></div>
    </div>`;
  }).join('');
  rack.querySelectorAll('.jb-slot').forEach(el => {
    el.onclick = () => { const i = Number(el.dataset.i); if (i === jb.i) putRecord(); else jbSelect(i); };
  });
  renderKeys();
  layoutRack();
  renderDeck();
}

// Selección por número, como los pulsadores de la máquina: 1..N.
const codeOf = i => String(i + 1);

function jbSelect(i) {
  pl.jb.i = i;
  pl.jb.key = '';
  layoutRack();
}

// Coloca cada carátula según su distancia a la elegida: la central de
// frente, las demás giradas hacia atrás y más pequeñas. Las medidas van en
// porcentaje del ancho del cristal para que escale con el mueble.
function layoutRack() {
  const jb = pl.jb;
  const w = $('#jbRack').clientWidth || 340;
  $('#jbRack').querySelectorAll('.jb-slot').forEach(el => {
    const d = Number(el.dataset.i) - jb.i;
    const ad = Math.abs(d);
    el.classList.toggle('center', d === 0);
    el.style.transform = `translate(-50%, -50%) translateX(${d * w * 0.26}px) translateZ(${-ad * w * 0.35}px) rotateY(${d === 0 ? 0 : (d < 0 ? 48 : -48)}deg) scale(${d === 0 ? 1 : 0.78})`;
    el.style.opacity = ad > 3 ? 0 : 1 - ad * 0.2;
    el.style.zIndex = 10 - ad;
    el.style.filter = d === 0 ? 'none' : `brightness(${1 - ad * 0.18})`;
  });
  const a = jb.list[jb.i];
  $('#jbDisplay').textContent = a ? codeOf(jb.i).padStart(2, '0') : '––';
  if (!a) return;
  const card = pl.cards[a.dir] || {};
  $('#jbTitle').innerHTML = `<span class="code">${codeOf(jb.i)}</span>
    <div class="i"><b>${esc(albumOf(a))}</b><span>${esc(artistOf(a))}${card.date ? ' · ' + esc(String(card.date).slice(0, 4)) : ''}</span></div>
    <span class="n">${jb.i + 1}/${jb.list.length}</span>`;
  $('#jbCaption').innerHTML = `<b>${esc(albumOf(a))}</b> <span class="sub">${esc(artistOf(a))} · ${a.files} pistas</span>
    <button class="ghost small" id="jbUnfav" title="quitar de favoritos" style="margin-left:10px">quitar ♥</button>`;
  $('#jbUnfav').onclick = () => toggleFav(a.dir);
}

// Los pulsadores: cifras; el número tecleado elige el disco y lo pone.
function renderKeys() {
  const box = $('#jbKeys');
  box.innerHTML = '1234567890'.split('').map(d => `<button class="key" data-k="${d}" title="pulsador ${d}">${d}</button>`).join('');
  box.querySelectorAll('.key').forEach(b => b.onclick = () => pressKey(b.dataset.k));
}
let jbKeyTimer = 0;
function pressKey(k) {
  const jb = pl.jb;
  jb.key = (jb.key + k).slice(-2);
  $('#jbDisplay').textContent = jb.key.padStart(2, '0');
  clearTimeout(jbKeyTimer);
  const n = Number(jb.key);
  // Un solo dígito puede ser el principio de un número de dos cifras: se
  // espera un momento; si el número ya no puede crecer, va directo.
  const go = () => {
    jb.key = '';
    if (n >= 1 && n <= jb.list.length) { jbSelect(n - 1); putRecord(); }
    else $('#jbDisplay').textContent = '--';
  };
  if (jb.key.length === 2 || n * 10 > jb.list.length) go(); else jbKeyTimer = setTimeout(go, 900);
}

$('#jbPrev').onclick = () => { if (pl.jb.list.length) jbSelect((pl.jb.i - 1 + pl.jb.list.length) % pl.jb.list.length); };
$('#jbNext').onclick = () => { if (pl.jb.list.length) jbSelect((pl.jb.i + 1) % pl.jb.list.length); };
$('#jbGo').onclick = () => putRecord();
document.addEventListener('keydown', e => {
  if (!jbOn() || !$('#tab-player').classList.contains('active') || /INPUT|TEXTAREA|SELECT/.test(e.target.tagName)) return;
  if (e.key === 'ArrowLeft') $('#jbPrev').click();
  else if (e.key === 'ArrowRight') $('#jbNext').click();
  else if (e.key === 'Enter') putRecord();
  else if (/^[0-9]$/.test(e.key)) pressKey(e.key);
});
$('#jbRack').addEventListener('wheel', e => { e.preventDefault(); (e.deltaY > 0 ? $('#jbNext') : $('#jbPrev')).click(); }, { passive: false });
window.addEventListener('resize', () => { if (jbOn()) layoutRack(); });

// putRecord: el mecanismo saca el vinilo de detrás de la funda y lo baja al
// plato, que en esta máquina queda oculto bajo el carro; luego suena
// delante de lo que hubiera en la lista.
async function putRecord() {
  const jb = pl.jb;
  const a = jb.list[jb.i];
  if (!a || jb.busy) return;
  jb.busy = true;
  const slot = $('#jbRack').querySelector(`.jb-slot[data-i="${jb.i}"]`);
  $('#jbDisplay').textContent = codeOf(jb.i).padStart(2, '0') + '●';
  if (slot) slot.classList.add('loading');
  await wait(900);
  if (slot) slot.classList.remove('loading');
  jb.busy = false;
  await addAlbum(a.dir, true);
  renderDeck();
}
const wait = ms => new Promise(r => setTimeout(r, ms));

// El plato refleja lo que suena (venga de la gramola o de la lista).
function renderDeck() {
  if (!jbOn()) return;
  const t = pl.queue[pl.idx];
  if (!t) { $('#jbNow').innerHTML = '<span class="lamp"></span>parada'; return; }
  $('#jbNow').innerHTML = `<span class="lamp ${audio.paused ? '' : 'on'}"></span>${audio.paused ? 'en pausa' : 'sonando'}: <b>${esc(t.title)}</b> · ${esc([t.artist, t.album].filter(Boolean).join(' — '))}`;
}

// ---- pletina: las cintas de la colección --------------------------------------
// Otra visualización: las cintas (los discos cuyo formato conocido es
// cassette; si no hay ninguna, todos) en una estantería, cada una con su
// caja y su cinta dibujadas con la carátula; abajo, la pletina (foto en
// deck/deck.png con el hueco semitransparente). Poner una cinta la mete en
// el hueco, se cierra la puerta, giran las bobinas, el contador cuenta y
// los vúmetros siguen el audio de verdad (Web Audio).
const dk = { list: [], i: 0, busy: false, ctx: null, an: [], raf: 0, tapeDir: '' };

// La cinta: la foto de plantilla por encima, la carátula detrás en el hueco
// de la etiqueta y los dos bujes aparte, que giran cuando suena.
const tapeHTML = (cover, title, artist, dir) => `<div class="tape">
  <div class="label">${cover ? `<img class="art" src="${cover}" alt="" data-ar="2.2" data-dir="${esc(dir || "")}">` : ""}<div class="name">${esc(title)}${artist ? " · " + esc(artist) : ""}</div></div>
  <img class="tpl" src="deck/tape.png" alt="" draggable="false">
  <img class="hub l" src="deck/hub.png" alt="" draggable="false"><img class="hub r" src="deck/hub.png" alt="" draggable="false">
</div>`;

// ---- carátulas de cassette con lomo ---------------------------------------
// Muchas carátulas de cinta son la cartulina escaneada: la portada con el
// lomo pegado (y a veces la contraportada), y da igual la proporción de la
// imagen, que muchas veces viene cuadrada. En la caja solo tiene que verse
// la portada, así que se mira el contenido: un doblez es una línea vertical
// (una raya, o un salto del mismo signo, en casi todas las filas de una
// columna). Un lomo es una tira de 5-42 % del alto pegada a un borde, de
// fondo liso, que acaba en un doblez; la portada es lo que sigue hasta el
// doblez siguiente que deje un panel de anchura normal (o hasta el otro
// borde). Si hay tiras en los dos bordes de la misma anchura es un marco,
// no un lomo. En las apaisadas, un lomo entre dos paneles anchos
// (contraportada · lomo · portada): la portada es el panel más movido (la
// contraportada suele ser texto sobre fondo liso). Sin lomo no se toca nada,
// salvo las bandas negras o blancas del escaneo. Se cachea por URL.
const frontCrops = new Map(); // src -> {x, y, w, h} en píxeles de la imagen, o null

function frontCrop(img) {
  const W = img.naturalWidth, H = img.naturalHeight;
  if (!W || !H) return null;
  const cw = 256, ch = Math.max(16, Math.min(512, Math.round(cw * H / W)));
  let d;
  try {
    const cv = document.createElement('canvas');
    cv.width = cw; cv.height = ch;
    const ctx = cv.getContext('2d', { willReadFrequently: true });
    ctx.drawImage(img, 0, 0, cw, ch);
    d = ctx.getImageData(0, 0, cw, ch).data;
  } catch (e) { return null; }
  const L = new Float32Array(cw * ch);
  for (let i = 0, j = 0; i < L.length; i++, j += 4) L[i] = d[j] * 0.3 + d[j + 1] * 0.59 + d[j + 2] * 0.11;
  const lum = (x, y) => L[y * cw + x];
  // 1) Bandas lisas blancas o negras pegadas a los bordes: el fondo del escaneo.
  const flat = (x0, y0, dx, dy, n) => {
    let s = 0, dev = 0;
    const l0 = lum(x0, y0);
    for (let k = 0; k < n; k++) { const l = lum(x0 + dx * k, y0 + dy * k); s += l; if (Math.abs(l - l0) > 24) dev++; }
    const m = s / n;
    return dev < n * 0.03 && (m < 28 || m > 227);
  };
  let top = 0, bottom = ch, left = 0, right = cw;
  while (top < ch * 0.3 && flat(0, top, 1, 0, cw)) top++;
  while (bottom > ch * 0.7 && flat(0, bottom - 1, 1, 0, cw)) bottom--;
  while (left < cw * 0.3 && flat(left, 0, 0, 1, ch)) left++;
  while (right > cw * 0.7 && flat(right - 1, 0, 0, 1, ch)) right--;
  // Solo cuentan las bandas de un escaneo metido en un cuadrado: a los dos
  // lados de un mismo eje, parecidas, de al menos un 10 %, y nada en el otro
  // eje. Un marco negro por los cuatro lados, o un margen estrecho, es
  // diseño y se deja.
  const band = (p, q, n) => p >= n * 0.1 && q >= n * 0.1 && Math.abs(p - q) <= 0.4 * Math.max(p, q);
  const tb = band(top, ch - bottom, ch), lr = band(left, cw - right, cw);
  if (!tb || lr) { top = 0; bottom = ch; }
  if (!lr || tb) { left = 0; right = cw; }
  const w = right - left, h = bottom - top;
  // 2) Líneas verticales: en cada columna, la fracción de filas en que es una
  // raya (más oscura o más clara que sus dos vecinas) o un borde (salto del
  // mismo signo con la siguiente). Una textura da saltos, pero no seguidos.
  const line = new Float32Array(cw);
  for (let x = left + 1; x < right - 1; x++) {
    let ray = 0, pos = 0, neg = 0;
    for (let y = top; y < bottom; y++) {
      const a = lum(x - 1, y), b = lum(x, y), c = lum(x + 1, y);
      if ((b < a - 12 && b < c - 12) || (b > a + 12 && b > c + 12)) ray++;
      const dd = c - b;
      if (dd > 12) pos++; else if (dd < -12) neg++;
    }
    line[x] = Math.max(ray, pos, neg) / h;
  }
  const lines = []; // [columna, fuerza]: máximos locales claros
  for (let x = left + 1; x < right - 1; x++) {
    if (line[x] < 0.6 || line[x - 1] > line[x] || line[x + 1] > line[x]) continue;
    const last = lines[lines.length - 1];
    if (last && x - last[0] < 4) { if (line[x] > last[1]) lines[lines.length - 1] = [x, line[x]]; continue; }
    lines.push([x, line[x]]);
  }
  const inRange = (v, a, b) => v >= a && v <= b;
  const sMin = 0.05 * h, sMax = 0.42 * h;
  // bgFrac: fracción del panel cercana a su luminancia mediana (un lomo es
  // fondo liso con texto). busy: lo movido que está (desviación media).
  const sample = (a, b) => { const v = []; for (let y = top; y < bottom; y += 2) for (let x = a; x < b; x += 2) v.push(lum(x, y)); return v; };
  const bgFrac = (a, b) => { const v = sample(a, b).sort((p, q) => p - q); const med = v[v.length >> 1]; let n = 0; for (const l of v) if (Math.abs(l - med) < 28) n++; return n / Math.max(1, v.length); };
  const busy = ([a, b]) => { const v = sample(a, b); const m = v.reduce((s, l) => s + l, 0) / Math.max(1, v.length); return v.reduce((s, l) => s + Math.abs(l - m), 0) / Math.max(1, v.length); };
  // textish: la tira (sin el doblez) lleva texto en vertical (o una orla):
  // en alguna columna del interior, la tinta (lejos de la luminancia del
  // fondo) se alterna muchas veces a lo largo de la tira; poca tinta en
  // total, y los bordes de la tira casi limpios (la tinta va por dentro; se
  // perdonan 2 px del borde de la imagen, que suele traer una raya; por el
  // lado del doblez se tolera más, que el texto del lomo suele arrimarse, pero
  // no un texto horizontal que cruza el doblez). Un fondo liso junto a una
  // figura tampoco lo pasa; un lomo, sí. `fold`: 'r' si el doblez está a la derecha.
  const textish = (a, b, fold) => {
    const wdt = b - a;
    if (wdt < 8) return false;
    const v = sample(a, b).sort((p, q) => p - q), med = v[v.length >> 1];
    const m = Math.max(2, Math.round(wdt * 0.1));
    let ink = 0, best = 0, edgeL = 0, edgeR = 0;
    for (let x = a + 2; x < b; x++) {
      let runs = 0, prev = false, k = 0;
      for (let y = top; y < bottom; y++) {
        const on = Math.abs(lum(x, y) - med) > 40;
        if (on) k++;
        if (on !== prev) runs++;
        prev = on;
      }
      if (x < a + m) { edgeL += k; continue; }
      if (x >= b - m) { edgeR += k; continue; }
      ink += k;
      best = Math.max(best, runs);
    }
    const f = ink / Math.max(1, (wdt - 2 * m) * h);
    edgeL /= Math.max(1, (m - 2) * h);
    edgeR /= m * h;
    const foldInk = fold === 'r' ? edgeR : edgeL, outerInk = fold === 'r' ? edgeL : edgeR;
    return best >= 10 && f >= 0.02 && f <= 0.6 && foldInk <= 0.7 && outerInk <= 0.1;
  };
  // 3) Lomo pegado a un borde: la línea más fuerte a 5-42 % del alto del
  // borde con fondo liso y texto en vertical en la tira (si la más fuerte
  // no lo tiene, la siguiente: un lomo con una orla lleva su propia raya).
  // Una línea parecida a la misma distancia del otro borde es un marco, no
  // un lomo, salvo que aquella tira también sea un lomo (portada entre dos).
  const edgeSpine = fromLeft => {
    const cands = lines.filter(([x]) => inRange(fromLeft ? x - left : right - x, sMin, sMax)).sort((p, q) => q[1] - p[1]);
    return cands.find(([x]) => { const a = fromLeft ? left : x, b = fromLeft ? x : right; return bgFrac(a, b) >= 0.45 && textish(fromLeft ? a : a + 3, fromLeft ? b - 2 : b, fromLeft ? 'r' : 'l'); }) || null;
  };
  const mirrored = (x, fromLeft) => { const wdt = fromLeft ? x - left : right - x; return lines.some(([y, s]) => s >= 0.6 && Math.abs((fromLeft ? right - y : y - left) - wdt) <= 0.2 * wdt); };
  let ls = edgeSpine(true), rs = edgeSpine(false);
  if (ls && rs) { if (ls[1] >= rs[1]) rs = null; else ls = null; } // portada entre dos lomos: el doblez más claro
  else if (ls && mirrored(ls[0], true)) ls = null; // un marco
  else if (rs && mirrored(rs[0], false)) rs = null;
  let sel = null;
  if (ls || rs) {
    // La portada: desde el doblez hasta el siguiente que deje un panel de
    // 0,45-1,05 del alto (el más fuerte), o hasta el otro borde (máx. 1,25).
    const a = (ls || rs)[0];
    let e = null;
    for (const [x, s] of lines) {
      const wdt = ls ? x - a : a - x;
      if (inRange(wdt, 0.45 * h, 1.05 * h) && (!e || s > e[1])) e = [x, s];
    }
    const span = Math.round(1.25 * h);
    sel = ls ? [a, e ? e[0] : Math.min(right, a + span)] : [e ? e[0] : Math.max(left, a - span), a];
  } else if (w > 1.25 * h) {
    // 4) Apaisada: un lomo entre dos paneles anchos; la portada, el más movido.
    let best = null;
    for (let i = 0; i < lines.length - 1; i++) {
      const [a, sa] = lines[i], [b, sb] = lines[i + 1];
      if (!inRange(b - a, sMin, sMax) || a - left < 0.45 * h || right - b < 0.45 * h || !textish(a + 3, b - 2)) continue;
      const s = Math.min(sa, sb);
      if (!best || s > best[2]) best = [a, b, s];
    }
    if (best) {
      const [a, b] = best, span = Math.round(1.05 * h);
      const p1 = [Math.max(left, a - span), a], p2 = [b, Math.min(right, b + span)];
      sel = busy(p1) >= busy(p2) ? p1 : p2;
    }
  }
  if (!sel) {
    if (top === 0 && bottom === ch && left === 0 && right === cw) return null;
    sel = [left, right]; // solo las bandas del escaneo
  }
  const sx = W / cw, sy = H / ch;
  return { x: Math.round(sel[0] * sx), y: Math.round(top * sy), w: Math.round((sel[1] - sel[0]) * sx), h: Math.round(h * sy) };
}

// fitFront coloca el recorte dentro de su hueco (data-ar: ancho/alto del
// hueco): centra el recorte y lo escala para que lo cubra. Manda el encuadre
// guardado a mano (data-dir), y si no lo hay, el automático.
function fitFront(im) {
  if (!im.naturalWidth) return;
  const W = im.naturalWidth, H = im.naturalHeight, A = Number(im.dataset.ar) || 1;
  let c;
  const man = im.dataset.dir && pl.crops[im.dataset.dir];
  if (man) c = { x: man.x * W, y: man.y * H, w: man.w * W, h: man.h * H };
  else {
    c = frontCrops.get(im.src);
    if (c === undefined) { c = frontCrop(im); frontCrops.set(im.src, c); }
  }
  if (!c) { im.style.cssText = ''; return; }
  const size = c.w / c.h > A ? `height:${H / c.h * 100}%;width:auto` : `width:${W / c.w * 100}%;height:auto`;
  im.style.cssText = `position:absolute;left:50%;top:50%;max-width:none;${size};transform:translate(-${(c.x + c.w / 2) / W * 100}%,-${(c.y + c.h / 2) / H * 100}%)`;
}

function fitFronts(root) {
  root.querySelectorAll('img.art').forEach(im => {
    if (im.complete && im.naturalWidth) fitFront(im); else im.onload = () => fitFront(im);
  });
}

// ---- encuadre a mano ------------------------------------------------------
// La detección del lomo no acierta siempre: con "Ajustar portada" se
// arrastra y se acerca la carátula dentro de la cartulina, y el encuadre
// (fracciones de la imagen) se guarda por disco (crops.json) y manda sobre
// el automático, en la caja y en la etiqueta de la cinta.
const ed = { dir: '', W: 0, H: 0, s: 0, smin: 0, ox: 0, oy: 0, cw: 0, ch: 0, drag: null };

function openCropEditor(a) {
  const cover = coverOfDir(a.dir);
  if (!cover) { $('#plMeta').textContent = 'Este disco no tiene carátula.'; return; }
  ed.dir = a.dir;
  $('#dkCropTitle').textContent = `${albumOf(a)} · ${artistOf(a)}`;
  const im = $('#dkCropImg');
  im.style.cssText = '';
  im.onload = () => {
    ed.W = im.naturalWidth; ed.H = im.naturalHeight;
    const card = $('#dkCropCard');
    ed.cw = card.clientWidth; ed.ch = card.clientHeight;
    ed.smin = Math.max(ed.cw / ed.W, ed.ch / ed.H);
    // De partida, el encuadre que se ve ahora (el guardado, o el automático).
    const man = pl.crops[a.dir];
    let c = man ? { x: man.x * ed.W, y: man.y * ed.H, w: man.w * ed.W, h: man.h * ed.H } : (frontCrops.get(cover) ?? frontCrop(im));
    if (!c) c = { x: 0, y: 0, w: ed.W, h: ed.H };
    ed.s = Math.max(ed.smin, ed.cw / c.w, ed.ch / c.h);
    ed.ox = ed.cw / 2 - (c.x + c.w / 2) * ed.s;
    ed.oy = ed.ch / 2 - (c.y + c.h / 2) * ed.s;
    edClamp(); edRender();
  };
  im.src = cover;
  if (im.complete && im.naturalWidth) im.onload();
  $('#dkCrop').hidden = false;
}

function edClamp() {
  ed.s = Math.max(ed.smin, Math.min(ed.smin * 6, ed.s));
  ed.ox = Math.min(0, Math.max(ed.cw - ed.W * ed.s, ed.ox));
  ed.oy = Math.min(0, Math.max(ed.ch - ed.H * ed.s, ed.oy));
}
function edRender() {
  $('#dkCropImg').style.cssText = `position:absolute;max-width:none;left:${ed.ox}px;top:${ed.oy}px;width:${ed.W * ed.s}px;height:${ed.H * ed.s}px`;
  $('#dkCropZoom').value = String(Math.round(Math.log(ed.s / ed.smin) / Math.log(6) * 100));
}
function edZoomAt(factor, px, py) {
  const s2 = Math.max(ed.smin, Math.min(ed.smin * 6, ed.s * factor));
  ed.ox = px - (px - ed.ox) * s2 / ed.s;
  ed.oy = py - (py - ed.oy) * s2 / ed.s;
  ed.s = s2;
  edClamp(); edRender();
}
$('#dkCropCard').addEventListener('pointerdown', e => { ed.drag = { x: e.clientX, y: e.clientY, ox: ed.ox, oy: ed.oy }; $('#dkCropCard').setPointerCapture(e.pointerId); e.preventDefault(); });
$('#dkCropCard').addEventListener('pointermove', e => { if (!ed.drag) return; ed.ox = ed.drag.ox + e.clientX - ed.drag.x; ed.oy = ed.drag.oy + e.clientY - ed.drag.y; edClamp(); edRender(); });
$('#dkCropCard').addEventListener('pointerup', () => { ed.drag = null; });
$('#dkCropCard').addEventListener('pointercancel', () => { ed.drag = null; });
$('#dkCropCard').addEventListener('wheel', e => { e.preventDefault(); const r = $('#dkCropCard').getBoundingClientRect(); edZoomAt(e.deltaY < 0 ? 1.1 : 1 / 1.1, e.clientX - r.left, e.clientY - r.top); }, { passive: false });
$('#dkCropZoom').oninput = e => { const s2 = ed.smin * Math.pow(6, Number(e.target.value) / 100); edZoomAt(s2 / ed.s, ed.cw / 2, ed.ch / 2); };
$('#dkCropCancel').onclick = () => { $('#dkCrop').hidden = true; };
$('#dkCropSave').onclick = async () => {
  const c = { x: -ed.ox / (ed.W * ed.s), y: -ed.oy / (ed.H * ed.s), w: ed.cw / (ed.W * ed.s), h: ed.ch / (ed.H * ed.s) };
  try { await App().SetCoverCrop(ed.dir, true, c); } catch (e) { $('#plMeta').textContent = 'No se pudo guardar: ' + e; return; }
  pl.crops[ed.dir] = c;
  $('#dkCrop').hidden = true;
  renderTapes(); renderDeckState();
};
$('#dkCropAuto').onclick = async () => {
  try { await App().SetCoverCrop(ed.dir, false, { x: 0, y: 0, w: 1, h: 1 }); } catch (e) { $('#plMeta').textContent = 'No se pudo guardar: ' + e; return; }
  delete pl.crops[ed.dir];
  $('#dkCrop').hidden = true;
  renderTapes(); renderDeckState();
};

function dkList() { return jbList(); } // los favoritos, como la gramola

function renderTapes() {
  dk.list = dkList();
  $('#plMeta').textContent = `${dk.list.length} favorito(s)`;
  if (dk.i >= dk.list.length) dk.i = Math.max(0, dk.list.length - 1);
  const rack = $('#dkRack');
  if (!dk.list.length) { rack.innerHTML = ''; $('#dkCaption').innerHTML = '<p class="empty">Marca discos con ♥ y aparecerán aquí como cintas.</p>'; return; }
  rack.innerHTML = dk.list.map((a, i) => {
    const cover = coverOfDir(a.dir);
    return `<div class="dk-item" data-i="${i}" title="${esc(albumOf(a))}">
      <div class="case"><div class="card">${cover ? `<img class="art" src="${cover}" alt="" data-ar="0.586" data-dir="${esc(a.dir)}">` : `<div class="art none">♪</div>`}</div><img class="tpl" src="deck/case.png" alt="" draggable="false"></div>
      ${tapeHTML(cover, albumOf(a), "", a.dir)}
    </div>`;
  }).join('');
  rack.querySelectorAll('.dk-item').forEach(el => {
    el.onclick = () => { const i = Number(el.dataset.i); if (i === dk.i) loadTape(); else dkSelect(i); };
  });
  fitFronts(rack);
  layoutShelf();
  renderDeckState();
}

function dkSelect(i) { dk.i = i; layoutShelf(); }

function layoutShelf() {
  const w = $('#dkRack').clientWidth || 800;
  $('#dkRack').querySelectorAll('.dk-item').forEach(el => {
    const d = Number(el.dataset.i) - dk.i;
    const ad = Math.abs(d);
    el.classList.toggle('sel', d === 0);
    el.style.transform = `translateX(-50%) translateX(${d * Math.min(200, w * 0.22)}px) translateZ(${-ad * 90}px) rotateY(${d === 0 ? 0 : (d < 0 ? 28 : -28)}deg) scale(${d === 0 ? 1.06 : 0.9})`;
    el.style.opacity = ad > 3 ? 0 : 1 - ad * 0.22;
    el.style.zIndex = 10 - ad;
    el.style.filter = d === 0 ? 'none' : `brightness(${1 - ad * 0.16})`;
  });
  const a = dk.list[dk.i];
  if (!a) return;
  const card = pl.cards[a.dir] || {};
  $('#dkCaption').innerHTML = `<b>${esc(albumOf(a))}</b> <span class="sub">${esc(artistOf(a))}${card.date ? ' · ' + esc(String(card.date).slice(0, 4)) : ''} · ${a.files} pistas</span>
    <button class="ghost small" id="dkGo" style="margin-left:10px">▶ Poner la cinta</button> <button class="ghost small" id="dkCropBtn" title="encuadrar la carátula en la cartulina">✂ Ajustar portada</button>`;
  $("#dkGo").onclick = loadTape;
  $("#dkCropBtn").onclick = () => openCropEditor(a);
}
$('#dkPrev').onclick = () => { if (dk.list.length) dkSelect((dk.i - 1 + dk.list.length) % dk.list.length); };
$('#dkNext').onclick = () => { if (dk.list.length) dkSelect((dk.i + 1) % dk.list.length); };
$('#dkRack').addEventListener('wheel', e => { e.preventDefault(); (e.deltaY > 0 ? $('#dkNext') : $('#dkPrev')).click(); }, { passive: false });
document.addEventListener('keydown', e => {
  if (!dkOn() || !$('#tab-player').classList.contains('active') || /INPUT|TEXTAREA|SELECT/.test(e.target.tagName)) return;
  if (e.key === 'ArrowLeft') $('#dkPrev').click();
  else if (e.key === 'ArrowRight') $('#dkNext').click();
  else if (e.key === 'Enter') loadTape();
});
window.addEventListener('resize', () => { if (dkOn()) layoutShelf(); });

// loadTape: la cinta sale de la estantería, entra en el hueco, se cierra
// la puerta y suena el disco delante de lo que hubiera en la lista.
async function loadTape() {
  const a = dk.list[dk.i];
  if (!a || dk.busy) return;
  dk.busy = true;
  const item = $('#dkRack').querySelector(`.dk-item[data-i="${dk.i}"]`);
  if (item) item.classList.add('loading');
  await ejectTape();
  const well = $('#dkWell');
  well.innerHTML = tapeHTML(coverOfDir(a.dir), albumOf(a), artistOf(a), a.dir);
  fitFronts(well);
  dk.tapeDir = a.dir;
  await wait(50);
  well.querySelector('.tape').classList.add('in');
  await wait(800);
  $('#dkDoor').classList.add('closed');
  await wait(400);
  if (item) item.classList.remove('loading');
  dk.busy = false;
  await addAlbum(a.dir, true);
  renderDeckState();
}

async function ejectTape() {
  const tape = $('#dkWell').querySelector('.tape');
  if (!tape) return;
  $('#dkDoor').classList.remove('closed');
  await wait(300);
  tape.classList.remove('in');
  await wait(700);
  $('#dkWell').innerHTML = '';
  dk.tapeDir = '';
}

// Botones de la pletina (zonas sobre la foto).
$('#dkRew').onclick = prev;
$('#dkFwd').onclick = next;
$('#dkPlay').onclick = () => { if (pl.idx < 0 && !pl.queue.length && dk.list.length) loadTape(); else togglePlay(); };
$('#dkStop').onclick = () => { audio.pause(); audio.currentTime = 0; renderDeckState(); };
$('#dkEject').onclick = async () => { audio.pause(); await ejectTape(); renderDeckState(); };

// renderDeckState: la cinta del hueco es la del disco que suena (si suena
// otro disco, la cambia), las bobinas giran o se paran, y los pilotos.
function renderDeckState() {
  if (!dkOn()) return;
  const t = pl.queue[pl.idx];
  const well = $('#dkWell');
  if (t && t.dir !== dk.tapeDir && !dk.busy) {
    well.innerHTML = tapeHTML(coverOfDir(t.dir), t.album, t.artist, t.dir);
    fitFronts(well);
    dk.tapeDir = t.dir;
    requestAnimationFrame(() => { const tp = well.querySelector('.tape'); if (tp) tp.classList.add('in'); });
    $('#dkDoor').classList.add('closed');
  }
  const tape = well.querySelector('.tape');
  if (tape) { tape.classList.toggle('playing', !!t); tape.classList.toggle('paused', !t || audio.paused); }
  $('#dkPlay').classList.toggle('lit', !!t && !audio.paused);
  $('#dkStop').classList.toggle('lit', !t || audio.paused);
  $('#dkNow').innerHTML = t ? `<span class="lamp ${audio.paused ? '' : 'on'}"></span>${audio.paused ? 'en pausa' : 'sonando'}: <b>${esc(t.title)}</b> · ${esc([t.artist, t.album].filter(Boolean).join(' — '))}` : '<span class="lamp"></span>parada';
  if (t && !audio.paused) startMeters(); else stopMeters();
}

// audioGraph: el analizador del audio (un canal por analizador), compartido
// por los vúmetros de la pletina y el latido del carrusel. Solo se puede
// enganchar una vez al <audio>, por eso se guarda.
function audioGraph() {
  if (!dk.ctx) {
    try {
      dk.ctx = new AudioContext();
      const src = dk.ctx.createMediaElementSource(audio);
      const split = dk.ctx.createChannelSplitter(2);
      src.connect(split);
      src.connect(dk.ctx.destination); // el audio sigue saliendo por los altavoces
      dk.an = [0, 1].map(ch => { const an = dk.ctx.createAnalyser(); an.fftSize = 512; an.smoothingTimeConstant = .6; split.connect(an, ch); return an; });
    } catch (e) { dk.ctx = null; return null; }
  }
  if (dk.ctx.state === 'suspended') dk.ctx.resume();
  return dk.an;
}

// level: el nivel de un analizador, de 0 (-40 dB) a 1 (+3 dB).
function level(an, buf) {
  an.getByteTimeDomainData(buf);
  let sum = 0;
  for (let k = 0; k < buf.length; k++) { const v = (buf[k] - 128) / 128; sum += v * v; }
  const rms = Math.sqrt(sum / buf.length);
  const db = 20 * Math.log10(rms || 1e-4); // -80..0
  return Math.max(0, Math.min(1, (db + 40) / 43));
}

// Vúmetros y contador con el audio de verdad: un analizador por canal.
function startMeters() {
  if (!audioGraph()) return;
  if (dk.raf) return;
  const buf = new Uint8Array(512);
  const needles = [$('#dkVu1 i'), $('#dkVu2 i')];
  const tick = () => {
    dk.raf = 0;
    if (!dkOn() || audio.paused) { needles.forEach(n => n.style.transform = 'rotate(-42deg)'); return; }
    dk.an.forEach((an, i) => { needles[i].style.transform = `rotate(${-42 + level(an, buf) * 84}deg)`; });
    $('#dkCounter').textContent = String(Math.floor(audio.currentTime * 1.4) % 1000).padStart(3, '0');
    dk.raf = requestAnimationFrame(tick);
  };
  dk.raf = requestAnimationFrame(tick);
}
function stopMeters() {
  if (dk.raf) { cancelAnimationFrame(dk.raf); dk.raf = 0; }
  $('#dkVu1 i').style.transform = $('#dkVu2 i').style.transform = 'rotate(-42deg)';
}

// ---- gramófono: los vinilos de favoritos ---------------------------------
// Otra visualización de favoritos: cada disco es una funda (foto
// gramo/sleeve.png con el cartón abierto: la carátula va detrás) en una
// estantería y, debajo, un gramófono de bocina visto en picado (foto
// gramo/gramophone.png). El plato es una elipse medida por tools/cabinet:
// el disco (foto gramo/record.png con la etiqueta abierta para la carátula)
// es un cuadrado abatido con rotateX para que encaje, y dentro gira. Poner
// un disco lo saca de la funda, lo deja caer en el plato y suena delante
// de lo que hubiera en la lista. El brazo de la foto descansa fuera del
// plato, así que el disco no lo tapa.
const gr = { list: [], i: 0, busy: false, dir: '' };

const recordHTML = (cover, dir) => `<div class="record">
  <div class="label">${cover ? `<img class="art" src="${cover}" alt="" data-dir="${esc(dir || '')}">` : ''}</div>
  <img class="tpl" src="gramo/record.png" alt="" draggable="false">
</div>`;

function renderRecords() {
  gr.list = jbList();
  $('#plMeta').textContent = `${gr.list.length} favorito(s)`;
  if (gr.i >= gr.list.length) gr.i = Math.max(0, gr.list.length - 1);
  const rack = $('#grRack');
  if (!gr.list.length) { rack.innerHTML = ''; $('#grCaption').innerHTML = '<p class="empty">Marca discos con ♥ y aparecerán aquí como vinilos.</p>'; return; }
  rack.innerHTML = gr.list.map((a, i) => {
    const cover = coverOfDir(a.dir);
    return `<div class="gr-item" data-i="${i}" title="${esc(albumOf(a))}">
      <div class="out">${recordHTML(cover, a.dir)}</div>
      <div class="card">${cover ? `<img class="art" src="${cover}" alt="">` : `<div class="art none">♪</div>`}</div>
      <img class="tpl" src="gramo/sleeve.png" alt="" draggable="false">
    </div>`;
  }).join('');
  rack.querySelectorAll('.gr-item').forEach(el => {
    el.onclick = () => { const i = Number(el.dataset.i); if (i === gr.i) putRecordOn(); else grSelect(i); };
  });
  layoutRecords();
  renderGramoState();
}

function grSelect(i) { gr.i = i; layoutRecords(); }

function layoutRecords() {
  const w = $('#grRack').clientWidth || 800;
  $('#grRack').querySelectorAll('.gr-item').forEach(el => {
    const d = Number(el.dataset.i) - gr.i;
    const ad = Math.abs(d);
    el.classList.toggle('sel', d === 0);
    el.style.transform = `translateX(-50%) translateX(${d * Math.min(230, w * 0.25)}px) translateZ(${-ad * 90}px) rotateY(${d === 0 ? 0 : (d < 0 ? 28 : -28)}deg) scale(${d === 0 ? 1.04 : 0.88})`;
    el.style.opacity = ad > 3 ? 0 : 1 - ad * 0.22;
    el.style.zIndex = 10 - ad;
    el.style.filter = d === 0 ? '' : `brightness(${1 - ad * 0.16})`;
  });
  const a = gr.list[gr.i];
  if (!a) return;
  const card = pl.cards[a.dir] || {};
  $('#grCaption').innerHTML = `<b>${esc(albumOf(a))}</b> <span class="sub">${esc(artistOf(a))}${card.date ? ' · ' + esc(String(card.date).slice(0, 4)) : ''} · ${a.files} pistas</span>
    <button class="ghost small" id="grGo" style="margin-left:10px">▶ Poner el disco</button>`;
  $('#grGo').onclick = putRecordOn;
}
$('#grPrev').onclick = () => { if (gr.list.length) grSelect((gr.i - 1 + gr.list.length) % gr.list.length); };
$('#grNext').onclick = () => { if (gr.list.length) grSelect((gr.i + 1) % gr.list.length); };
$('#grRack').addEventListener('wheel', e => { e.preventDefault(); (e.deltaY > 0 ? $('#grNext') : $('#grPrev')).click(); }, { passive: false });
document.addEventListener('keydown', e => {
  if (!grOn() || !$('#tab-player').classList.contains('active') || /INPUT|TEXTAREA|SELECT/.test(e.target.tagName)) return;
  if (e.key === 'ArrowLeft') $('#grPrev').click();
  else if (e.key === 'ArrowRight') $('#grNext').click();
  else if (e.key === 'Enter') putRecordOn();
});
window.addEventListener('resize', () => { if (grOn()) layoutRecords(); });

// putRecordOn: el disco sale de la funda (se desliza y cae), aparece en el
// plato y suena el disco delante de lo que hubiera en la lista.
async function putRecordOn() {
  const a = gr.list[gr.i];
  if (!a || gr.busy) return;
  gr.busy = true;
  const item = $('#grRack').querySelector(`.gr-item[data-i="${gr.i}"]`);
  if (item) item.classList.add('loading');
  await wait(600);
  setRecord(a.dir);
  await wait(650);
  if (item) item.classList.remove('loading');
  gr.busy = false;
  await addAlbum(a.dir, true);
  renderGramoState();
}

// setRecord pone el disco de una carpeta en el plato, cayendo desde arriba.
function setRecord(dir) {
  const spin = $('#grSpin');
  spin.innerHTML = recordHTML(coverOfDir(dir), dir);
  const rec = spin.querySelector('.record');
  rec.classList.add('drop');
  requestAnimationFrame(() => requestAnimationFrame(() => rec.classList.remove('drop')));
  gr.dir = dir;
}

// renderGramoState: el disco del plato es el que suena (si suena otro, lo
// cambia), gira o se para, y la línea de abajo.
function renderGramoState() {
  if (!grOn()) return;
  const t = pl.queue[pl.idx];
  if (t && t.dir !== gr.dir && !gr.busy) setRecord(t.dir);
  const spin = $('#grSpin');
  spin.classList.toggle('playing', !!t);
  spin.classList.toggle('paused', !t || audio.paused);
  $('#grNow').innerHTML = t ? `<span class="lamp ${audio.paused ? '' : 'on'}"></span>${audio.paused ? 'en pausa' : 'sonando'}: <b>${esc(t.title)}</b> · ${esc([t.artist, t.album].filter(Boolean).join(' — '))}` : '<span class="lamp"></span>parado';
}

// ---- carrusel: las carátulas, y en grande la que suena ------------------
// La visualización sencilla: los favoritos como carátulas en un carrusel y,
// debajo, en grande, la carátula del disco que está sonando (o la elegida,
// apagada, si no suena nada), con un latido suave que sigue el audio.
const cv = { list: [], i: 0, raf: 0 };

function renderCarousel() {
  cv.list = jbList();
  $('#plMeta').textContent = `${cv.list.length} favorito(s)`;
  if (cv.i >= cv.list.length) cv.i = Math.max(0, cv.list.length - 1);
  const rack = $('#cvRack');
  if (!cv.list.length) { rack.innerHTML = ''; $('#cvCaption').innerHTML = '<p class="empty">Marca discos con ♥ y aparecerán aquí.</p>'; renderCarouselState(); return; }
  rack.innerHTML = cv.list.map((a, i) => {
    const cover = coverOfDir(a.dir);
    return `<div class="cv-item" data-i="${i}" data-dir="${esc(a.dir)}" title="${esc(albumOf(a))} · ${esc(artistOf(a))}">${cover ? `<img class="art" src="${cover}" alt="">` : `<div class="art none">♪</div>`}</div>`;
  }).join('');
  rack.querySelectorAll('.cv-item').forEach(el => {
    el.onclick = () => { const i = Number(el.dataset.i); if (i === cv.i) playSelected(); else cvSelect(i); };
  });
  layoutCarousel();
  renderCarouselState();
}

function cvSelect(i) { cv.i = i; layoutCarousel(); renderCarouselState(); }

function layoutCarousel() {
  const w = $('#cvRack').clientWidth || 800;
  $('#cvRack').querySelectorAll('.cv-item').forEach(el => {
    const d = Number(el.dataset.i) - cv.i;
    const ad = Math.abs(d);
    el.classList.toggle('sel', d === 0);
    el.style.transform = `translateX(-50%) translateX(${d * Math.min(210, w * 0.23)}px) translateZ(${-ad * 90}px) rotateY(${d === 0 ? 0 : (d < 0 ? 32 : -32)}deg) scale(${d === 0 ? 1.04 : 0.86})`;
    el.style.opacity = ad > 3 ? 0 : 1 - ad * 0.2;
    el.style.zIndex = 10 - ad;
    el.style.filter = d === 0 ? '' : `brightness(${1 - ad * 0.16})`;
  });
  const a = cv.list[cv.i];
  if (!a) return;
  const card = pl.cards[a.dir] || {};
  $('#cvCaption').innerHTML = `<b>${esc(albumOf(a))}</b> <span class="sub">${esc(artistOf(a))}${card.date ? ' · ' + esc(String(card.date).slice(0, 4)) : ''} · ${a.files} pistas</span>
    <button class="ghost small" id="cvGo" style="margin-left:10px">▶ Reproducir</button>`;
  $('#cvGo').onclick = playSelected;
}
$('#cvPrev').onclick = () => { if (cv.list.length) cvSelect((cv.i - 1 + cv.list.length) % cv.list.length); };
$('#cvNext').onclick = () => { if (cv.list.length) cvSelect((cv.i + 1) % cv.list.length); };
$('#cvRack').addEventListener('wheel', e => { e.preventDefault(); (e.deltaY > 0 ? $('#cvNext') : $('#cvPrev')).click(); }, { passive: false });
document.addEventListener('keydown', e => {
  if (!cvOn() || !$('#tab-player').classList.contains('active') || /INPUT|TEXTAREA|SELECT/.test(e.target.tagName)) return;
  if (e.key === 'ArrowLeft') $('#cvPrev').click();
  else if (e.key === 'ArrowRight') $('#cvNext').click();
  else if (e.key === 'Enter') playSelected();
});
window.addEventListener('resize', () => { if (cvOn()) layoutCarousel(); });

async function playSelected() {
  const a = cv.list[cv.i];
  if (!a) return;
  await addAlbum(a.dir, true);
  renderCarouselState();
}

// renderCarouselState: la grande es la carátula del disco que suena; si no
// suena nada, la del elegido, apagada. Marca en el carrusel el que suena.
function renderCarouselState() {
  if (!cvOn()) return;
  const t = pl.queue[pl.idx];
  const a = t ? (albumByDir(t.dir) || { dir: t.dir, album: t.album, artist: t.artist }) : cv.list[cv.i];
  const big = $('#cvBig');
  const cover = a ? coverOfDir(a.dir) : '';
  const key = (a ? a.dir : '') + '|' + cover;
  if (big.dataset.key !== key) {
    big.innerHTML = cover ? `<img class="art" src="${cover}" alt="">` : '<div class="art none">♪</div>';
    big.dataset.key = key;
  }
  big.classList.toggle('idle', !t);
  $('#cvRack').querySelectorAll('.cv-item').forEach(el => el.classList.toggle('playing', !!t && el.dataset.dir === t.dir));
  $('#cvText').innerHTML = t
    ? `<b>${esc(t.title)}</b><span class="sub"><span class="lamp ${audio.paused ? '' : 'on'}"></span>${audio.paused ? 'en pausa' : 'sonando'} · ${esc([t.artist, t.album].filter(Boolean).join(' — '))}</span>`
    : (a ? `<b>${esc(albumOf(a))}</b><span class="sub">${esc(artistOf(a))} · Enter o ▶ para reproducir</span>` : '');
  if (t && !audio.paused) startPulse(); else stopPulse();
}

// El latido: la carátula grande crece un pelo con el nivel del audio.
function startPulse() {
  if (!audioGraph() || cv.raf) return;
  const buf = new Uint8Array(512);
  const tick = () => {
    cv.raf = 0;
    if (!cvOn() || audio.paused) { $('#cvBig').style.transform = ''; return; }
    const lv = Math.max(...dk.an.map(an => level(an, buf)));
    $('#cvBig').style.transform = `scale(${1 + Math.max(0, lv - 0.45) * 0.07})`;
    cv.raf = requestAnimationFrame(tick);
  };
  cv.raf = requestAnimationFrame(tick);
}
function stopPulse() {
  if (cv.raf) { cancelAnimationFrame(cv.raf); cv.raf = 0; }
  $('#cvBig').style.transform = '';
}
