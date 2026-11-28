package media

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"usenet-goup/internal/sysproc"
)

var mediaExts = map[string]bool{
	".mkv":  true,
	".mp4":  true,
	".avi":  true,
	".m2ts": true,
	".ts":   true,
	".vob":  true,
	".mov":  true,
	".wmv":  true,
	".iso":  true,
	".mp3":  true,
	".flac": true,
	".ogg":  true,
}

var knownFileExts = map[string]bool{
	".rar": true, ".zip": true, ".7z": true, ".nfo": true, ".txt": true, ".nzb": true,
	".m4v": true, ".webm": true, ".mpg": true, ".mpeg": true, ".srt": true,
}

var imageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

var (
	imdbRegex     = regexp.MustCompile(`tt\d{6,9}`)
	braceTagRegex = regexp.MustCompile(`\{[^}]+\}`)
	releaseTagRgx = regexp.MustCompile(`(?i)[._ -](s\d{1,3}(e\d{1,3})?|season[._ -]?\d{1,3}|episode[._ -]?\d{1,3}|complete|german|dl|english|multi|1080p|2160p|720p|480p|web|webrip|webdl|bluray|bdrip|remux|x264|x265|hevc|h264|h265|hdr|dv|dovi|aac|ac3|eac3|dts|truehd|atmos)([._ -].*)?$`)
	htmlTagRegex  = regexp.MustCompile(`<[^>]+>`)
)

func FindLargestMediaFile(sourcePath string) (string, int64) {
	normSrc := sysproc.NormalizePath(sourcePath)
	info, err := os.Stat(normSrc)
	if err != nil {
		return "", 0
	}
	if !info.IsDir() {
		if mediaExts[strings.ToLower(filepath.Ext(normSrc))] {
			return normSrc, info.Size()
		}
		return "", 0
	}

	var bestPath string
	var bestSize int64
	_ = filepath.WalkDir(normSrc, func(p string, d os.DirEntry, wErr error) error {
		if wErr != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if mediaExts[ext] {
			if fi, fErr := d.Info(); fErr == nil && fi.Size() > bestSize {
				bestSize = fi.Size()
				bestPath = p
			}
		}
		return nil
	})
	return bestPath, bestSize
}

