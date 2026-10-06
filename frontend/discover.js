// Biblioteca › Descubrir: buscar fuera de la colección, ver la discografía
// de un artista con lo que ya se tiene marcado, y saltar a artistas
// parecidos. Cada paso (una búsqueda, un artista) se apila para poder volver
// por las migas.
// Usa App, $, esc (downloads.js) y lib, srcTag, coverURL, openDetail,
// getAlbum, loadAlbums (library.js).

const disc = {
  stack: [],          // pasos: {type: 'search', q} | {type: 'artist', name, deezerId, mbid} | {type: 'foryou'}
  seq: 0,             // para descartar respuestas de un paso que ya no se ve
  onlyMissing: false, // discografía: esconder lo que ya se tiene
  hide: [],           // estilos (etiquetas de Last.fm) que no se quieren ver en las sugerencias
};
try {
  disc.onlyMissing = localStorage.getItem('discOnlyMissing') === '1';
  disc.hide = JSON.parse(localStorage.getItem('discHide') || '[]');
} catch (e) {}
$('#discHide').value = disc.hide.join(', ');
$('#discHide').onchange = () => {
  disc.hide = $('#discHide').value.split(',').map(s => s.trim().toLowerCase()).filter(Boolean);
  try { localStorage.setItem('discHide', JSON.stringify(disc.hide)); } catch (e) {}
  const top = disc.stack[disc.stack.length - 1];
  if (top && top.type !== 'search') discGo(top, false); // vuelve a pintar (de la caché, es rápido)
};
// Un sugerido con alguna de las etiquetas ocultas no se enseña.
const discHidden = a => disc.hide.length && (a.tags || []).some(t => disc.hide.some(h => t.includes(h)));

const kindLabel = { album: 'Discos', ep: 'EPs', live: 'En directo', compile: 'Recopilatorios', single: 'Sencillos' };

// openDiscover enseña la vista. Con `step`, va a ese paso; con `refresh`,
// vuelve a pedir el paso actual (al volver de un disco recién descargado, lo
// que se tiene ha cambiado).
window.openDiscover = (refresh, step) => {
  ['#libList', '#libBody', '#libDupesView', '#libRenameView', '#libOrgView', '#libCarryView'].forEach(s => $(s).hidden = true);
  $('#libDiscoverView').hidden = false;
  if (step) discGo(step, true);
  else if (refresh && disc.stack.length) discGo(disc.stack[disc.stack.length - 1], false);
  else if (!disc.stack.length) $('#discQuery').focus();
};

$('#libDiscover').onclick = () => openDiscover();
$('#discBack').onclick = () => { stopPreview(); $('#libDiscoverView').hidden = true; $('#libList').hidden = false; loadAlbums(); };
$('#discGo').onclick = () => { const q = $('#discQuery').value.trim(); if (q) discGo({ type: 'search', q }, true); };
$('#discQuery').onkeydown = e => { if (e.key === 'Enter') $('#discGo').click(); };
$('#discForYou').onclick = () => discGo({ type: 'foryou' }, true);

// Progreso de «para ti» (la primera vez son muchas consultas a Last.fm).
window.runtime.EventsOn('foryou', p => {
  const top = disc.stack[disc.stack.length - 1];
  const el = $('#discBody .foryou-progress');
  if (!top || top.type !== 'foryou' || !el) return;
  el.textContent = `cargando… ${p.total ? Math.round(p.done / p.total * 100) + ' %' : ''}`;
});

