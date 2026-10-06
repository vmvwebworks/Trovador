// Pestaña Radio: canciones de los artistas de «para ti», una detrás de otra,
// sin tener que buscar nada (radio.go). Mientras suena una se prepara la
// siguiente: las de YouTube tardan unos segundos en bajar.
// ♥ guarda la canción con su disco en «Te han gustado» (y el artista cuenta
// para el gusto); ✕ salta y descarta al artista; desde la lista se baja el
// disco con el flujo de siempre (Biblioteca › Completar).
// Usa App, $, esc, soloAudio (downloads.js), fmtDur, coverURL, getAlbum
// (library.js) y openDiscover (discover.js).

const rd = {
  seed: '',       // artista de la radio ('' = para ti)
  cur: null,      // radioTrack que suena
  next: null,     // Promise de la siguiente
  blob: '',
  seq: 0,
  started: false,
  liked: false,
};
const rdAudio = $('#radioAudio');

// Pedir canción: si falla (Last.fm caído, nada que poner), se reintenta un
// par de veces antes de rendirse.
async function rdFetch() {
  let last;
  for (let i = 0; i < 3; i++) {
    try { return await App().RadioNext(rd.seed); } catch (e) { last = e; }
  }
  throw last;
}

function rdStatus(msg) { $('#rdStatus').textContent = msg || ''; }

async function rdPlay(t) {
  const my = ++rd.seq;
  rd.cur = t; rd.liked = false;
  rdRender();
  rdStatus('cargando…');
  try {
    const src = t.local ? '/media?p=' + encodeURIComponent(t.local) : '/preview?u=' + encodeURIComponent(t.stream);
    const resp = await fetch(src);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const blob = await resp.blob();
    if (my !== rd.seq) return;
    if (rd.blob) URL.revokeObjectURL(rd.blob);
    rd.blob = URL.createObjectURL(blob);
    rdAudio.src = rd.blob;
    await rdAudio.play();
    rdStatus('');
  } catch (e) {
    if (my !== rd.seq) return;
    rdStatus('no se puede escuchar: siguiente');
    setTimeout(() => my === rd.seq && rdAdvance(), 1500);
    return;
  }
  // La siguiente, ya, mientras suena esta.
  rd.next = rdFetch();
  rd.next.catch(() => {});
}

async function rdAdvance() {
  const my = ++rd.seq;
  rdAudio.pause();
  rdStatus('cargando…');
  setBusy(true);
  try {
    const t = await (rd.next || rdFetch());
    rd.next = null;
    if (my !== rd.seq) return;
    await rdPlay(t);
  } catch (e) {
    rd.next = null;
    if (my === rd.seq) rdStatus('Error: ' + e);
  } finally { setBusy(false); }
}

function setBusy(on) { $('#rdNext').disabled = on || !rd.started; }

const rdSrcLabel = { bandcamp: 'Bandcamp', youtube: 'YouTube', deezer: 'Deezer · 30 s' };

function rdRender() {
  const t = rd.cur;
  ['#rdNext', '#rdLike', '#rdNope', '#rdArtistBtn'].forEach(s => $(s).disabled = !t);
  $('#rdPlay').textContent = !rd.started ? '▶ empezar' : rdAudio.paused ? '▶' : '⏸';
  $('#radioAudio').hidden = !t;
  if (!t) return;
  $('#rdSource').textContent = rdSrcLabel[t.source] || t.source;
  $('#rdTitle').textContent = t.title;
  $('#rdArtist').innerHTML = `<b>${esc(t.artist)}</b>${t.album ? ` · ${esc(t.album)}` : ''}`;
  $('#rdWhy').textContent = (t.because || []).length ? 'como ' + t.because.slice(0, 3).join(', ') : '';
  $('#rdTags').textContent = (t.tags || []).join(' · ');
  rdCover(t);
  $('#rdLike').classList.toggle('on', rd.liked);
  $('#rdLike').textContent = rd.liked ? '♥ guardada' : '♥ me gusta';
  document.title = `${rdAudio.paused ? '' : '📻 '}${t.title} — ${t.artist} · Trovador`;
}

// La portada: la del disco y, si no hay o no carga, la miniatura de YouTube
// o la foto del artista.
let rdCovers = [], rdCoverOf = null;
function rdCover(t) {
  if (rdCoverOf === t) return; // ya puesta (rdRender se llama a menudo)
  rdCoverOf = t;
  rdCovers = [t.cover && coverURL({ coverUrl: t.cover }, true), ...(t.coverAlt || [])].filter(Boolean);
  rdNextCover();
}
function rdNextCover() {
  const u = rdCovers.shift();
  $('#rdCover').hidden = !u; $('#rdPh').hidden = !!u;
  if (u) $('#rdCover').src = u;
}
$('#rdCover').onerror = rdNextCover;