func ExtractMediaInfo(sourcePath string, logFn func(string)) string {
	mediaPath, mediaSize := FindLargestMediaFile(sourcePath)
	if mediaPath == "" {
		return ""
	}
	fileNameOnly := filepath.Base(sysproc.DisplayPath(mediaPath))

	// 1. Reiner Go-Container-Parser (MKV/EBML & MP4/MOV) ohne jedes externe Programm
	if nativeInfo := parseNativeMediaSummary(mediaPath, mediaSize); nativeInfo != "" {
		logFn(fmt.Sprintf("MediaInfo nativ in Go ausgelesen: %s", fileNameOnly))
		return nativeInfo
	}

	if miBin := findMediaInfoCLI(); miBin != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		cmd := exec.CommandContext(ctx, miBin, mediaPath)
		sysproc.HideWindow(cmd)
		cmd.WaitDelay = 1500 * time.Millisecond
		out, err := cmd.Output()
		cancel()
		if err == nil && len(out) > 0 {
			lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
			var cleaned []string
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "Unique ID") {
					continue
				}
				if strings.HasPrefix(trimmed, "Complete name") {
					if idx := strings.Index(line, ": "); idx != -1 {
						line = line[:idx+2] + fileNameOnly
					}
				}
				cleaned = append(cleaned, line)
			}
			res := strings.TrimSpace(strings.Join(cleaned, "\n"))
			if res != "" {
				logFn(fmt.Sprintf("MediaInfo über CLI ausgelesen: %s", fileNameOnly))
				return res
			}
		}
	}

	if ffBin := sysproc.FindTool([]string{"ffprobe"}, []string{
		`C:\Program Files\ffmpeg\bin\ffprobe.exe`,
		`/usr/bin/ffprobe`,
		`/usr/local/bin/ffprobe`,
		`/opt/homebrew/bin/ffprobe`,
	}); ffBin != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		cmd := exec.CommandContext(ctx, ffBin, "-hide_banner", mediaPath)
		sysproc.HideWindow(cmd)
		cmd.WaitDelay = 1500 * time.Millisecond
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil && len(out) > 0 {
			raw := strings.ReplaceAll(string(out), "\r\n", "\n")
			var filtered []string
			for _, line := range strings.Split(raw, "\n") {
				t := strings.TrimSpace(line)
				if strings.HasPrefix(t, "Input #0") {
					filtered = append(filtered, fmt.Sprintf("General: %s (%.2f MB)", fileNameOnly, float64(mediaSize)/(1024*1024)))
				} else if strings.HasPrefix(t, "Duration:") || strings.HasPrefix(t, "Stream #") || strings.HasPrefix(t, "title") || strings.HasPrefix(t, "language") {
					filtered = append(filtered, line)
				}
			}
			if len(filtered) > 0 {
				logFn(fmt.Sprintf("Media-Streams über ffprobe ausgelesen: %s", fileNameOnly))
				return strings.TrimSpace(strings.Join(filtered, "\n"))
			}
		}
	}

	extUpper := strings.ToUpper(strings.TrimPrefix(filepath.Ext(fileNameOnly), "."))
	sizeMB := float64(mediaSize) / (1024 * 1024)
	sizeGB := float64(mediaSize) / (1024 * 1024 * 1024)
	sizeStr := fmt.Sprintf("%.2f MB", sizeMB)
	if sizeGB >= 1.0 {
		sizeStr = fmt.Sprintf("%.2f GB (%.2f MB)", sizeGB, sizeMB)
	}
	logFn(fmt.Sprintf("Basis-MediaInfo (Go-Fallback) erstellt für: %s", fileNameOnly))
	return fmt.Sprintf("General\nComplete name                            : %s\nFormat                                   : %s\nFile size                                : %s", fileNameOnly, extUpper, sizeStr)
}

func cleanReleaseTitleForSearch(releaseName string) (string, string) {
	// Entfernt ggf. {imdb-tt...} / {tmdb-...} Tags, Staffel-/Folgenmarkierungen (S01, S01E01, E01, Season 1) und Release-Tags
	// Nur echte Dateiendungen abschneiden: bei "Der.Pate" ist ".Pate" Teil des Titels.
	base := releaseName
	if ext := strings.ToLower(filepath.Ext(releaseName)); mediaExts[ext] || imageExts[ext] || knownFileExts[ext] {
		base = strings.TrimSuffix(releaseName, filepath.Ext(releaseName))
	}
	base = braceTagRegex.ReplaceAllString(base, " ")
	base = releaseTagRgx.ReplaceAllString(base, "")
	tokens := strings.FieldsFunc(base, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == ' '
	})
	var titleParts []string
	var year string
	for _, tok := range tokens {
		t := strings.TrimSpace(tok)
		if t == "" {
			continue
		}
		if len(t) == 4 && isAllDigits(t) && t >= "1920" && t <= "2099" && len(titleParts) > 0 {
			year = t
			break
		}
		titleParts = append(titleParts, t)
	}
	return strings.TrimSpace(strings.Join(titleParts, " ")), year
}

func doGetWithUA(client *http.Client, targetURL string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	return body, resp.StatusCode, err
}

// ddgLookupIMDbID ist die letzte Quelle für eine IMDb-ID (wenn NFO, Dateiname, TMDB und TVMaze nichts liefern).
// DuckDuckGo-HTML ist kein offizielles API und kann jederzeit blockieren; Fehler werden still ignoriert.
func ddgLookupIMDbID(client *http.Client, cleanTitle string, year string, logFn func(string)) string {
	if cleanTitle == "" {
		return ""
	}
	q := cleanTitle + " imdb"
	if year != "" {
		q = cleanTitle + " " + year + " imdb"
	}
	body, code, err := doGetWithUA(client, "https://html.duckduckgo.com/html/?q="+url.QueryEscape(q))
	if err != nil || code >= 400 {
		return ""
	}
	if id := imdbRegex.FindString(string(body)); id != "" {
		logFn(fmt.Sprintf("IMDb-ID %s für '%s' über Websuche ermittelt.", id, cleanTitle))
		return id
	}
	return ""
}