async function discGo(step, push) {
  if (push) {
    disc.stack.push(step);
    if (disc.stack.length > 12) disc.stack.shift();
  }
  renderCrumbs();
  $('#discErr').textContent = '';
  const my = ++disc.seq;
  const body = $('#discBody');
  if (step.type === 'search') {
    $('#discQuery').value = step.q;
    body.innerHTML = `<p class="empty">Buscando «${esc(step.q)}»…</p>`;
    try {
      const res = await App().DiscoverSearch(step.q);
      if (my === disc.seq) renderDiscSearch(res);
    } catch (e) { if (my === disc.seq) { body.innerHTML = ''; $('#discErr').textContent = e; } }
    return;
  }
  if (step.type === 'foryou') {
    body.innerHTML = '<p class="empty foryou-progress">cargando…</p>';
    try {
      const res = await App().ForYou();
      if (my === disc.seq) renderForYou(res);
    } catch (e) { if (my === disc.seq) { body.innerHTML = ''; $('#discErr').textContent = e; } }
    return;
  }
  body.innerHTML = `<p class="empty">Leyendo la discografía de ${esc(step.name)} (Deezer y MusicBrainz)…</p>`;
  try {
    const d = await App().ArtistDiscography(step.name, step.deezerId || '', step.mbid || '');
    if (my !== disc.seq) return;
    // Lo que se ha averiguado del artista se queda en el paso: volver a él
    // (migas, refresco) ya no tiene que buscarlo por el nombre.
    Object.assign(step, { deezerId: d.artist.deezerId, mbid: d.artist.mbid });
    renderDiscography(d);
    const sim = await App().SimilarArtists(step.name, step.deezerId || '', step.mbid || '');
    if (my === disc.seq) renderSimilar(sim);
  } catch (e) { if (my === disc.seq) { $('#discErr').textContent = e; } }
}

function renderCrumbs() {
  $('#discCrumbs').innerHTML = disc.stack.map((s, i) => {
    const text = s.type === 'search' ? `«${esc(s.q)}»` : s.type === 'foryou' ? 'para ti' : esc(s.name);
    return i === disc.stack.length - 1 ? `<b>${text}</b>` : `<a data-i="${i}">${text}</a>`;
  }).join(' › ');
  $('#discCrumbs').querySelectorAll('a').forEach(a => a.onclick = () => {
    disc.stack = disc.stack.slice(0, Number(a.dataset.i) + 1);
    discGo(disc.stack[disc.stack.length - 1], false);
  });
}

const discWarnings = w => (w || []).length ? `<p class="empty warn">${w.map(esc).join(' · ')}</p>` : '';

// ---- búsqueda ---------------------------------------------------------------
function renderDiscSearch(res) {
  const artists = res.artists || [], rels = res.releases || [];
  $('#discMeta').textContent = `${artists.length} artista(s) · ${rels.length} disco(s)`;
  let html = discWarnings(res.warnings);
  if (artists.length) html += `<h2>Artistas</h2>${artistGrid(artists)}`;
  if (rels.length) html += `<h2>Discos</h2>${releaseGrid(rels, true)}`;
  if (!artists.length && !rels.length) html += '<p class="empty">Nada. Prueba con otra forma del nombre, o «Artista - Disco».</p>';
  $('#discBody').innerHTML = html;
  wireArtists(artists);
  wireReleases(rels);
}

// ---- artista: discografía y parecidos ----------------------------------------
function renderDiscography(d) {
  const a = d.artist, rels = d.releases || [];
  const have = rels.filter(r => r.owned).length;
  $('#discMeta').textContent = `${rels.length} disco(s) · tienes ${have}`;
  const groups = Object.keys(kindLabel).map(k => [k, rels.filter(r => r.kind === k)]).filter(([, l]) => l.length);
  $('#discBody').innerHTML = `
    <div class="disc-head">
      ${a.picture ? `<img src="${esc(a.picture)}" alt="">` : '<span class="ph">♪</span>'}
      <div>
        <h3 style="margin:0">${esc(a.name)}</h3>
        <div class="meta">${a.owned ? `tienes ${a.owned} disco(s) suyo(s)` : 'no tienes nada suyo'}${a.fans ? ` · ${a.fans.toLocaleString()} fans en Deezer` : ''}${a.mbid ? ` · <a href="#" class="mblink">MusicBrainz</a>` : ''}</div>
      </div>
      <span class="spacer"></span>
      <button class="ghost" id="discRadio" title="radio de este artista y sus parecidos">📻 radio</button>
      <label class="meta"><input type="checkbox" id="discOnlyMissing" ${disc.onlyMissing ? 'checked' : ''}> solo lo que me falta</label>
    </div>
    ${discWarnings(d.warnings)}
    ${groups.map(([k, list]) => {
      const shown = disc.onlyMissing ? list.filter(r => !r.owned) : list;
      const head = `<h2>${kindLabel[k]} <span class="meta">${list.length}${list.some(r => r.owned) ? ` · tienes ${list.filter(r => r.owned).length}` : ''}</span></h2>`;
      if (!shown.length) return head + '<p class="empty">Los tienes todos.</p>';
      // Los sencillos pueden ser decenas: plegados hasta que se pidan.
      if (k === 'single') return head + `<div class="singles" hidden>${releaseGrid(shown, false)}</div><button class="ghost" id="discSingles">ver ${shown.length} sencillo(s)</button>`;
      return head + releaseGrid(shown, false);
    }).join('')}
    <div id="discSimilar"><h2>Parecidos</h2><p class="empty">Buscando artistas parecidos…</p></div>`;
  $('#discOnlyMissing').onchange = e => {
    disc.onlyMissing = e.target.checked;
    try { localStorage.setItem('discOnlyMissing', disc.onlyMissing ? '1' : ''); } catch (err) {}
    const sim = $('#discSimilar').innerHTML;
    renderDiscography(d);
    $('#discSimilar').innerHTML = sim; wireArtists(disc.similar || [], $('#discSimilar'));
  };
  $('#discRadio').onclick = () => { stopPreview(); startRadio(a.name); };
  const singles = $('#discSingles');
  if (singles) singles.onclick = () => { singles.previousElementSibling.hidden = false; singles.remove(); };
  const mb = $('#discBody').querySelector('a.mblink');
  if (mb) mb.onclick = e => { e.preventDefault(); window.runtime.BrowserOpenURL('https://musicbrainz.org/artist/' + a.mbid); };
  wireReleases(rels);
}

