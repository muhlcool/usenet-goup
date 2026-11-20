# Usenet GoUp (v1.35)

High-Speed Cross-Platform Usenet Uploader in Go (Windows, Linux, macOS).

<img width="2025" height="2646" alt="Usenet-GoUp" src="https://github.com/user-attachments/assets/9ac8f822-7986-4c72-94f5-bab7d6b5a7d4" />

## Features (v1.35)
- **Templates for Media, Games & Misc:** Dedicated posting templates for `movies_series` (with cover, MediaInfo, metadata) as well as simple templates for `games` (PC / Console) and `misc` (E-Book, Software, Audio).
- **Streaming Upload (< 60 MB RAM):** On-demand segmentation via `ReadAt`, no RAM spike on 50+ GB releases.
- **Live Telemetry:** Real-time progress for archiving (RAR5 / 7-Zip / Native Go splitting), PAR2 (ParPar / par2), and NNTP upload.
- **MediaInfo Detection:** Automatically finds the largest media file (`.mkv`, `.mp4`, `.avi`, `.m2ts`, etc.) and reads MediaInfo (or `ffprobe` / file metadata as fallback) directly for the `[MEDIAINFO]` block.
- **Cover & Image Upload in Pure Go (without curl.exe):** Detects local covers (`cover.jpg`, `poster.jpg`, `folder.jpg`, or a manually selected image), accepts direct image URLs, or optionally uploads local covers via Go HTTP (`multipart/form-data`) directly to **PixHost** or **ImgBB**.
- **Special Character, Unicode & Long-Path Protection:** Windows Extended-Length (`\\?\`), XML control character sanitization, and CP437/UTF-8 NFO decoding.
- **Crash & Freeze Log (`crash-freeze.log`):** Watchdog, panic stack traces, and browser stall detection.

## Project Structure (from v1.32)
| File / Package | Content |
| --- | --- |
| `main.go` | Startup, port check, open browser |
| `app.go` | Server state, routes, configuration, small API handlers |
| `pipeline.go` | Upload job: archive/PAR2 (or cache) -> upload -> NZB -> metadata/template |
| `cache.go` | Package cache (dry run and failed uploads) |
| `security.go` | Local API protection, JSON helpers |
| `jobs.go` | Job lock and cancellation |
| `logbroker.go` | Live log and progress via Server-Sent Events |
| `datadir.go` | Location of config.json/secret.key, disk space requirement |
| `build-all.ps1` | Cross-build script for all target platforms |
| `internal/config` | Saved settings and shared job parameters |
| `internal/archiver`, `internal/par2` | RAR/7z/native splitting, PAR2 |
| `internal/nntp`, `internal/yenc` | Upload, NZB, posting template, yEnc |
| `internal/media` | MediaInfo, TMDB/TVMaze, cover hosting |
| `internal/sysproc` | Platform code: paths, dialogs, clipboard, disk space, external tools |
| `internal/crypto` | Encryption of secrets |

Tests: `go test -race ./...`

## Build (Windows - Windowless Including App Icon)
```powershell
go build -trimpath -ldflags="-s -w -H=windowsgui -X main.version=v1.35" -o usenet-goup.exe .
```

## Build (Linux / macOS)
```bash
go build -trimpath -ldflags="-s -w -X main.version=v1.35" -o usenet-goup .
```

## Build All Targets
```powershell
.\build-all.ps1
```

## Dependencies & Licenses
- **Own Source Code:** Released under the **MIT License** (see file `LICENSE`). No warranty; use at your own risk.
- **Go Standard Library:** This project comes without external Go third-party modules (`go.mod` contains only the Go standard library, BSD-3-Clause license).
- **Optional External CLI Tools (Not Included in Source Code):** If installed on the target system, external programs such as `rar` / `7z` (archiving), `parpar` / `par2` (PAR2 creation), `mediainfo` / `ffprobe` (media data), as well as on Linux `zenity` / `kdialog` / `wl-copy` / `xclip` may be called. Their own license terms apply to each of these programs.

## Transparency & Network Connections
- No telemetry, tracking, or update servers are contacted.
- External network connections occur exclusively for:
  1. the configured NNTP server (upload),
  2. cover upload (PixHost / ImgBB) when a cover is present/selected, as well as optional metadata lookups (TMDB, TVMaze, DuckDuckGo) primarily for `movies_series`.
- Local settings and keys (`config.json`, `secret.key`) as well as logs (`crash-freeze.log`) are created locally at runtime in the application or user directory and are not part of this source archive.
