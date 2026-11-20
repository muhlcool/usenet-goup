package media

import (
	"encoding/binary"
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