function renderSimilar(sim) {
  // Los que ya están en la colección, al final: esto es para descubrir.
  const all = sim.artists || [];
  const list = all.filter(a => !discHidden(a)).sort((x, y) => (x.owned > 0) - (y.owned > 0));
  disc.similar = list;
  const box = $('#discSimilar');
  if (!box) return;
  box.innerHTML = `<h2>Parecidos</h2>${discWarnings(sim.warnings)}` + (list.length ? artistGrid(list) : '');
  wireArtists(list, box);
}

// ---- para ti: a partir de toda la colección ----------------------------------
// Agrupado por estilo (la primera etiqueta de cada uno), para que lo que más
// abunda en la colección no tape lo demás. Un estilo con un solo sugerido se
// junta con el estilo que lo contiene («depressive black metal» → «black
// metal») o, si no hay, va a «otros».
function renderForYou(res) {
  const all = res.artists || [];
  const list = all.filter(a => !discHidden(a));
  $('#discMeta').textContent = '';
  const groups = new Map();
  for (const a of list) {
    const k = (a.tags || [])[0] || 'sin etiquetar';
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(a);
  }
  for (const [k, l] of [...groups]) {
    if (l.length > 2) continue;
    const parent = [...groups.keys()].find(p => p !== k && groups.get(p).length > 2 && k.includes(p));
    const to = parent || 'otros';
    if (!groups.has(to)) groups.set(to, []);
    groups.get(to).push(...l);
    groups.delete(k);
  }
  const ordered = [...groups].sort(([ka, a], [kb, b]) => (ka === 'otros') - (kb === 'otros') || b.length - a.length);
  $('#discBody').innerHTML = discWarnings(res.warnings) +
    ordered.map(([k, l], i) => `<div class="fy-group" data-g="${i}"><h2>${esc(k)} <span class="meta">${l.length}</span></h2>${artistGrid(l)}</div>`).join('');
  $('#discBody').querySelectorAll('.fy-group').forEach(g => wireArtists(ordered[Number(g.dataset.g)][1], g));
}