// Radio de un artista (él y sus parecidos), desde aquí o desde Descubrir.
// Sin artista, «para ti».
window.startRadio = seed => {
  rd.seed = (seed || '').trim();
  rd.next = null; // la que estaba preparada era de la otra radio
  rd.started = true;
  $('#rdSeed').value = '';
  rdRenderMode();
  showTab('radio');
  rdAdvance();
};
function rdRenderMode() {
  $('#rdMode').textContent = rd.seed ? `Radio de ${rd.seed}` : 'Para ti';
  $('#rdModeClear').hidden = !rd.seed;
}
$('#rdSeedGo').onclick = () => { const s = $('#rdSeed').value.trim(); if (s) startRadio(s); };
$('#rdSeed').onkeydown = e => { if (e.key === 'Enter') $('#rdSeedGo').click(); };
$('#rdModeClear').onclick = () => startRadio('');

$('#rdPlay').onclick = () => {
  if (!rd.started) {
    rd.started = true;
    rdStatus('cargando…');
    rdAdvance();
    return;
  }
  if (!rd.cur) return;
  if (rdAudio.paused) rdAudio.play(); else rdAudio.pause();
};
$('#rdNext').onclick = rdAdvance;
$('#rdNope').onclick = async () => {
  const t = rd.cur;
  if (!t) return;
  try { await App().DismissArtist(t.artist, false); } catch (e) {}
  rdAdvance();
};
$('#rdLike').onclick = async () => {
  const t = rd.cur;
  if (!t || rd.liked) return;
  try {
    renderLikes(await App().RadioLike(t));
    rd.liked = true; rdRender();
  } catch (e) { rdStatus('Error: ' + e); }
};
$('#rdArtistBtn').onclick = () => rd.cur && goArtist(rd.cur.artist, rd.cur.deezerId);

// Espacio = pausa/sigue en la pestaña Radio (en las demás, el reproductor).
document.addEventListener('keydown', e => {
  if (e.code !== 'Space' || /INPUT|TEXTAREA|SELECT|BUTTON/.test(e.target.tagName) || !$('#tab-radio').classList.contains('active') || !rd.cur) return;  e.preventDefault();
  if (rdAudio.paused) rdAudio.play(); else rdAudio.pause();
});

rdAudio.onended = rdAdvance;
rdAudio.onplay = () => { soloAudio(rdAudio); rdRender(); };
rdAudio.onpause = rdRender;

// Mientras se calcula «para ti» la primera vez.
window.runtime.EventsOn('foryou', p => {
  if (rd.started && !rd.cur) rdStatus(`cargando… ${p.total ? Math.round(p.done / p.total * 100) + ' %' : ''}`);
});

// ---- te han gustado ------------------------------------------------------------
function renderLikes(list) {
  $('#rdLikesMeta').textContent = list.length || '';
  $('#rdLikesBox').hidden = !list.length;
  if (!list.length) return;
  $('#rdLikes').innerHTML = list.map((l, i) => `<div class="rd-like" data-i="${i}">
      ${l.cover ? `<img class="mini" src="${esc(coverURL({ coverUrl: l.cover }, false))}" alt="" loading="lazy">` : '<span class="mini none">♪</span>'}
      <div class="t"><b>${esc(l.title)}</b><span>${esc(l.artist)}${l.album ? ' · ' + esc(l.album) : ''}</span></div>
      ${l.albumKey ? '<button class="ghost get">descargar el disco</button>'
        : '<button class="ghost find" title="No se sabe de qué disco es: ver su discografía en Descubrir">buscar el disco</button>'}
      <button class="ghost artist">artista</button>
      <button class="ghost unlike" title="Quitar de la lista">✕</button>
    </div>`).join('');
  $('#rdLikes').querySelectorAll('.rd-like').forEach(row => {
    const l = list[Number(row.dataset.i)];
    const get = row.querySelector('button.get'), find = row.querySelector('button.find');
    if (get) get.onclick = () => {
      showTab('library');
      lib.fromDiscover = false;
      getAlbum({ id: l.albumKey, artist: l.artist, title: l.album }, get, err => rdStatus('Error: ' + err));
    };
    if (find) find.onclick = () => goArtist(l.artist, '');
    row.querySelector('button.artist').onclick = () => goArtist(l.artist, '');
    row.querySelector('button.unlike').onclick = async () => {
      try { renderLikes(await App().RadioUnlike(l.artist, l.title)); } catch (e) { rdStatus('Error: ' + e); }
    };
  });
}

// El artista en Biblioteca › Descubrir (discografía y parecidos).
function goArtist(name, deezerId) {
  showTab('library');
  openDiscover(false, { type: 'artist', name, deezerId: deezerId || '', mbid: '' });
}

window.radioOnShow = async () => {
  try { renderLikes(await App().RadioLikes() || []); } catch (e) {}
};
