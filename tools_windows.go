package main

var toolDefs = []toolDef{
	{"yt-dlp", "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe", []string{"yt-dlp.exe"}, false},
	{"ffmpeg", "https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip", []string{"ffmpeg.exe", "ffprobe.exe"}, false},
	{"deno", "https://github.com/denoland/deno/releases/latest/download/deno-x86_64-pc-windows-msvc.zip", []string{"deno.exe"}, false},
	// slskd: motor Soulseek, 60 MB; solo si se usa la pestaña Soulseek.
	{"slskd", "https://github.com/slskd/slskd/releases/download/0.26.0/slskd-0.26.0-win-x64.zip", []string{"slskd.exe"}, true},
}
