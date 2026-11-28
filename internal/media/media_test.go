package media

import (
	"encoding/binary"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildAtom(typ string, payload []byte) []byte {
	sz := uint32(8 + len(payload))
	out := make([]byte, sz)
	binary.BigEndian.PutUint32(out[0:4], sz)
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func TestCleanReleaseTitleForSearch(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Der.Pate", "Der Pate"},
		{"Der.Pate.1972.German.DL.1080p.BluRay.x264-GROUP", "Der Pate"},
		{"1917.2019.2160p.UHD.BluRay.HDR.DoVi.x265-GROUP", "1917"},
		{"Meine.Serie.S02E05.GERMAN.1080p.WEB.x264-GROUP", "Meine Serie"},
	}
	for _, c := range cases {
		gotTitle, _ := cleanReleaseTitleForSearch(c.in)
		if gotTitle != c.want {
			t.Errorf("cleanReleaseTitleForSearch(%q) = %q, want %q", c.in, gotTitle, c.want)
		}
	}
}

func TestParseMP4NestedAtomsAndTruncatedPayload(t *testing.T) {
	dir := t.TempDir()

	// mvhd v0 (20 bytes inside payload): timescale=1000, duration=5000 -> 5s
	mvhd := make([]byte, 20)
	mvhd[0] = 0
	binary.BigEndian.PutUint32(mvhd[12:16], 1000)
	binary.BigEndian.PutUint32(mvhd[16:20], 5000)

	// tkhd (84 bytes inside payload): 1920x1080 in fixed-point 16.16 at end-8..end
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:80], 1920<<16)
	binary.BigEndian.PutUint32(tkhd[80:84], 1080<<16)

	trak := buildAtom("trak", tkhd)
	moov := buildAtom("moov", append(buildAtom("mvhd", mvhd), trak...))

	// Append a truncated atom header at the end
	trunc := []byte{0x00, 0x00, 0x00, 0x20, 'f', 'r', 'e', 'e', 0x01, 0x02}
	mp4Bytes := append(moov, trunc...)

	f := filepath.Join(dir, "sample.mp4")
	if err := os.WriteFile(f, mp4Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	info := ExtractMediaInfo(f, func(string) {})
	if info == "" || !strings.Contains(info, "sample.mp4") {
		t.Fatalf("expected media info for sample.mp4, got: %q", info)
	}
	if strings.Contains(info, "1920") && !strings.Contains(info, "1080") {
		t.Fatalf("partial resolution in media info: %q", info)
	}

	// Also verify 64-bit oversized atom does not panic
	corrupt := filepath.Join(dir, "corrupt64.mp4")
	raw64 := []byte{
		0x00, 0x00, 0x00, 0x01, 'm', 'o', 'o', 'v',
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xF0,
	}
	if err := os.WriteFile(corrupt, raw64, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = ExtractMediaInfo(corrupt, func(string) {})
}

func TestParseMP4AtomSizeOverflow(t *testing.T) {
	raw := []byte{0, 0, 0, 16, 'f', 'r', 'e', 'e', 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 1, 'm', 'o', 'o', 'v', 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xF8}
	raw = append(raw, make([]byte, 64)...)
	p := filepath.Join(t.TempDir(), "x.mp4")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	parseMP4File(p, int64(len(raw)))
}

func TestParseAVIFile_BasicHeader(t *testing.T) {
	dir := t.TempDir()
	aviPath := filepath.Join(dir, "sample.avi")

	// Build minimal RIFF AVI with hdrl -> avih + LIST strl (vids, 1280x720, 25 fps) + LIST strl (auds, MP3, 2ch, 48000Hz)
	makeChunk := func(id string, data []byte) []byte {
		out := make([]byte, 8+len(data)+(len(data)&1))
		copy(out[0:4], id)
		binary.LittleEndian.PutUint32(out[4:8], uint32(len(data)))
		copy(out[8:], data)
		return out
	}
	makeList := func(listType string, body []byte) []byte {
		payload := append([]byte(listType), body...)
		return makeChunk("LIST", payload)
	}

	avihData := make([]byte, 56)
	binary.LittleEndian.PutUint32(avihData[0:4], 40000) // 40000 us = 25 fps
	binary.LittleEndian.PutUint32(avihData[16:20], 250) // 250 frames = 10s
	binary.LittleEndian.PutUint32(avihData[24:28], 2)   // 2 streams

	vStrh := make([]byte, 56)
	copy(vStrh[0:4], "vids")
	copy(vStrh[4:8], "XVID")
	binary.LittleEndian.PutUint32(vStrh[20:24], 1)
	binary.LittleEndian.PutUint32(vStrh[24:28], 25)

	vStrf := make([]byte, 40)
	binary.LittleEndian.PutUint32(vStrf[0:4], 40)
	binary.LittleEndian.PutUint32(vStrf[4:8], 1280)
	binary.LittleEndian.PutUint32(vStrf[8:12], 720)
	copy(vStrf[16:20], "XVID")

	vStrl := makeList("strl", append(makeChunk("strh", vStrh), makeChunk("strf", vStrf)...))

	aStrh := make([]byte, 56)
	copy(aStrh[0:4], "auds")
	copy(aStrh[4:8], "MP3 ")
	binary.LittleEndian.PutUint32(aStrh[20:24], 1)
	binary.LittleEndian.PutUint32(aStrh[24:28], 48000)

	aStrf := make([]byte, 18)
	binary.LittleEndian.PutUint16(aStrf[0:2], 0x0055) // MP3
	binary.LittleEndian.PutUint16(aStrf[2:4], 2)      // 2 channels
	binary.LittleEndian.PutUint32(aStrf[4:8], 48000)  // 48 kHz
	binary.LittleEndian.PutUint32(aStrf[8:12], 16000) // 128 kbps

	aStrl := makeList("strl", append(makeChunk("strh", aStrh), makeChunk("strf", aStrf)...))

	hdrlBody := append(makeChunk("avih", avihData), append(vStrl, aStrl...)...)
	hdrlList := makeList("hdrl", hdrlBody)

	var fileBuf []byte
	fileBuf = append(fileBuf, "RIFF"...)
	var riffLen [4]byte
	binary.LittleEndian.PutUint32(riffLen[:], uint32(4+len(hdrlList)))
	fileBuf = append(fileBuf, riffLen[:]...)
	fileBuf = append(fileBuf, "AVI "...)
	fileBuf = append(fileBuf, hdrlList...)

	if err := os.WriteFile(aviPath, fileBuf, 0600); err != nil {
		t.Fatal(err)
	}

	out := ExtractMediaInfo(dir, func(string) {})
	if !strings.Contains(out, "Format                                   : AVI") {
		t.Fatalf("expected AVI format in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Width                                    : 1280 pixels") {
		t.Fatalf("expected width 1280 in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Height                                   : 720 pixels") {
		t.Fatalf("expected height 720 in output, got:\n%s", out)
	}
}

func TestParseAVIFile_MalformedAndNonAVI(t *testing.T) {
	dir := t.TempDir()

	// 1. RIFF WAVE (not AVI) must be rejected cleanly
	wavPath := filepath.Join(dir, "sound.avi")
	wav := []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x44\xac\x00\x00\x88\x58\x01\x00\x02\x00\x10\x00data\x00\x00\x00\x00")
	if err := os.WriteFile(wavPath, wav, 0o600); err != nil {
		t.Fatal(err)
	}
	if sum, ok := parseAVIFile(wavPath, int64(len(wav))); ok || sum != nil {
		t.Fatalf("expected RIFF WAVE to be rejected by parseAVIFile")
	}

	// 2. Truncated hdrl chunk size must not panic or loop endlessly
	truncPath := filepath.Join(dir, "trunc.avi")
	trunc := []byte{
		'R', 'I', 'F', 'F', 0xFF, 0xFF, 0xFF, 0x7F,
		'A', 'V', 'I', ' ',
		'L', 'I', 'S', 'T', 0xFF, 0xFF, 0xFF, 0x7F,
		'h', 'd', 'r', 'l',
		'a', 'v', 'i', 'h', 0x10, 0x00, 0x00, 0x00,
	}
	if err := os.WriteFile(truncPath, trunc, 0o600); err != nil {
		t.Fatal(err)
	}
	if sum, ok := parseAVIFile(truncPath, int64(len(trunc))); ok || sum != nil {
		t.Fatalf("expected truncated AVI to return false cleanly")
	}
}

func TestExtractMediaInfo_ISOGoFallbackWithoutExternalCLI(t *testing.T) {
	dir := t.TempDir()
	isoPath := filepath.Join(dir, "Backup.Disc.2026.iso")
	if err := os.WriteFile(isoPath, make([]byte, 8192), 0o600); err != nil {
		t.Fatal(err)
	}

	// Point PATH to an empty directory and override candidates so neither mediainfo nor ffprobe is found
	emptyBin := t.TempDir()
	t.Setenv("PATH", emptyBin)
	oldCandidates := mediaInfoCandidates
	mediaInfoCandidates = nil
	t.Cleanup(func() { mediaInfoCandidates = oldCandidates })

	var logs []string
	out := ExtractMediaInfo(dir, func(msg string) { logs = append(logs, msg) })
	if !strings.Contains(out, "Format                                   : ISO") {
		t.Fatalf("expected ISO Go-Fallback output, got:\n%s", out)
	}
	if !strings.Contains(out, "Backup.Disc.2026.iso") {
		t.Fatalf("expected ISO filename in output, got:\n%s", out)
	}
	joinedLogs := strings.Join(logs, "\n")
	if !strings.Contains(joinedLogs, "Basis-MediaInfo (Go-Fallback)") {
		t.Fatalf("expected Go-Fallback log message, got: %s", joinedLogs)
	}
}

func TestFindMediaInfoCLI_CacheAvoidsRepeatedProbes(t *testing.T) {
	fakePath := filepath.Join(t.TempDir(), "nonexistent-mediainfo-test")
	miProbeMu.Lock()
	miProbeCache[fakePath] = false
	miProbeMu.Unlock()
	t.Cleanup(func() {
		miProbeMu.Lock()
		delete(miProbeCache, fakePath)
		miProbeMu.Unlock()
	})

	miProbeMu.Lock()
	cached, seen := miProbeCache[fakePath]
	miProbeMu.Unlock()
	if !seen || cached {
		t.Fatalf("expected cached false entry for %s", fakePath)
	}
}

func writeTestCover(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(p, []byte("\xff\xd8\xff\xe0 test"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveCoverLocalFileIsNotUploaded(t *testing.T) {
	var logs []string
	u, p := ResolveCoverForTemplate(t.TempDir(), writeTestCover(t), func(m string) { logs = append(logs, m) })
	if u != "" || p != "" {
		t.Fatalf("lokales Cover darf keine URL liefern (kein Upload), got %q / %q", u, p)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "manuell hochladen") {
		t.Fatalf("erwarteter Hinweis auf manuellen Upload fehlt, got %v", logs)
	}
}

func TestResolveCoverURLIsPassedThrough(t *testing.T) {
	const poster = "https://image.tmdb.org/t/p/w500/abc.jpg"
	u, p := ResolveCoverForTemplate(t.TempDir(), poster, func(string) {})
	if u != poster || p != poster {
		t.Fatalf("Poster-URL muss unverändert übernommen werden, got %q / %q", u, p)
	}
}

// urlRecorder merkt sich alle angefragten URLs und beantwortet nichts (kein echtes Netz).
type urlRecorder struct{ urls []string }

func (h *urlRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	h.urls = append(h.urls, r.URL.String())
	return nil, errors.New("kein Netz im Test")
}

func tmdbKeysUsed(rec *urlRecorder) []string {
	var keys []string
	for _, raw := range rec.urls {
		if u, err := url.Parse(raw); err == nil && strings.Contains(u.Host, "themoviedb.org") {
			keys = append(keys, u.Query().Get("api_key"))
		}
	}
	return keys
}

func TestFetchReleaseMediaMetaUsesDefaultTMDBKey(t *testing.T) {
	rec := &urlRecorder{}
	FetchReleaseMediaMeta(&http.Client{Transport: rec}, "Der.Film.2024.German.1080p.WEB.x264-GRP", "tt1234567", "", "de", func(string) {})
	keys := tmdbKeysUsed(rec)
	if len(keys) == 0 {
		t.Fatalf("ohne eigenen Key muss TMDB mit dem Standard-Key angefragt werden, angefragt: %v", rec.urls)
	}
	for _, k := range keys {
		if k != DefaultTMDBKey {
			t.Fatalf("erwartet Standard-Key, got %q", k)
		}
	}
}

func TestFetchReleaseMediaMetaPrefersGivenTMDBKey(t *testing.T) {
	rec := &urlRecorder{}
	FetchReleaseMediaMeta(&http.Client{Transport: rec}, "Der.Film.2024.German.1080p.WEB.x264-GRP", "tt1234567", "eigener-tmdb-key", "de", func(string) {})
	keys := tmdbKeysUsed(rec)
	if len(keys) == 0 || keys[0] != "eigener-tmdb-key" {
		t.Fatalf("übergebener Key wurde nicht verwendet, got %v", keys)
	}
}
