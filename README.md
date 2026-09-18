# Trovador

App de escritorio (Windows) para descargar música de YouTube, YouTube Music y
Bandcamp como MP3 con carátula y etiquetas. Vídeos sueltos, playlists,
álbumes, y discos enteros en un solo vídeo (los trocea por capítulos).

Por dentro es un programa en **Go** con ventana **Wails** (WebView2) que
orquesta [yt-dlp](https://github.com/yt-dlp/yt-dlp) y **ffmpeg**. El código
está comentado para que sirva de introducción a Go.


La app se llamó `music_picker` hasta septiembre de 2026; el nombre cambió a
Trovador (el que va de plaza en plaza recogiendo y llevando canciones). El
exe es ahora `build\bin\Trovador.exe`, y la carpeta de datos
`%APPDATA%\Trovador`: si al arrancar encuentra la antigua
`%APPDATA%\music_picker` y no la nueva, la renombra, así que ajustes,
caché, favoritos y encuadres siguen ahí. El módulo Go y la carpeta del
proyecto conservan el nombre antiguo.


## Distribución

Trovador se reparte **portable**: un zip con `Trovador.exe` en las
[releases de GitHub](https://github.com/vmvwebworks/Trovador/releases). No hay
instalador: se saca el exe a una carpeta y ya; las herramientas se las
descarga él la primera vez a `bin\` junto al exe, y los datos van a
`%APPDATA%\Trovador`. Para actualizar, sustituir el exe por el nuevo.

Publicar una versión es etiquetarla: `git tag v1.0.0 && git push --tags`.
El flujo `.github/workflows/release.yml` compila en un runner de Windows con
la versión dentro (`-ldflags "-X main.version=1.0.0"`; sale en la cabecera
de la app) y publica el zip. En cada push, `test.yml` pasa `go vet` y los
tests unitarios (los de integración necesitan las herramientas y se pasan
a mano). La página del proyecto (`docs/`, GitHub Pages) está en
https://vmvwebworks.github.io/Trovador/.
## Uso

Ejecuta `build/bin/Trovador.exe`. La primera vez descarga yt-dlp, ffmpeg
y deno (~230 MB) a `bin/` junto al exe, desde sus repositorios oficiales.

Pega enlaces (uno por línea), elige formato y pulsa **Descargar** (o
Ctrl+Enter). Cada trabajo muestra su progreso, su log y se puede cancelar.

- Vídeos sueltos → `Título.mp3` en la carpeta de destino.
- Playlists y álbumes → subcarpeta con su nombre, pistas numeradas.
- Vídeo con capítulos (disco entero) → subcarpeta con una pista por capítulo,
  con título, número y álbum en las etiquetas; el fichero largo se borra.
- Un "Mix" de YouTube (`list=RD...`) baja solo el vídeo del enlace.

**Ajustes**: carpeta de destino (por defecto `Música\Trovador`) y
descargas en paralelo. Se guardan en `%APPDATA%\Trovador\config.json`.

`<destino>\.descargado.txt` es el registro de lo ya bajado: al repetir un
enlace se salta aunque hayas movido el fichero. Borra su línea para
volver a bajarlo.

Si YouTube deja de funcionar, casi siempre basta con **Actualizar yt-dlp**
(botón arriba a la derecha; ejecuta `yt-dlp -U`).

El exe y su carpeta `bin/` se pueden mover juntos a donde quieras.

## Biblioteca

La segunda pestaña comprueba y arregla discos ya descargados contra varias
fuentes, consultadas a la vez desde la app:

- **MusicBrainz** (+ Cover Art Archive): la referencia; sin clave. Búsqueda
  exacta y aproximada (tolerante a erratas y acentos).
- **Deezer**: API pública sin clave; duraciones y carátulas grandes.
- **Bandcamp**: su buscador y la página del álbum (tracklist con duraciones
  y carátula). Donde suele estar lo underground.
- **Discogs**: la mejor para demos, cintas y tiradas pequeñas. Necesita un
  token personal gratuito (discogs.com → Settings → Developers → Generate
  token) que se pega en Ajustes; sin él, esa fuente no se consulta.

La Biblioteca lista **todos los discos que cuelgan de la carpeta elegida, a
cualquier profundidad**: cada carpeta con ficheros de audio dentro es un
disco (`Colección\Artista\Disco` se ve como `Artista\Disco`), más los
ficheros sueltos de la propia carpeta. Las carpetas `_original`,
`_duplicados` y las que empiezan por `.` no cuentan.

Los nombres no tienen que coincidir letra a letra: el análisis limpia años y
sufijos del nombre de carpeta (`2014 - Flögo de bort (Demo)`), prueba varias
interpretaciones, y las pistas se emparejan con los ficheros por **parecido
de título + duración**, no por orden. *Etiquetar* usa ese emparejamiento para
fijar nombres y números correctos aunque los ficheros estén desordenados o
mal nombrados (los que no casan con ninguna pista se dejan como están).

**Búsqueda en cascada**: el botón *Buscar* de la ficha prueba, en orden:
(1) por artista/álbum; (2) si no hay nada, por las canciones: busca 3-4
títulos de pista y propone las ediciones que los contienen ("4/4 canciones"),
lo que identifica un disco solo por sus temas ("1998 - Demo" → *Frost –
Under the Hungarian Blackmoon*); (3) si ningún disco contiene esas canciones,
**orígenes**: la carpeta es una selección, y se busca de qué disco viene cada
fichero (por su etiqueta de álbum o por el título), agrupando el resultado
por disco de origen con carátula, año, pista y si la duración cuadra; (4) si
tampoco, un enlace para buscar en el navegador. El análisis en lote usa los
pasos 1 y 2. En orígenes, los ficheros que solo cuadran por duración pero no
por título salen como **dudosos**.

**Crear álbum**: en la lista de orígenes, cada disco tiene *Crear álbum y
mover N*: crea la carpeta `Artista - Álbum (Año)` junto a la actual, baja la
carátula oficial y lleva allí los ficheros de ese disco ya curados (nombre
`NN - Título`, etiquetas de la edición, carátula incrustada).

**Pista suelta**: si la carpeta tiene un solo fichero y es una de las pistas
de la edición elegida (misma duración y título parecido), la ficha lo dice
(cuál es) y ofrece *Crear álbum con esta pista*: crea la carpeta del disco,
lleva allí el fichero curado, borra la carpeta de origen si queda vacía y
abre *Completar* para traer el resto. En el lote, confirmar una propuesta en
estado *pista suelta* hace lo mismo al aplicar. Estas carpetas nunca se
renombran al nombre del disco: solo se renombra cuando la edición cuadra.

**Descargar un disco desde la búsqueda**: cada resultado de la ficha tiene
*descargar*: crea la carpeta `Artista - Álbum (Año)` en la carpeta de
descargas con la carátula y abre *Completar* con todas las pistas por
traer. Sirve para la discografía que sale al buscar solo por artista.

**Completar**: para un álbum al que le faltan pistas (recién creado, o desde
la comparación de cualquier disco con *Completar*), busca cada pista que
falta en **Soulseek** (si está conectado: una búsqueda "artista álbum" y
localiza las pistas en las carpetas de otros usuarios, con formato, bitrate y
si hay hueco libre), en **Bandcamp** (si el disco está allí, la URL exacta
de cada pista: yt-dlp la baja como un vídeo) y en **YouTube** (yt-dlp,
pista a pista), y propone candidatos ordenados por duración y título;
Soulseek delante, luego Bandcamp, FLAC y hueco libre primero. Se marcan y *Descargar marcadas* las baja a la carpeta del
álbum, nombradas, etiquetadas y con la carátula (las de Soulseek esperan a
que llegue la transferencia; con cola en el otro extremo pueden tardar).

**Mejorar en Soulseek**: en la ficha de un disco con edición elegida, busca
el disco en Soulseek y, por cada pista que tienes, ofrece versiones claramente
mejores: sin pérdida (FLAC/WV/APE) frente a mp3, o un salto de bitrate de al
menos ×1,4 y 64 kbps (128→192 sí; 256→320 no), siempre con la misma duración
(±3 s). *Sustituir marcadas* descarga la nueva, la deja curada (nombre,
etiquetas, carátula) y aparta la antigua a `_original` dentro de la carpeta
del disco; si la descarga falla, la antigua vuelve a su sitio.

**Duplicados**: el botón *duplicados* de la lista agrupa las carpetas de la
raíz que son el mismo disco (por artista/álbum deducido, con el nombre
limpio: "ZVERI ｜ Full Album" = "ZVERI"), identifica la edición y compara la
integridad de cada copia: pistas, duración, bitrate, carátula, etiquetas y
estado frente a la edición. Recomienda la mejor copia; *Fusionar en la
marcada* le trae las pistas que le falten de las otras y aparta las carpetas
sobrantes enteras a `_duplicados` (nada se borra).

**En lote**: elige una carpeta raíz (por defecto la de descargas) y la app
detecta cada disco (subcarpeta con audio). *Analizar todo* busca sola la
edición de cada uno —a partir de las etiquetas y del nombre de la carpeta,
quitando ruido como `[SWE]` o `(Full Album)`— y se queda con la que cuadra
en duraciones. Estados: **íntegro**, **separable** (un fichero largo que dura
lo que la edición), **parcial** (faltan pistas, las que hay cuadran), **con
extras** (sobran ficheros, pero el disco está entero), **pista suelta** (un
solo fichero que es una de las pistas del disco: lo típico al traerse un tema
suelto de YouTube), **no cuadra** (revisar a mano), **no encontrado**.

Nada se aplica sin tu visto bueno: el análisis deja cada edición como
**propuesta**. Puedes *Confirmar* una a una, *Aceptar propuestas* (las que
cuadran, de golpe), o desplegar *ver N candidatas* para ver todo lo que
encontró la búsqueda (evaluado o no) y elegir otra. Si ninguna fuente lo
tiene, *navegador* abre la búsqueda en Google/Discogs/Bandcamp, y se puede
pegar una URL de MusicBrainz en la caja para usarla. Luego *Aplicar a
marcados* hace lo que corresponda a cada disco confirmado: carátula a los que
no tienen, separar los separables, etiquetar los íntegros. MusicBrainz admite
1 petición/segundo: cuenta 3-8 s por disco.

**Vistas**: miniaturas (carátula de `cover.jpg` o la incrustada en el primer
fichero, nombre, nº de pistas y estado) o lista (tabla con estado, edición,
candidatas y confirmación). Con más de 50 discos empiezan desmarcados; *marcar
todos* y las casillas eligen sobre qué actúan *Analizar marcados* y *Aplicar*.

**Ficha del disco**: pinchando en un disco se entra en su ficha: carátula
grande, resumen (artista/álbum según etiquetas, pistas, duración, formato,
bitrate, tamaño, cuántas llevan carátula), la carpeta, y una tabla con la
información real de **cada fichero** leída con ffprobe: título, artista,
álbum, nº de pista, año, duración, códec y bitrate, y si lleva carátula
(las etiquetas ausentes se marcan en rojo). Debajo, la búsqueda manual de la
edición (CD, vinilo, digital, año, país...; la columna Pistas en verde cuando
coincide) y la comparación pista a pista (Δ en verde ±3 s, ámbar ±10 s, rojo
más).

Acciones (en lote o en el detalle):

- **Carátula**: descarga la portada oficial a `cover.jpg` y la incrusta en
  todas las pistas (mp3, m4a, flac). La portada es la de la fuente de la
  edición (Cover Art Archive para MusicBrainz; Deezer, Bandcamp y Discogs
  dan la suya); si esa fuente no la tiene, se busca el mismo disco en las
  demás (mismo título y artista, preferiblemente mismas pistas) y se toma
  la de la primera que la tenga. El registro dice de dónde salió.
  Las listas de ediciones (ficha y candidatas del lote) enseñan una
  miniatura de la carátula de cada una, para elegir viendo; el botón
  *carátula* de cada fila la baja e incrusta sin cambiar de edición, y la
  edición elegida muestra la suya en grande junto al título.
- **Separar pistas**: si la carpeta tiene un solo fichero largo que dura lo
  que la edición, lo corta con las duraciones oficiales (sin recodificar),
  nombra y etiqueta cada pista, y aparta el original a `_original`.
  Si la edición no trae duraciones (Discogs a veces), se toman de otra
  fuente que tenga el mismo disco (emparejando las pistas por título; la
  ficha lo indica: «duraciones de bandcamp»). Y si ninguna las tiene, la
  ficha ofrece **Detectar silencios** —busca los huecos entre canciones y
  propone los cortes— o pegar las **marcas de tiempo** (por ejemplo, la
  lista de la descripción del vídeo: se ignora el texto), y **Separar con
  estas marcas** corta ahí con los títulos de la edición.
- **Etiquetar**: renombra a `NN - Título.ext` y escribe título, artista,
  álbum, número y año según la edición (emparejando por parecido).

Elegir una edición que cuadra (íntegra, separable, parcial o con extras) —al pinchar un resultado
en la ficha o al confirmar en el lote— **renombra la carpeta** en ese
momento (el título a secas dentro de la carpeta del artista; `Artista -
Álbum (Año)` si está suelta); aplicar una acción también lo hace. Si ya existe
otra carpeta con ese nombre, no se mezclan: avisa y se queda. La tarjeta y la
ficha se refrescan solas.

### Renombrar carpetas en lote

El botón **renombrar** de la Biblioteca pone a cada carpeta de disco el
nombre que le toca, el mismo que deja el renombrado por edición, sin ir una
por una: coge los discos marcados, o **todos** si no hay ninguno marcado.

El nombre depende de dónde cuelgue la carpeta. Dentro de la carpeta de su
artista (`Colección\Artista\Disco`) se queda con el **título a secas**:
`Bathory\Hammerheart`, no `Bathory\Bathory - Hammerheart (1990)`, porque el
artista ya lo dice la ruta y el año está en las etiquetas. Suelta —la
carpeta de descargas es plana— lleva `Artista - Álbum (Año)` para que se
explique sola. El nombre sale de la edición elegida en el análisis si
la hay y, si no, de las **etiquetas** de las pistas (que en un disco ya
etiquetado son las de su edición, y en uno recién bajado suelen estar más
limpias que `..._-_Vinyl-2013-EMS`); el nombre de la carpeta es el último
recurso. Nada se toca hasta confirmar: se enseña el cambio de cada carpeta,
de dónde sale el nombre y, en las que se quedan, por qué (ya se llama así,
ya existe una carpeta igual al lado, dos discos del plan acabarían
llamándose igual, no se sabe cómo se llama). Cada fila se puede desmarcar.

Las propuestas que **pierden alguna palabra** del nombre de ahora llevan un
aviso que dice cuál: quitar `FLAC`, `320` o un año repetido no avisa, pero
un split o una recopilación con las etiquetas de otro disco sí, que es donde
la propuesta suele estar mal. *Desmarcar las avisadas* las quita todas de
golpe para aplicar solo lo evidente. Los ficheros de dentro no se tocan.

### Ordenar por artista

La colección va como `Colección\Artista\Disco`. El botón **por artista** de
la Biblioteca coge los discos marcados y propone dónde iría cada uno: busca
la carpeta del artista en la colección (sin distinguir mayúsculas, acentos
ni "The Cure" / "Cure, The"; si existe, manda su grafía) y, si no la hay, la
crea. El artista sale de la edición elegida en el análisis (que trae los
acreditados por separado) o, si no, de las etiquetas y el nombre de la
carpeta; en un disco entre varios artistas ("Oliphant / Sequentia", "A
feat. B") manda el **primero acreditado**. "&" y "," no parten nada (Simon
& Garfunkel es un solo artista). La colección es la carpeta de la
Biblioteca salvo que se cambie en el panel. Se enseña el plan —quién se
mueve, quién se queda y por qué (ya está en su carpeta, no se sabe el
artista, ya existe una carpeta igual)— y **Mover** lo aplica; nunca se
mezclan carpetas. El icono de carpeta viaja con el disco.

### Llevarse discos (el USB del coche)

El botón **llevarse…** de la Biblioteca copia los discos
marcados (o todos) a otra carpeta, pensado para el USB del coche. Cada
disco va en su carpeta «Artista - Álbum» (o Artista\Álbum), con nombres
aptos para FAT32 (sin `<>:"/\|?*`, sin punto final), y solo con el audio y
las carátulas: ni `desktop.ini`, ni `folder.ico`, ni `_original`. Si se
marca *a MP3 lo que no lo sea* (por defecto), lo que no sea MP3 se
convierte al copiar (320 kb/s o V0, con etiquetas y carátula; el original
no se toca). Antes de copiar enseña el plan: destino de cada disco,
pistas, cuántas se convierten, cuánto ocupa (a ojo en las convertidas) y
si cabe en el destino; los que ya estén (con todas sus pistas) se saltan
salvo que se pida repetirlos. Avance por pista y cancelable; el destino y
las opciones se recuerdan.

### Iconos en el Explorador

Las carpetas de discos pueden verse en el Explorador de Windows como lo que
son: un **CD** con la carátula impresa, un **vinilo** negro asomando de su
funda, una **cinta** con la carátula de etiqueta, o una **funda** plana
(descargas o formato desconocido). El dibujo se genera con la carátula del
disco y se deja como icono de la carpeta: un `desktop.ini` y un `folder.ico`
ocultos dentro (rutas relativas: la carpeta se puede mover) y el atributo de
solo lectura en la carpeta, que es la marca que usa el propio Explorador
para "carpeta personalizada" y no impide tocar lo que hay dentro. Al pasar
el ratón, el Explorador enseña artista, disco, formato y número de pistas.

El formato sale, por este orden, de lo que fijes a mano en la ficha
(selector *Explorador*), del formato de la edición elegida (MusicBrainz,
Discogs...), o de la etiqueta `media` de los ficheros (la escriben Picard y
esta app al etiquetar). Con *Iconos de carpeta en el Explorador* activo en
Ajustes (lo está por defecto), el icono se crea o actualiza solo al aplicar
carátula o etiquetas, al crear un álbum y al terminar una descarga que crea
carpeta (lista o álbum de Bandcamp: funda). Para la colección entera, el
botón **iconos Explorador** de la Biblioteca recorre la carpeta elegida a
cualquier profundidad y pone icono a todos los discos (varios a la vez;
con progreso y cancelable); guarda en el desktop.ini una firma de con qué
se hizo, así que la siguiente pasada salta los que no han cambiado y solo
rehace los que tienen carátula o pistas nuevas. En la ficha de un disco se
puede fijar el tipo a mano o *quitar* el icono. Si el Explorador tarda en
enseñar el cambio, F5 en la carpeta.

Las **carpetas de artista** (las que contienen discos) también reciben
icono en esa misma pasada y al ordenar por artista: el **logo** del grupo si
existe (TheAudioDB), si no su **foto** (TheAudioDB, Discogs con token, Deezer
o Bandcamp), y
si no hay nada, un abanico con las carátulas de sus discos. Para no
colgarle a un grupo la foto de otro que se llama igual, el artista se
identifica siempre a través de uno de sus discos (el MBID que dejan las
etiquetas —*Etiquetar* lo escribe— o una búsqueda del disco en la fuente);
nunca por el nombre a secas: antes sin foto que con la de otro. La imagen
se guarda en la carpeta como `logo.png` o `folder.jpg` (compatibles con
Kodi y otros) y no se vuelve a pedir; si pones tú un `logo.png`, `folder.jpg`
o `artist.jpg`, manda el tuyo.

## Reproductor

La cuarta pestaña reproduce la colección. El audio suena en la propia
ventana (WebView2): cada pista se pide a Go por HTTP (`/media?p=ruta`, en
`player.go`), se guarda en memoria y se reproduce desde ahí, con lo que
saltar dentro de la pista es inmediato.

- **Buscar**: la caja de arriba filtra por canción, artista, disco o
  carpeta (varias palabras = todas). En la vista de discos, los que cuadran
  por alguna canción la enseñan bajo la carátula y, si hay pocos, salen
  desplegados.
- **Discos**: cuadrícula de toda la colección (la carpeta de *Colección*,
  por defecto la de descargas; se cambia ahí mismo) con carátula, título y
  artista. Pulsar un disco lo **despliega**: sus pistas una a una, cada una
  con ▶ (sonar ya) y + (añadir a la lista), y *+ disco entero* / *▶ disco*.
  ▶ sobre la carátula reproduce el disco sin desplegarlo. Los discos que
  están en la lista llevan el borde marcado.
- **Canciones**: todas las pistas de la colección en una tabla (canción,
  artista, disco), con ▶ y + en cada fila; con el filtro se acota.

- **Lista de reproducción** (a la derecha): pistas agrupadas por disco;
  pulsar una la reproduce; ✕ la quita; *barajar* y *vaciar*. Se guarda al
  cerrar y se recupera al abrir (sin sonar sola).
- **Barra de reproducción** (abajo, en todas las pestañas): carátula, pista,
  anterior / reproducir-pausa / siguiente, posición y volumen. Espacio =
  reproducir/pausa cuando no se está escribiendo.
- **▶ en la Biblioteca**: cada fichero de la ficha (tabla de ficheros y
  columna *Fichero local* de la comparación) tiene ▶ para escucharlo: se
  pone delante de lo que quede en la lista y suena ya.
- **Favoritos (gramola)**: ♥ en una carátula o en el panel del disco lo
  marca como favorito (se guarda en `%APPDATA%\Trovador\favorites.json`
  y sigue a la carpeta si la app la renombra o la mueve). El botón
  **♥ Favoritos** de la cabecera cambia la cuadrícula por la gramola: una
  Wurlitzer 1015. El mueble es una imagen (`frontend/jukebox/cabinet.png`,
  generada a partir de `assets/wurlitzer.png` con `go run ./tools/cabinet
  assets/wurlitzer.png frontend/jukebox/cabinet.png 157`, que mide el cristal
  y el panel, deja transparentes el fondo y el cristal, pasa las luces al tono
  del acento de la interfaz —157°, el verde menta— y ahúma la madera hacia
  los grises de la ventana).
  Por el cristal se ve el carro con las carátulas de los favoritos (carrusel
  3D: flechas, ← →, rueda del ratón); en el panel de papel, la tira del disco
  elegido con su número; debajo, los pulsadores 1–0 (teclear el número pone
  ese disco, como en la máquina; también desde el teclado del PC) y el botón
  ▶. Ponerlo (▶, la carátula central, Enter o el número) saca el vinilo de
  detrás de la funda y lo baja al mecanismo, y el disco suena delante de lo
  que hubiera en la lista. Bajo el mueble, lo que suena con su piloto.
- **Pletina**: dentro de Favoritos, el selector de arriba (Gramola /
  Pletina / Gramófono / Carrusel) cambia de visualización, y se recuerda.
  La pletina enseña los mismos favoritos en una estantería, cada uno como su
  caja de pie con la cinta delante. La caja y la cinta son fotos
  (`frontend/deck/case.png` y `tape.png`, de `assets/case.jpg` y
  `assets/tape.png` con `go run ./tools/cabinet case …` y `… tape …`): la
  herramienta quita el damero de fondo, abre en transparente la cartulina y
  la etiqueta (la carátula va detrás y las llena: la portada ES la cartulina)
  y recorta un buje (`hub.png`) para hacerlo girar; imprime las coordenadas
  para el CSS.
  Las carátulas de cinta que son la cartulina escaneada (la portada con el
  lomo pegado, a veces con la contraportada, y muchas veces metida en un
  cuadrado) se recortan solas mirando el contenido, no la proporción: un
  doblez es una línea vertical (una raya o un salto de color en casi todas
  las filas de una columna); un lomo, una tira de 5-42 % del alto pegada a
  un borde, de fondo liso y con texto en vertical (tinta que se alterna
  muchas veces a lo largo de la tira y no cruza el doblez); la portada es lo
  que sigue al lomo hasta el doblez siguiente que deje un panel de anchura
  normal. Una línea parecida a la misma distancia del otro borde es un
  marco, no un lomo. En las apaisadas, un lomo entre dos paneles anchos
  (contraportada · lomo · portada): la portada es el panel más movido. Sin
  lomo no se toca nada, salvo las bandas negras o blancas de un escaneo
  metido en un cuadrado (a los dos lados de un eje, parecidas, de un 10 % o
  más). Probado contra las 356 miniaturas de la colección.
  Y para lo que no acierte, **Ajustar portada** (junto a *Poner la cinta*):
  se arrastra y se acerca la carátula dentro de la cartulina y el encuadre
  se guarda por disco (`%APPDATA%\Trovador\crops.json`; sigue a la
  carpeta si se mueve) y manda sobre el automático, en la caja y en la
  etiqueta de la cinta; *Automático* lo quita.
  Debajo, la pletina (`frontend/deck/deck.png`,
  de `assets/deck.png` con `go run ./tools/cabinet deck …`, hueco
  semitransparente y baño del acento). *Poner la cinta* (o Enter, o la caja
  del centro) la mete en el hueco, cierra la puerta y giran las bobinas; el
  disco suena delante de lo que hubiera en la lista. Los botones de la foto
  funcionan (anterior, play/pausa, stop, siguiente, sacar), el contador
  cuenta y los **vúmetros siguen el audio de verdad** (Web Audio, un canal
  cada uno).
- **Gramófono**: la tercera visualización de Favoritos. Los discos son
  fundas de vinilo (foto `frontend/gramo/sleeve.png`, de `assets/sleeve.png`
  con `go run ./tools/cabinet sleeve …`: el cartón blanco abierto, la
  carátula detrás y el disco asomando) en una estantería; debajo, un
  gramófono de bocina de los años 20 visto en picado
  (`frontend/gramo/gramophone.png`, de `assets/gramophone.png` con `go run
  ./tools/cabinet gramo …`). La herramienta quita el damero de fondo (mide
  los dos grises en las esquinas, cruza los bordes comprimidos y las sombras
  sin colarse por el objeto, y rellena los huecos cerrados como el de bajo
  la bocina) y mide la elipse del plato por el fieltro verde (el brazo
  descansa fuera del plato, así que el disco no lo tapa). El disco
  (`frontend/gramo/record.png`, de `assets/record.png` con `… record …`,
  etiqueta blanca abierta para la carátula) es un cuadrado abatido con
  `rotateX` para encajar en la elipse, y dentro gira. *Poner el disco* (o
  Enter, o la funda del centro) lo saca de la funda, lo deja caer en el
  plato y suena delante de lo que hubiera en la lista; el plato enseña
  siempre lo que suena. El baño del acento a lo bruto no le sienta (deja el
  latón y la caoba manchados de verde): en su lugar, un etalonaje a la luz
  de la interfaz (sombras hacia el gris azulado del fondo, brillos hacia el
  menta del acento, cada uno a la luminancia del píxel, y algo menos de
  saturación; `gramo … sin` lo evita). Las fotos sobre fondo liso (negro, o
  blanco para el vinilo) se recortan exactas; sobre damero pintado, con
  rebabas.
- **Carrusel**: la cuarta visualización, la sencilla: los favoritos como
  carátulas en un carrusel y, debajo, en grande, la carátula del disco que
  está sonando (o la del elegido, apagada, si no suena nada), con el título
  y un latido suave que sigue el nivel del audio (el mismo analizador que
  los vúmetros de la pletina). Enter, ▶ o la carátula del centro lo
  reproduce. (Los trovadores animados, la idea de la miniatura medieval,
  quedan para cuando haya con qué generar el vídeo.)
- **Convertir de formato**: la ventana (Chromium) no reproduce WMA, APE,
  WavPack, AIFF ni ALAC; esas pistas salen marcadas en el panel del disco y
  en la lista, con un botón *convertir* (a MP3 320, MP3 V0, FLAC o M4A 256,
  según el selector *convertir a* de la cabecera). Cada pista tiene ⇄ para
  convertirla por gusto, y el disco entero se convierte de una vez. ffmpeg
  recodifica conservando etiquetas y carátula, el fichero nuevo queda junto
  al original con la extensión nueva y el original se aparta a
  `_original`; la cola pasa a la ruta nueva y, si era la que sonaba, se
  reintenta sola.

Las tarjetas de disco (artista, álbum y una miniatura de 200 px de la
carátula) se guardan en `%APPDATA%Trovadorrds.json` y `thumbs`:
la primera vez la cuadrícula se rellena disco a disco (ffmpeg/ffprobe por
carpeta); desde la segunda sale al instante. Si cambia algo en la carpeta
de un disco, su tarjeta se recalcula. La Biblioteca usa la misma caché.

## Soulseek

La tercera pestaña es un cliente Soulseek. El protocolo lo lleva
[slskd](https://github.com/slskd/slskd) (demonio Soulseek de código abierto,
AGPL), que la app descarga a `bin` la primera vez (60 MB, botón *Instalar
motor*), arranca de fondo sin interfaz propia escuchando solo en
`127.0.0.1:5030` con una clave de API aleatoria por sesión, y para al cerrar
la app. Sus datos (base de transferencias, caché de compartidos, log) van a
`%APPDATA%Trovadorslskd`.

En *ajustes* de la pestaña: usuario y contraseña de Soulseek (si el nombre
está libre, la red crea la cuenta al entrar), carpeta compartida (por
defecto la madre de la carpeta de descargas, es decir, la biblioteca; se
comparte en solo lectura) y puerto de escucha (50300; ábrelo en el router
para que los demás puedan descargarte, o muchos te bloquearán). Se guardan
en `config.json` en claro.

Buscar devuelve usuarios (hueco libre, cola, velocidad) con sus ficheros
agrupados por carpeta remota (= disco), con filtros (solo carpetas, solo
FLAC, mp3 ≥ 320k). *Descargar carpeta* pide el disco entero; la flecha, un
fichero. Las descargas caen en la carpeta de descargas de la app, una
subcarpeta por disco, y aparecen en Biblioteca para curarlas. La lista de
descargas se refresca cada 2 s mientras la pestaña está abierta.

## Desarrollo

Requisitos: Go y la CLI de Wails (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`).
No hace falta Node: la interfaz es HTML/JS plano sin paso de build.

```
wails build                     # -> build/bin/Trovador.exe
wails dev                       # ventana con recarga en caliente al editar
go vet ./...                    # análisis estático
go test ./...                   # tests unitarios (rápidos, sin red)
```

Test de integración (descarga de verdad, necesita las herramientas):

```
MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run TestDownloadReal
MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run "TestLibrary|TestBatch"
SLSK_USER=... SLSK_PASS=... MUSIC_PICKER_BIN=build/bin/bin go test -tags integration -v -run TestSoulseek
```

Con `wails dev` el binario corre desde una carpeta temporal, así que
apunta a las herramientas con `MUSIC_PICKER_BIN=build/bin/bin`.

## Estructura

```
main.go             arranque de Wails: ventana + embed de frontend/
app.go              App: los métodos que la ventana puede llamar (Submit, Jobs, Cancel...)
jobs.go             almacén de trabajos en memoria (mutex) que avisa a la ventana por eventos
downloader.go       worker pool con goroutines; construye y ejecuta yt-dlp; troceado por capítulos
tools.go            descarga/actualización de yt-dlp, ffmpeg(+ffprobe) y deno a bin/
musicbrainz.go      cliente de MusicBrainz (1 req/s, reintentos, búsqueda aproximada) y Cover Art Archive
sources.go          Deezer, Bandcamp y Discogs; búsqueda en paralelo en todas las fuentes
fuzzy.go            parecido de nombres (Levenshtein + palabras) y emparejamiento pista↔fichero
library.go          Biblioteca: escaneo con ffprobe, comparación, carátula, corte y etiquetado con ffmpeg
batch.go            Biblioteca en lote: detección de discos, búsqueda de candidatas, aplicación
review.go           revisión: confirmar propuestas, elegir candidata o URL de MusicBrainz, buscar fuera
bytracks.go         identificar un disco por sus canciones (grabaciones → ediciones)
origins.go          orígenes: de qué disco viene cada fichero de una carpeta mezclada
album.go            crear álbum (carpeta + ficheros curados), completar pistas desde YouTube, renombrar carpeta
dupes.go            duplicados: agrupar copias del mismo disco, comparar integridad, fusionar
rename.go           renombrar carpetas en lote: proponer el nombre de cada carpeta (según cuelgue de la del artista o no) y aplicarlo tras confirmar
organize.go         ordenar por artista: proponer Colección\Artista\Disco y mover, creando la carpeta del artista
artists.go          iconos de las carpetas de artista: logo (TheAudioDB, Metal Archives) o foto, o mosaico de carátulas
icons.go            dibujo de los iconos (CD, vinilo, cinta, funda) con la carátula y codificación .ico
folder_icons.go     icono de carpeta del Explorador: desktop.ini, atributos, tipo automático o fijado
icons_windows.go    atributos de fichero y aviso al Explorador (solo Windows; icons_other.go, el resto)
soulseek.go         Soulseek: instalar/arrancar/parar slskd y su API (buscar, descargar, transferencias)
complete_slsk.go    completar un álbum desde Soulseek: localizar pistas que faltan y descargarlas curadas
upgrade_slsk.go     mejorar calidad desde Soulseek: detectar versiones mejores y sustituir
config.go           ajustes persistentes (JSON en %APPDATA%)
exec_windows.go     oculta la consola de los subprocesos (solo compila en Windows)
player.go           reproductor: servir el audio por HTTP con rangos, discos de la colección, caché de tarjetas y miniaturas
convert.go          convertir pistas de formato con ffmpeg (etiquetas y carátula intactas; original a _original)
favorites.go        favoritos del reproductor (favorites.json), siguen a la carpeta si se mueve
crops.go            encuadre manual de la carátula en la cartulina de la pletina (crops.json), sigue a la carpeta
carry.go            llevarse discos a otra carpeta (USB del coche): nombres FAT32, solo audio y carátulas, a MP3 si se pide
tools/cabinet       prepara las fotos: gramola (cristal y panel), pletina (hueco), caja y cinta, gramófono (plato), disco y funda
frontend/index.html la interfaz (pestañas Descargas y Biblioteca)
frontend/player.js  pestaña Reproductor, cola y barra de reproducción
frontend/downloads.js, library.js, soulseek.js  lógica de cada pestaña; hablan con Go vía window.go.main.App
fuzzy_test.go       tests unitarios del parecido y el emparejamiento (go test ./...)
downloader_test.go, library_test.go  tests de integración (etiqueta `integration`)
wails.json          configuración de Wails
build/              icono y manifiesto (generados por Wails); build/bin/ es la salida
```