func FindLocalCover(sourcePath string, explicitCover string) string {
	trimmed := strings.TrimSpace(explicitCover)
	if trimmed != "" {
		if strings.HasPrefix(strings.ToLower(trimmed), "http://") || strings.HasPrefix(strings.ToLower(trimmed), "https://") {
			return trimmed
		}
		norm := sysproc.NormalizePath(trimmed)
		if info, err := os.Stat(norm); err == nil && !info.IsDir() {
			return norm
		}
	}

	normSrc := sysproc.NormalizePath(sourcePath)
	info, err := os.Stat(normSrc)
	if err != nil {
		return ""
	}
	searchDir := normSrc
	if !info.IsDir() {
		searchDir = filepath.Dir(normSrc)
	}

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return ""
	}

	preferredNames := []string{"cover", "poster", "folder", "front", "thumb"}
	var firstImage string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !imageExts[ext] {
			continue
		}
		full := filepath.Join(searchDir, e.Name())
		if firstImage == "" {
			firstImage = full
		}
		baseLow := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		for _, pref := range preferredNames {
			if baseLow == pref {
				return full
			}
		}
	}
	return firstImage
}

// ResolveCoverForTemplate liefert die Cover-URL für die Posting-Vorlage. Es wird nichts
// hochgeladen: Eine URL (manuell oder Poster von TMDB/TVMaze) wird direkt übernommen.
// Ein lokales Bild im Quellordner wird nur gemeldet, der Upload erfolgt manuell.
func ResolveCoverForTemplate(sourcePath string, explicitCover string, logFn func(string)) (templateURL string, previewURL string) {
	found := FindLocalCover(sourcePath, explicitCover)
	if found == "" {
		return "", ""
	}
	low := strings.ToLower(found)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return found, found
	}
	logFn(fmt.Sprintf("Lokales Cover erkannt (%s), kein automatischer Upload: bitte manuell hochladen und Link in die Vorlage einsetzen.", filepath.Base(sysproc.DisplayPath(found))))
	return "", ""
}

type MediaMeta struct {
	CoverURL   string
	PreviewURL string
	IMDbID     string
	Synopsis   string
}

// DefaultTMDBKey ist der öffentlich bekannte TMDB-Key aus Jellyfin.
const DefaultTMDBKey = "4219e299c89411838049ab0dab19ebd5"

// LocalIMDbID liest eine IMDb-ID nur aus NFO oder Release-Namen, ganz ohne Netzwerk.
func LocalIMDbID(releaseName, nfoContent string) string {
	if id := imdbRegex.FindString(nfoContent); id != "" {
		return id
	}
	return imdbRegex.FindString(releaseName)
}

