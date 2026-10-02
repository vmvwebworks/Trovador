package main

// En Linux: yt-dlp en su versión autónoma (no necesita Python), deno en
// zip y ffmpeg el del sistema (sudo apt install ffmpeg), que se enlaza en
// bin/. Los nombres llevan .exe como en Windows; toolPath se lo quita.
var toolDefs = []toolDef{
	{"yt-dlp", "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux", []string{"yt-dlp.exe"}, false},
	{"ffmpeg", "", []string{"ffmpeg.exe", "ffprobe.exe"}, false},
	{"deno", "https://github.com/denoland/deno/releases/latest/download/deno-x86_64-unknown-linux-gnu.zip", []string{"deno.exe"}, false},
	// slskd: motor Soulseek, 60 MB; solo si se usa la pestaña Soulseek.
	{"slskd", "https://github.com/slskd/slskd/releases/download/0.26.0/slskd-0.26.0-linux-x64.zip", []string{"slskd.exe"}, true},
}