// ---- piezas: tarjetas de artistas y de discos --------------------------------
function artistGrid(list) {
  return `<div class="grid">${list.map((a, i) => `<div class="card artist ${pv.artist === a.name ? 'playing' : ''}" data-i="${i}" data-name="${esc(a.name)}" title="ver su discografía">
      ${a.picture ? `<img class="thumb" src="${esc(a.picture)}" loading="lazy" alt="">` : '<div class="thumb none">♪</div>'}
      ${a.owned ? `<span class="badge have">tienes ${a.owned}</span>` : ''}
      ${a.match ? `<span class="badge match" title="parecido según ${esc((a.sources || []).join(' y '))}">${Math.round(a.match * 100)}%</span>` : ''}
      <div class="title">${esc(a.name)}</div>
      <div class="sub">${(a.because || []).length ? `como ${esc(a.because.slice(0, 3).join(', '))}` : a.fans ? a.fans.toLocaleString() + ' fans' : (a.sources || []).includes('lastfm') ? 'Last.fm' : ''}</div>
      ${(a.tags || []).length ? `<div class="sub tags" title="estilo según Last.fm">${esc(a.tags.join(' · '))}</div>` : ''}
      ${a.listeners > 0 ? `<div class="sub" title="oyentes en Last.fm">${a.listeners.toLocaleString()} oyentes</div>` : ''}
      <div class="acts"><button class="ghost listen" title="Escuchar algo suyo para hacerte una idea: lo más escuchado en Deezer, un disco de Bandcamp o, si no, YouTube">▶ escuchar</button><button class="ghost dismiss" title="Ya lo conozco / no me interesa: no volverá a sugerirse">✕</button></div>
    </div>`).join('')}</div>`;
}
function wireArtists(list, root = $('#discBody')) {
  root.querySelectorAll('.card.artist').forEach(c => {
    const a = list[Number(c.dataset.i)];
    if (a) wireArtistCard(c, a);
  });
}
function wireArtistCard(c, a) {
  c.onclick = () => discGo({ type: 'artist', name: a.name, deezerId: a.deezerId || '', mbid: a.mbid || '' }, true);
  c.querySelector('button.listen').onclick = e => { e.stopPropagation(); previewArtist(a, c); };
  c.querySelector('button.dismiss').onclick = e => { e.stopPropagation(); dismissArtist(a, c); };
}

// «Ya lo conozco / no me interesa»: la tarjeta se queda como un aviso con
// «deshacer» hasta que se vuelva a pintar la lista.
async function dismissArtist(a, card) {
  try { await App().DismissArtist(a.name, false); } catch (e) { $('#discErr').textContent = e; return; }
  card.onclick = null;
  card.classList.add('dismissed');
  card.innerHTML = `<div class="title">${esc(a.name)}</div><div class="sub">no volverá a salir</div><div class="acts"><button class="ghost undo">deshacer</button></div>`;
  card.querySelector('button.undo').onclick = async e => {
    e.stopPropagation();
    try { await App().DismissArtist(a.name, true); } catch (err) { $('#discErr').textContent = err; return; }
    const tmp = document.createElement('div');
    tmp.innerHTML = artistGrid([a]);
    const fresh = tmp.querySelector('.card');
    fresh.dataset.i = card.dataset.i;
    card.replaceWith(fresh);
    wireArtistCard(fresh, a);
  };
}