func FetchReleaseMediaMeta(client *http.Client, releaseName string, nfoContent string, tmdbKey string, metaLang string, logFn func(string)) MediaMeta {
	meta := MediaMeta{IMDbID: LocalIMDbID(releaseName, nfoContent)}
	cleanTitle, year := cleanReleaseTitleForSearch(releaseName)

	// Leerer Key = eingebauter Standard-Key (öffentlich bekannter Jellyfin-Key).
	effectiveTMDBKey := strings.TrimSpace(tmdbKey)
	if effectiveTMDBKey == "" {
		effectiveTMDBKey = DefaultTMDBKey
	}

	tmdbLang := "de-DE"
	if strings.ToLower(strings.TrimSpace(metaLang)) == "en" {
		tmdbLang = "en-US"
	}

	// 1. PRIORITÄT 1: TMDB-Lookup per IMDb-ID (deutsche Beschreibung & Poster für Filme und Serien)
	if effectiveTMDBKey != "" && meta.IMDbID != "" {
		findURL := fmt.Sprintf("https://api.themoviedb.org/3/find/%s?api_key=%s&external_source=imdb_id&language=%s", url.PathEscape(meta.IMDbID), url.QueryEscape(effectiveTMDBKey), tmdbLang)
		if raw, code, err := doGetWithUA(client, findURL); err == nil && code < 400 {
			var findRes struct {
				MovieResults []struct {
					PosterPath string `json:"poster_path"`
					Overview   string `json:"overview"`
				} `json:"movie_results"`
				TvResults []struct {
					PosterPath string `json:"poster_path"`
					Overview   string `json:"overview"`
				} `json:"tv_results"`
			}
			if json.Unmarshal(raw, &findRes) == nil {
				if len(findRes.MovieResults) > 0 {
					if strings.TrimSpace(findRes.MovieResults[0].Overview) != "" {
						meta.Synopsis = strings.TrimSpace(findRes.MovieResults[0].Overview)
					}
					if findRes.MovieResults[0].PosterPath != "" {
						coverU := "https://image.tmdb.org/t/p/w500" + findRes.MovieResults[0].PosterPath
						meta.CoverURL = coverU
						meta.PreviewURL = coverU
						logFn(fmt.Sprintf("Cover & Metadaten (Priorität 1: TMDB IMDb %s) gefunden: %s", meta.IMDbID, coverU))
					}
				} else if len(findRes.TvResults) > 0 {
					if strings.TrimSpace(findRes.TvResults[0].Overview) != "" {
						meta.Synopsis = strings.TrimSpace(findRes.TvResults[0].Overview)
					}
					if findRes.TvResults[0].PosterPath != "" {
						coverU := "https://image.tmdb.org/t/p/w500" + findRes.TvResults[0].PosterPath
						meta.CoverURL = coverU
						meta.PreviewURL = coverU
						logFn(fmt.Sprintf("Cover & Metadaten (Priorität 1: TMDB IMDb %s) gefunden: %s", meta.IMDbID, coverU))
					}
				}
			}
		}
	}

	// 2. PRIORITÄT 1b: TMDB Multi-Search per bereinigtem Titel & Jahr (wenn noch kein Cover oder keine deutsche Beschreibung vorliegt)
	if effectiveTMDBKey != "" && cleanTitle != "" && (meta.CoverURL == "" || meta.Synopsis == "") {
		q := url.QueryEscape(cleanTitle)
		u := fmt.Sprintf("https://api.themoviedb.org/3/search/multi?api_key=%s&language=%s&query=%s", url.QueryEscape(effectiveTMDBKey), tmdbLang, q)
		if year != "" {
			u += "&year=" + url.QueryEscape(year)
		}
		if raw, code, err := doGetWithUA(client, u); err == nil && code < 400 {
			var res struct {
				Results []struct {
					ID         int    `json:"id"`
					MediaType  string `json:"media_type"`
					PosterPath string `json:"poster_path"`
					Overview   string `json:"overview"`
				} `json:"results"`
			}
			if json.Unmarshal(raw, &res) == nil && len(res.Results) > 0 {
				if meta.Synopsis == "" && strings.TrimSpace(res.Results[0].Overview) != "" {
					meta.Synopsis = strings.TrimSpace(res.Results[0].Overview)
				}
				if meta.CoverURL == "" && res.Results[0].PosterPath != "" {
					coverU := "https://image.tmdb.org/t/p/w500" + res.Results[0].PosterPath
					meta.CoverURL = coverU
					meta.PreviewURL = coverU
					logFn(fmt.Sprintf("Cover & Metadaten (Priorität 1: TMDB-Suche '%s') gefunden: %s", cleanTitle, coverU))
				}
				if meta.IMDbID == "" && res.Results[0].ID > 0 && (res.Results[0].MediaType == "movie" || res.Results[0].MediaType == "tv") {
					extURL := fmt.Sprintf("https://api.themoviedb.org/3/%s/%d/external_ids?api_key=%s", res.Results[0].MediaType, res.Results[0].ID, url.QueryEscape(effectiveTMDBKey))
					if extRaw, extCode, extErr := doGetWithUA(client, extURL); extErr == nil && extCode < 400 {
						var extRes struct {
							IMDbID string `json:"imdb_id"`
						}
						if json.Unmarshal(extRaw, &extRes) == nil && extRes.IMDbID != "" {
							meta.IMDbID = extRes.IMDbID
						}
					}
				}
			}
		}
	}

	// 3. Fallback: TVMaze Lookup nur falls TMDB nichts geliefert hat
	if cleanTitle != "" && (meta.CoverURL == "" || meta.Synopsis == "" || meta.IMDbID == "") {
		var tvURL string
		if meta.IMDbID != "" {
			tvURL = "https://api.tvmaze.com/lookup/shows?imdb=" + url.QueryEscape(meta.IMDbID)
		} else {
			tvURL = "https://api.tvmaze.com/singlesearch/shows?q=" + url.QueryEscape(cleanTitle)
		}
		if raw, code, err := doGetWithUA(client, tvURL); err == nil && code < 400 {
			var tvRes struct {
				Summary   string `json:"summary"`
				Externals *struct {
					IMDb string `json:"imdb"`
				} `json:"externals"`
				Image *struct {
					Original string `json:"original"`
					Medium   string `json:"medium"`
				} `json:"image"`
			}
			if json.Unmarshal(raw, &tvRes) == nil {
				if meta.IMDbID == "" && tvRes.Externals != nil && tvRes.Externals.IMDb != "" {
					meta.IMDbID = tvRes.Externals.IMDb
				}
				if meta.Synopsis == "" && tvRes.Summary != "" {
					meta.Synopsis = strings.TrimSpace(htmlTagRegex.ReplaceAllString(tvRes.Summary, ""))
				}
				if meta.CoverURL == "" && tvRes.Image != nil {
					u := strings.TrimSpace(tvRes.Image.Medium)
					if u == "" {
						u = strings.TrimSpace(tvRes.Image.Original)
					}
					if u != "" {
						meta.CoverURL = u
						meta.PreviewURL = u
						logFn(fmt.Sprintf("Cover (Fallback TVMaze) gefunden: %s", u))
					}
				}
			}
		}
	}

	// 4. Letzte Quelle für die IMDb-ID: Websuche.
	if meta.IMDbID == "" {
		meta.IMDbID = ddgLookupIMDbID(client, cleanTitle, year, logFn)
	}

	return meta
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// mediaInfoCandidates: Suchorte für MediaInfo. Die Program-Files-Pfade sind die Standard-
// installation der Fenster-Version (GUI); sie werden nur genutzt, wenn dort die CLI liegt.
var mediaInfoCandidates = []string{
	`C:\Program Files\MediaInfo\MediaInfo.exe`,
	`C:\Program Files (x86)\MediaInfo\MediaInfo.exe`,
	`/usr/bin/mediainfo`,
	`/usr/local/bin/mediainfo`,
	`/opt/homebrew/bin/mediainfo`,
}

// miProbeTimeout begrenzt die einmalige Prüfung, ob ein gefundenes MediaInfo die CLI ist.
var miProbeTimeout = 5 * time.Second

var (
	miProbeMu    sync.Mutex
	miProbeCache = map[string]bool{}
)

// findMediaInfoCLI liefert nur die Kommandozeilen-Version von MediaInfo.
// Unter Windows heißt auch die GUI-Version "MediaInfo.exe". Mit einer Datei als Argument
// öffnet sie ein (verstecktes) Fenster und beendet sich nie, GoUp hing dann bei AVI/ISO.
// Deshalb wird jede Fundstelle einmal mit --Version geprüft: nur die CLI antwortet
// mit "MediaInfo Command line". Das Ergebnis wird pro Pfad zwischengespeichert.
func findMediaInfoCLI() string {
	bin := sysproc.FindTool([]string{"mediainfo"}, mediaInfoCandidates)
	if bin == "" {
		return ""
	}
	miProbeMu.Lock()
	defer miProbeMu.Unlock()
	if ok, seen := miProbeCache[bin]; seen {
		if ok {
			return bin
		}
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), miProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--Version")
	sysproc.HideWindow(cmd)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	ok := err == nil && strings.Contains(strings.ToLower(string(out)), "command line")
	miProbeCache[bin] = ok
	if ok {
		return bin
	}
	return ""
}
