package nntp

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNZBAndSorting(t *testing.T) {
	dir := t.TempDir()
	nzbPath := filepath.Join(dir, "test.nzb")

	files := []NZBFile{
		{
			Poster:  "p2 <p2@example.invalid>",
			Date:    1700000000,
			Subject: `Release [02/02] - "b.rar" yEnc (1/1)`,
			Groups:  []string{"alt.binaries.test"},
			Segments: []NZBSegment{
				{Bytes: 200, Number: 2, MsgID: "seg2@example.invalid"},
				{Bytes: 100, Number: 1, MsgID: "seg1@example.invalid"},
			},
		},
		{
			Poster:  "p1 <p1@example.invalid>",
			Date:    1700000000,
			Subject: `Release [01/02] - "a.rar" yEnc (1/1)`,
			Groups:  []string{"alt.binaries.test"},
			Segments: []NZBSegment{
				{Bytes: 300, Number: 1, MsgID: "seg0@example.invalid"},
			},
		},
	}

	if err := WriteNZB(nzbPath, "Release & <Title>\x00", "s3cr3t", files); err != nil {
		t.Fatalf("WriteNZB failed: %v", err)
	}

	raw, err := os.ReadFile(nzbPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	var parsed NZB
	if err := xml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("generated NZB is invalid XML: %v\n%s", err, string(raw))
	}
	if len(parsed.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(parsed.Files))
	}
	if !strings.Contains(parsed.Files[0].Subject, `"a.rar"`) {
		t.Fatalf("files not sorted by subject: %+v", parsed.Files)
	}
	if parsed.Files[1].Segments[0].Number != 1 || parsed.Files[1].Segments[1].Number != 2 {
		t.Fatalf("segments not sorted by number: %+v", parsed.Files[1].Segments)
	}
}

func TestDecodeNFOBytesCP437AndUTF8(t *testing.T) {
	cp437Raw := []byte{0xC9, 0xCD, 0xBB, '\r', '\n', 'F', 0x84, 'r', 'b', 'e', '\r', '\n'}
	gotCP := DecodeNFOBytes(cp437Raw)
	if !strings.Contains(gotCP, "╔═╗") || !strings.Contains(gotCP, "Färbe") {
		t.Fatalf("unexpected CP437 decode: %q", gotCP)
	}

	utf8BOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte("Line1\r\nGröße\r\n")...)
	gotUTF := DecodeNFOBytes(utf8BOM)
	if gotUTF != "Line1\nGröße" {
		t.Fatalf("unexpected UTF-8 decode: %q", gotUTF)
	}
}

func TestWritePostingTemplateAndSubject(t *testing.T) {
	dir := t.TempDir()
	nzbPath := filepath.Join(dir, "Sample.Release.2026.nzb")

	_, tpl, err := WritePostingTemplate(
		nzbPath,
		"Sample.Release.2026",
		"obf-header",
		[]string{"alt.binaries.misc", "alt.binaries.test"},
		"pass123",
		1732082640,
		"NFO Line",
		"Video: HEVC 1080p",
		"https://pixhost.to/show/123/cover.jpg",
		"tt1234567",
		"Kurze Beschreibung.",
		"de",
	)
	if err != nil {
		t.Fatalf("WritePostingTemplate failed: %v", err)
	}

	for _, want := range []string{
		`[SIZE="3"][COLOR=Tomato][B]Sample.Release.2026[/B][/COLOR][/SIZE]`,
		"[IMG]https://pixhost.to/show/123/cover.jpg[/IMG]",
		"Kurze Beschreibung.",
		"[imdb]tt1234567[/imdb]",
		"[HIDE][EZPOST]Sample.Release.2026|obf-header|alt.binaries.misc,alt.binaries.test|pass123|{1732082640}[/EZPOST][/HIDE]",
		`[SIZE="2"][COLOR=DeepSkyBlue]NFO:[/COLOR][/SIZE]` + "\n[NFO]NFO Line[/NFO]",
		`[SIZE="2"][COLOR=DeepSkyBlue]MediaInfo:[/COLOR][/SIZE]` + "\n[NFO]Video: HEVC 1080p[/NFO]",
	} {
		if !strings.Contains(tpl, want) {
			t.Fatalf("missing expected snippet %q in template:\n%s", want, tpl)
		}
	}

	subj := FormatSubject("BaseSubj", 1, 3, "file.part01.rar", 1, 10)
	if subj != `BaseSubj [01/03] - "file.part01.rar" yEnc (1/10)` {
		t.Fatalf("unexpected subject: %q", subj)
	}
}

