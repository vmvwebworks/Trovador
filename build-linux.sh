#!/usr/bin/env bash
# Compila Trovador en Linux (Ubuntu 24.04+) y lo añade al menú de
# aplicaciones. Requisitos, una vez:
#   sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev ffmpeg
#   Go y la CLI de Wails (go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0)
set -euo pipefail
cd "$(dirname "$0")"
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"

# Ubuntu 24.04 trae webkit2gtk-4.1; Wails usa la 4.0 salvo con esta etiqueta.
wails build -tags webkit2_41

./linux-menu.sh build/bin