function releaseGrid(rels, withArtist) {
  return `<div class="grid">${rels.map(r => {
    const u = coverURL(r, false);
    const alt = r.source === 'musicbrainz' && r.releaseGroupId ? `https://coverartarchive.org/release-group/${r.releaseGroupId}/front-250` : '';
    const sub = [(r.date || '').slice(0, 4), r.trackCount ? `${r.trackCount} pistas` : '', r.format && r.source === 'musicbrainz' ? r.format : ''].filter(Boolean).join(' · ');
    return `<div class="card rel ${r.owned ? 'have' : ''}" data-id="${esc(r.id)}" title="${esc(r.owned ? 'Lo tienes: ' + r.owned : r.title)}">
      ${u ? `<img class="thumb" src="${esc(u)}" loading="lazy" alt="" data-alt="${esc(alt)}" onerror="discImgFail(this)">` : '<div class="thumb none">♪</div>'}
      ${r.owned ? '<span class="badge have">✓ lo tienes</span>' : ''}
      <div class="title">${withArtist ? esc(r.artist) + ' — ' : ''}${esc(r.title)}</div>
      <div class="sub">${srcTag(r.source)} ${esc(sub)}</div>
      <div class="acts">${r.owned
        ? '<button class="ghost open">abrir</button>'
        : '<button class="ghost listen" title="Escucharlo antes: Bandcamp (entero), Deezer (30 s por pista) o YouTube">▶ escuchar</button><button class="ghost get" title="Crear su carpeta con la carátula y buscar cada pista en Soulseek, Bandcamp y YouTube">descargar</button>'}</div>
    </div>`;
  }).join('')}</div>`;
}
function wireReleases(rels) {
  const box = $('#discBody');
  box.querySelectorAll('.card.rel').forEach(c => {
    const r = rels.find(x => x.id === c.dataset.id);
    if (!r) return;
    c.classList.toggle('playing', !!pv.rel && pv.rel.id === r.id);
    const open = c.querySelector('button.open'), get = c.querySelector('button.get'), listen = c.querySelector('button.listen');
    if (open) open.onclick = () => { stopPreview(); lib.fromDiscover = true; openDetail(r.owned); };
    if (get) get.onclick = () => downloadRelease(r, get);
    if (listen) listen.onclick = () => previewRelease(r);
  });
}
function downloadRelease(r, btn) {
  lib.fromDiscover = true; $('#discErr').textContent = '';
  stopPreview();
  getAlbum(r, btn, err => $('#discErr').textContent = err);
}

// ---- escuchar antes de bajar ------------------------------------------------
// Go busca audio del disco (preview.go): Bandcamp entero, Deezer a trozos de
// 30 s o, si no hay, YouTube pista a pista bajo demanda. Se toca en su
// propio <audio>, aparte de la cola del reproductor; uno pausa al otro.
const pv = { rel: null, artist: '', tracks: [], i: -1, seq: 0, blob: '' };
const pvAudio = $('#pvAudio');

// Escuchar un disco concreto.
function previewRelease(r) {
  startPreview({
    rel: r, title: `${r.artist ? r.artist + ' — ' : ''}${r.title}`, searching: 'cargando…',
    load: () => App().PreviewRelease(r.id, r.artist || '', r.title),
    card: $('#discBody').querySelector(`.card.rel[data-id="${CSS.escape(r.id)}"]`),
  });
}
// Escuchar algo de un artista (los parecidos): lo más escuchado en Deezer, un
// disco suyo de Bandcamp o lo más escuchado en Last.fm vía YouTube.
function previewArtist(a, card) {
  startPreview({
    artist: a.name, title: a.name, searching: 'cargando…',
    load: () => App().ArtistSample(a.name, a.deezerId || ''), card,
  });
}

async function startPreview(o) {
  const my = ++pv.seq;
  pv.rel = o.rel || null; pv.artist = o.artist || ''; pv.tracks = []; pv.i = -1;
  $('#discPreview').hidden = false;
  $('#pvTitle').textContent = o.title;
  $('#pvMeta').textContent = o.searching;
  $('#pvTracks').innerHTML = '';
  $('#pvCover').hidden = true;
  const get = $('#pvGet');
  get.disabled = false; get.textContent = 'descargar';
  get.hidden = !o.rel;
  if (o.rel) get.onclick = () => downloadRelease(o.rel, get);
  markPlaying();
  try {
    const p = await o.load();
    if (my !== pv.seq) return;
    pv.tracks = p.tracks || [];
    const streams = pv.tracks.some(t => t.stream);
    const label = sourceLabel[p.release.source] || p.release.source;
    const src = o.rel && p.release.source === o.rel.source ? '' : ` (${label}: «${p.release.title}»)`;
    $('#pvMeta').textContent = p.top ? (streams ? 'Deezer · 30 s' : 'YouTube')
      : p.full ? 'Bandcamp' + src
      : streams ? 'Deezer · 30 s' + src
      : 'YouTube';
    // La muestra de un artista que es un disco de Bandcamp se puede bajar.
    if (!o.rel && !p.top && p.release.id) {
      const rel = { ...p.release, artist: p.release.artist || o.artist };
      get.hidden = false; get.onclick = () => downloadRelease(rel, get);
      $('#pvTitle').textContent = `${o.artist} — ${p.release.title}`;
    }
    const cover = coverURL(p.release, false) || (o.rel ? coverURL(o.rel, false) : '');
    if (cover) { $('#pvCover').src = cover; $('#pvCover').hidden = false; }
    // Si la tarjeta se había quedado sin imagen, la carátula que ha aparecido.
    const ph = cover && o.card && o.card.querySelector('.thumb.none');
    if (ph) ph.replaceWith(Object.assign(document.createElement('img'), { className: 'thumb', src: cover, alt: '' }));
    renderPvTracks();
    // Empieza solo con lo que suena sin esperar; la muestra de un artista
    // también con YouTube, que para eso se ha pedido.
    const first = pv.tracks.findIndex(t => t.stream);
    if (first >= 0) pvPlay(first);
    else if (p.top && pv.tracks.length) pvPlay(0);
  } catch (e) { if (my === pv.seq) $('#pvMeta').textContent = 'no he podido buscar audio: ' + e; }
}

function renderPvTracks() {
  $('#pvTracks').innerHTML = pv.tracks.map((t, i) => `<div class="pvt ${i === pv.i ? 'now' : ''}" data-i="${i}" title="${t.stream ? '' : 'buscar en YouTube'}">
      <span class="n">${i === pv.i && !pvAudio.paused ? '▶' : t.number || i + 1}</span>
      <span class="t">${esc(t.title)}${t.album ? ` <span class="dim">· ${esc(t.album)}</span>` : ''}${t.stream ? '' : ' <span class="src deezer">YouTube</span>'}${t.state ? ` <span class="dim">${esc(t.state)}</span>` : ''}</span>
      <span class="meta">${t.length ? fmtDur(t.length) : ''}</span>
    </div>`).join('');
  $('#pvTracks').querySelectorAll('.pvt').forEach(el => el.onclick = () => pvPlay(Number(el.dataset.i)));
}

async function pvPlay(i) {
  const t = pv.tracks[i];
  if (!t) return;
  const my = ++pv.seq;
  pv.i = i;
  pvAudio.pause();
  try {
    let src;
    if (t.stream) src = '/preview?u=' + encodeURIComponent(t.stream);
    else {
      if (!t.local) {
        t.state = 'buscando en YouTube…'; renderPvTracks();
        t.local = await App().PreviewYouTube(t.artist || (pv.rel && pv.rel.artist) || pv.artist, t.title);
      }
      src = '/media?p=' + encodeURIComponent(t.local);
    }
    t.state = 'cargando…'; renderPvTracks();
    // Como el reproductor: fetch → Blob, sin depender de cómo pida WebView2
    // el audio de un <audio>.
    const resp = await fetch(src);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const blob = await resp.blob();
    if (my !== pv.seq) return;
    if (pv.blob) URL.revokeObjectURL(pv.blob);
    pv.blob = URL.createObjectURL(blob);
    pvAudio.src = pv.blob;
    t.state = '';
    await pvAudio.play();
  } catch (e) {
    if (my === pv.seq) t.state = 'no se puede escuchar: ' + (e && e.message ? e.message : e);
  }
  if (my === pv.seq) renderPvTracks();
}

function stopPreview() {
  pv.seq++;
  pvAudio.pause();
  pv.rel = null; pv.artist = '';
  $('#discPreview').hidden = true;
  markPlaying();
}
function markPlaying() {
  $('#discBody').querySelectorAll('.card.rel').forEach(c => c.classList.toggle('playing', !!pv.rel && pv.rel.id === c.dataset.id));
  $('#discBody').querySelectorAll('.card.artist').forEach(c => c.classList.toggle('playing', !!pv.artist && pv.artist === c.dataset.name));
}

$('#pvClose').onclick = stopPreview;
// Al acabar una pista, la siguiente que se pueda escuchar sin esperar (las
// de YouTube, solo si se piden).
pvAudio.onended = () => {
  const next = pv.tracks.findIndex((t, j) => j > pv.i && (t.stream || t.local));
  if (next >= 0) pvPlay(next); else renderPvTracks();
};
pvAudio.onplay = () => {
  soloAudio(pvAudio); // el reproductor y la radio, en pausa mientras se escucha esto
  renderPvTracks();
};
pvAudio.onpause = renderPvTracks;

// Carátula que no carga: la del grupo de ediciones y, si tampoco, una nota.
window.discImgFail = img => {
  if (img.dataset.alt) { img.src = img.dataset.alt; img.dataset.alt = ''; return; }
  img.replaceWith(Object.assign(document.createElement('div'), { className: 'thumb none', textContent: '♪' }));
};
