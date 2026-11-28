package media

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ebml baut ein EBML-Element: id (inkl. Längen-Markerbits) + 8-Byte-Größe + Inhalt.
func ebml(id uint32, payload ...[]byte) []byte {
	var out []byte
	switch {
	case id > 0xFFFFFF:
		out = append(out, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFFFF:
		out = append(out, byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFF:
		out = append(out, byte(id>>8), byte(id))
	default:
		out = append(out, byte(id))
	}
	var body []byte
	for _, p := range payload {
		body = append(body, p...)
	}
	size := make([]byte, 8)
	binary.BigEndian.PutUint64(size, uint64(len(body)))
	size[0] = 0x01 // 8-Byte-VInt
	return append(append(out, size...), body...)
}

func ebmlUint(id uint32, v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return ebml(id, b)
}

func ebmlFloat(id uint32, v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	return ebml(id, b)
}

// sampleMKV: 2 h Film, 1920x1080 H.264 mit 25 fps, deutsche AAC-Tonspur 2.0, Untertitel.
func sampleMKV() []byte {
	header := ebml(0x1A45DFA3, ebml(0x4282, []byte("matroska")))
	info := ebml(0x1549A966,
		ebmlUint(0x2AD7B1, 1000000),
		ebmlFloat(0x4489, 7200000),
		ebml(0x5741, []byte("GoUp-Test")),
	)
	video := ebml(0xAE,
		ebmlUint(0xD7, 1), ebmlUint(0x83, 1),
		ebml(0x86, []byte("V_MPEG4/ISO/AVC")),
		ebmlUint(0x23E383, 40000000),
		ebml(0xE0, ebmlUint(0xB0, 1920), ebmlUint(0xBA, 1080)),
	)
	audio := ebml(0xAE,
		ebmlUint(0xD7, 2), ebmlUint(0x83, 2),
		ebml(0x86, []byte("A_AAC")),
		ebml(0x22B59C, []byte("ger")),
		ebml(0xE1, ebmlFloat(0xB5, 48000), ebmlUint(0x9F, 2)),
	)
	sub := ebml(0xAE,
		ebmlUint(0xD7, 3), ebmlUint(0x83, 17),
		ebml(0x86, []byte("S_TEXT/UTF8")),
		ebml(0x22B59C, []byte("eng")),
	)
	return append(header, ebml(0x18538067, info, ebml(0x1654AE6B, video, audio, sub))...)
}

func writeTemp(t testing.TB, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseMatroskaSample(t *testing.T) {
	data := sampleMKV()
	p := writeTemp(t, "film.mkv", data)
	sum, ok := parseMatroskaFile(p, int64(len(data)))
	if !ok {
		t.Fatal("gültiges MKV nicht erkannt")
	}
	if len(sum.tracks) != 3 {
		t.Fatalf("erwartet 3 Spuren, got %d", len(sum.tracks))
	}
	v, a, s := sum.tracks[0], sum.tracks[1], sum.tracks[2]
	if v.kind != "Video" || v.width != 1920 || v.height != 1080 || math.Abs(v.fps-25) > 0.01 {
		t.Fatalf("Videospur falsch: %+v", v)
	}
	if a.kind != "Audio" || a.channels != 2 || a.sampleRate != 48000 || a.language != "ger" {
		t.Fatalf("Tonspur falsch: %+v", a)
	}
	if s.kind != "Text" || s.language != "eng" {
		t.Fatalf("Untertitel falsch: %+v", s)
	}
	if math.Abs(sum.durationMs-7200000) > 1 {
		t.Fatalf("Dauer falsch: %v ms", sum.durationMs)
	}
	txt := parseNativeMediaSummary(p, int64(len(data)))
	for _, want := range []string{"Matroska", "1920 pixels", "AVC", "AAC"} {
		if !strings.Contains(txt, want) {
			t.Errorf("MediaInfo-Text enthält %q nicht:\n%s", want, txt)
		}
	}
}

// Tief verschachtelte Segmente dürfen keinen Stack-Überlauf (Absturz) auslösen.
func TestParseMatroskaDeepNestingNoCrash(t *testing.T) {
	data := ebml(0x1A45DFA3, ebml(0x4282, []byte("matroska")))
	seg := []byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF} // Segment, Größe "unbekannt"
	for i := 0; i < 1_000_000; i++ {
		data = append(data, seg...)
	}
	p := writeTemp(t, "tief.mkv", data)
	parseWithTimeout(t, p, int64(len(data)))
}

func parseWithTimeout(t testing.TB, p string, size int64) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = parseNativeMediaSummary(p, size)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Parser hängt bei %s", filepath.Base(p))
	}
}

// Fuzzing: zufällig veränderte Dateien dürfen weder abstürzen noch hängen.
// Normaler "go test" prüft nur die Startdateien; gezielt: go test -fuzz=FuzzParseNative ./internal/media
func FuzzParseNative(f *testing.F) {
	f.Add(sampleMKV(), uint8(0))
	f.Add(minimalMP4(), uint8(1))
	f.Add(minimalAVI(), uint8(2))
	f.Add([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x80}, uint8(0))
	exts := []string{".mkv", ".mp4", ".avi"}
	f.Fuzz(func(t *testing.T, data []byte, kind uint8) {
		p := writeTemp(t, "x"+exts[int(kind)%len(exts)], data)
		parseWithTimeout(t, p, int64(len(data)))
	})
}

// minimalMP4: ftyp + moov/mvhd + trak/tkhd/mdia/hdlr(vide).
func minimalMP4() []byte {
	atom := func(typ string, body ...[]byte) []byte {
		var b []byte
		for _, x := range body {
			b = append(b, x...)
		}
		h := make([]byte, 8)
		binary.BigEndian.PutUint32(h, uint32(8+len(b)))
		copy(h[4:], typ)
		return append(h, b...)
	}
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:], 1000)
	binary.BigEndian.PutUint32(mvhd[16:], 60000)
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:], 1280<<16)
	binary.BigEndian.PutUint32(tkhd[80:], 720<<16)
	hdlr := make([]byte, 24)
	copy(hdlr[8:], "vide")
	return append(atom("ftyp", []byte("isom\x00\x00\x02\x00isom")),
		atom("moov", atom("mvhd", mvhd), atom("trak", atom("tkhd", tkhd), atom("mdia", atom("hdlr", hdlr))))...)
}

// minimalAVI: RIFF/AVI mit hdrl/avih und einem Video-strl.
func minimalAVI() []byte {
	chunk := func(id string, body []byte) []byte {
		h := make([]byte, 8)
		copy(h, id)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(body)))
		b := append(h, body...)
		if len(body)%2 == 1 {
			b = append(b, 0)
		}
		return b
	}
	list := func(typ string, body ...[]byte) []byte {
		b := []byte(typ)
		for _, x := range body {
			b = append(b, x...)
		}
		return chunk("LIST", b)
	}
	avih := make([]byte, 56)
	binary.LittleEndian.PutUint32(avih[0:], 40000)
	binary.LittleEndian.PutUint32(avih[16:], 1000)
	binary.LittleEndian.PutUint32(avih[32:], 640)
	binary.LittleEndian.PutUint32(avih[36:], 480)
	strh := make([]byte, 56)
	copy(strh, "vids")
	copy(strh[4:], "XVID")
	body := append([]byte("AVI "), list("hdrl", chunk("avih", avih), list("strl", chunk("strh", strh)))...)
	return chunk("RIFF", body)
}