func TestWritePostingTemplateGameMode(t *testing.T) {
	dir := t.TempDir()
	nzbPath := filepath.Join(dir, "RuneScape.Dragonwilds.PS5-LIGHTFORCE.nzb")

	_, tpl, err := WritePostingTemplateWithType(
		nzbPath,
		"RuneScape Dragonwilds PS5-LIGHTFORCE",
		"obf-hdr",
		[]string{"alt.binaries.games.ps5"},
		"",
		1732082640,
		"GAME NFO TEXT",
		"Video: 1080p",
		"https://pixhost.to/show/999/cover.jpg",
		"tt9999999",
		"Sollte bei Game nicht erscheinen",
		"de",
		"games",
	)
	if err != nil {
		t.Fatalf("WritePostingTemplateWithType failed: %v", err)
	}
	if !strings.Contains(tpl, `[SIZE="3"][COLOR=Tomato][B]RuneScape Dragonwilds PS5-LIGHTFORCE[/B][/COLOR][/SIZE]`) {
		t.Fatalf("missing title in template:\n%s", tpl)
	}
	if !strings.Contains(tpl, "[IMG]https://pixhost.to/show/999/cover.jpg[/IMG]") {
		t.Fatalf("missing cover in template:\n%s", tpl)
	}
	if !strings.Contains(tpl, `[SIZE="2"][COLOR=DeepSkyBlue]NFO:[/COLOR][/SIZE]`+"\n[NFO]GAME NFO TEXT[/NFO]") {
		t.Fatalf("missing NFO block in template:\n%s", tpl)
	}
	if strings.Contains(tpl, "[imdb]") || strings.Contains(tpl, "Sollte bei Game nicht erscheinen") || strings.Contains(tpl, "MediaInfo:") {
		t.Fatalf("unexpected movie/mediainfo fields in game template:\n%s", tpl)
	}

	_, tplNoCover, err := WritePostingTemplateWithType(
		nzbPath,
		"Game.Without.Cover.LINUX-GRP",
		"obf-hdr",
		[]string{"alt.binaries.linux"},
		"pw",
		1732082640,
		"",
		"",
		"",
		"",
		"",
		"en",
		"games",
	)
	if err != nil {
		t.Fatalf("WritePostingTemplateWithType (no cover) failed: %v", err)
	}
	if strings.Contains(tplNoCover, "[IMG]") || strings.Contains(tplNoCover, "[NFO]") {
		t.Fatalf("unexpected [IMG] or [NFO] when empty:\n%s", tplNoCover)
	}
}

func TestWritePostingTemplateMiscMode(t *testing.T) {
	dir := t.TempDir()
	nzbPath := filepath.Join(dir, "Max.Mustermann.Das.Buch.2026.GERMAN.EPUB.eBook-GRP.nzb")

	_, tpl, err := WritePostingTemplateWithType(
		nzbPath,
		"Max.Mustermann.Das.Buch.2026.GERMAN.EPUB.eBook-GRP",
		"obf-hdr",
		[]string{"alt.binaries.e-book.german"},
		"secret",
		1732082640,
		"EBOOK NFO",
		"General: should be ignored",
		"",
		"tt0000001",
		"Ignored movie synopsis",
		"de",
		"misc",
	)
	if err != nil {
		t.Fatalf("WritePostingTemplateWithType (misc) failed: %v", err)
	}
	if !strings.Contains(tpl, `[SIZE="3"][COLOR=Tomato][B]Max.Mustermann.Das.Buch.2026.GERMAN.EPUB.eBook-GRP[/B][/COLOR][/SIZE]`) {
		t.Fatalf("missing title in misc template:\n%s", tpl)
	}
	if !strings.Contains(tpl, `[SIZE="2"][COLOR=DeepSkyBlue]NFO:[/COLOR][/SIZE]`+"\n[NFO]EBOOK NFO[/NFO]") {
		t.Fatalf("missing NFO in misc template:\n%s", tpl)
	}
	if strings.Contains(tpl, "[IMG]") || strings.Contains(tpl, "[imdb]") || strings.Contains(tpl, "Ignored movie synopsis") || strings.Contains(tpl, "MediaInfo:") {
		t.Fatalf("unexpected media/imdb/img fields in misc template:\n%s", tpl)
	}
}
