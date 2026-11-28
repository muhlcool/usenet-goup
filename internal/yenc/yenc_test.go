package yenc

import (
	"bytes"
	"crypto/rand"
	"hash/crc32"
	"strings"
	"testing"
)

// Decode ist ein minimaler Referenz-Decoder (inkl. NNTP-Dot-Unstuffing) für Tests.
func Decode(t *testing.T, enc []byte) []byte {
	t.Helper()
	var out []byte
	lines := strings.Split(string(enc), "\r\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "=ybegin") || strings.HasPrefix(l, "=ypart") || strings.HasPrefix(l, "=yend") || l == "" {
			continue
		}
		if strings.HasPrefix(l, "..") {
			l = l[1:]
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if c == '=' {
				i++
				c = l[i] - 64
			}
			out = append(out, c-42)
		}
	}
	return out
}

func TestRoundTripAndLineRules(t *testing.T) {
	data := make([]byte, 768*1024+333)
	_, _ = rand.Read(data)
	// gezielt kritische Werte: Leerzeichen/Tab/Punkt (kodiert) an Zeilenrändern
	for i := 0; i < 4096; i++ {
		data[i] = byte(i) - 42
	}
	enc := AppendSegment(nil, data, "a\r\nb.rar", 1<<30, 3, 9, 1000)
	if got := Decode(t, enc); !bytes.Equal(got, data) {
		t.Fatalf("Roundtrip fehlgeschlagen")
	}
	for i, l := range strings.Split(string(enc), "\r\n") {
		if strings.ContainsAny(l, "\r\n\x00") {
			t.Fatalf("Zeile %d enthält CR/LF/NUL", i)
		}
		if strings.HasPrefix(l, "=y") || l == "" {
			if strings.HasPrefix(l, "=ybegin") && strings.Contains(l, "\r") {
				t.Fatal("Name nicht bereinigt")
			}
			continue
		}
		if l[0] == '.' || l[0] == ' ' || l[0] == '\t' {
			t.Fatalf("Zeile %d beginnt mit %q", i, l[0])
		}
		if e := l[len(l)-1]; e == ' ' || e == '\t' {
			t.Fatalf("Zeile %d endet mit Whitespace", i)
		}
		if len(l) > lineLength+1 {
			t.Fatalf("Zeile %d zu lang: %d", i, len(l))
		}
	}
	want := crc32.ChecksumIEEE(data)
	if !strings.Contains(string(enc), "pcrc32="+strings.ToLower(hex8(want))) {
		t.Fatal("pcrc32 fehlt oder falsch")
	}
}

func hex8(v uint32) string { return string(appendHex8(nil, v)) }

func TestSmallCRCPadding(t *testing.T) {
	if hex8(0xab) != "000000ab" {
		t.Fatal(hex8(0xab))
	}
}

func BenchmarkAppendSegment(b *testing.B) {
	d := make([]byte, 768*1024)
	_, _ = rand.Read(d)
	var buf []byte
	b.SetBytes(int64(len(d)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf = AppendSegment(buf, d, "x.rar", 1<<30, 2, 10, 786432)
	}
}

func TestSinglePartAndEmptyPayload(t *testing.T) {
	encEmpty := AppendSegment(nil, nil, "empty.bin", 0, 1, 1, 0)
	if !strings.Contains(string(encEmpty), "=ybegin line=128 size=0 name=empty.bin") {
		t.Fatalf("unexpected single-part header: %s", encEmpty)
	}
	if strings.Contains(string(encEmpty), "=ypart") {
		t.Fatalf("single-part should not contain =ypart: %s", encEmpty)
	}
	if !strings.Contains(string(encEmpty), "=yend size=0 crc32=00000000") {
		t.Fatalf("unexpected single-part footer: %s", encEmpty)
	}
	if got := Decode(t, encEmpty); len(got) != 0 {
		t.Fatalf("expected 0 decoded bytes, got %d", len(got))
	}

	// Verify buffer reuse across segments does not leak stale bytes
	buf := make([]byte, 0, 64)
	p1 := []byte("first segment payload")
	p2 := []byte("short")
	buf = AppendSegment(buf, p1, "part.dat", int64(len(p1)+len(p2)), 1, 2, 0)
	buf = AppendSegment(buf, p2, "part.dat", int64(len(p1)+len(p2)), 2, 2, int64(len(p1)))
	if got := Decode(t, buf); !bytes.Equal(got, p2) {
		t.Fatalf("buffer reuse mismatch: got %q want %q", got, p2)
	}
}

func FuzzAppendSegment(f *testing.F) {
	f.Add([]byte("abc"), "test.bin", int64(3), 1, 1, int64(0))
	f.Add([]byte{0, 10, 13, 61, 46, 32, 9, 255}, "edge.bin", int64(8), 2, 3, int64(8))
	f.Fuzz(func(t *testing.T, data []byte, name string, total int64, part int, totalParts int, offset int64) {
		if total < int64(len(data)) {
			total = int64(len(data))
		}
		if totalParts < 1 {
			totalParts = 1
		}
		if part < 1 || part > totalParts {
			part = 1
		}
		if offset < 0 {
			offset = 0
		}
		enc := AppendSegment(nil, data, name, total, part, totalParts, offset)
		if len(enc) == 0 {
			t.Fatal("empty yEnc output")
		}
	})
}
