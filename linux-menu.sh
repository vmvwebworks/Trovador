#!/usr/bin/env bash
# Añade Trovador al menú de aplicaciones (lo llama build-linux.sh, y va
# también en el tar.gz de las releases). Se ejecuta desde la carpeta en la
# que está el binario Trovador, o se le pasa esa carpeta.
set -euo pipefail
dir="$(cd "${1:-$(dirname "$0")}" && pwd)"
icon="$dir/appicon.png"
[ -f "$icon" ] || icon="$dir/../appicon.png" # build/bin/Trovador -> build/appicon.png
mkdir -p "$HOME/.local/share/applications"
cat > "$HOME/.local/share/applications/trovador.desktop" <<DESK
[Desktop Entry]
Type=Application
Name=Trovador
Comment=Descarga música de YouTube y Bandcamp
Exec="$dir/Trovador"
Icon=$(cd "$(dirname "$icon")" && pwd)/appicon.png
Terminal=false
Categories=AudioVideo;Audio;
DESK
echo "Listo: Trovador está en el menú de aplicaciones ($dir/Trovador)"
