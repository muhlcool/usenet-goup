package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomTemplatesPerCategory(t *testing.T) {
	dir := t.TempDir()
	tplDir := ensureCustomTemplatesDir(dir, nil)
	if tplDir == "" {
		t.Fatal("expected templates dir")
	}
	for _, name := range []string{"movies_series.txt", "games.txt", "misc.txt"} {
		if _, err := os.Stat(filepath.Join(tplDir, name)); err != nil {
			t.Fatalf("expected starter template %s: %v", name, err)
		}
	}

	rendered, ok := loadAndRenderCustomTemplate(dir, "games", templateVars{
		Title:     "Test.Game.LINUX-GRP",
		Header:    "hdr1",
		Groups:    "alt.binaries.games",
		Password:  "pw123",
		Timestamp: 1732082640,
		NFO:       "Line A",
		CoverURL:  "https://example.invalid/g.jpg",
	}, nil)
	if !ok {
		t.Fatal("expected custom games template to load")
	}
	if !strings.Contains(rendered, `[SIZE="3"][COLOR=Tomato][B]Test.Game.LINUX-GRP[/B][/COLOR][/SIZE]`) {
		t.Fatalf("unexpected rendered games template:\n%s", rendered)
	}
	if !strings.Contains(rendered, "[IMG]https://example.invalid/g.jpg[/IMG]") {
		t.Fatalf("missing cover tag:\n%s", rendered)
	}
	if !strings.Contains(rendered, "[HIDE][EZPOST]Test.Game.LINUX-GRP|hdr1|alt.binaries.games|pw123|{1732082640}[/EZPOST][/HIDE]") {
		t.Fatalf("missing EZPOST line:\n%s", rendered)
	}
	if !strings.Contains(rendered, `[SIZE="2"][COLOR=DeepSkyBlue]NFO:[/COLOR][/SIZE]`+"\n[NFO]Line A[/NFO]") {
		t.Fatalf("missing NFO block:\n%s", rendered)
	}

	// Empty optional values should drop empty tags & headings cleanly
	renderedEmpty, ok := loadAndRenderCustomTemplate(dir, "misc", templateVars{
		Title:     "Book.Release.EPUB-GRP",
		Header:    "hdr2",
		Groups:    "alt.binaries.misc",
		Password:  "",
		Timestamp: 1732082640,
		NFO:       "",
		CoverURL:  "",
	}, nil)
	if !ok {
		t.Fatal("expected misc template to render")
	}
	if strings.Contains(renderedEmpty, "[IMG][/IMG]") || strings.Contains(renderedEmpty, "[NFO][/NFO]") || strings.Contains(renderedEmpty, "NFO:") {
		t.Fatalf("empty tags/headings were not cleaned up:\n%s", renderedEmpty)
	}
}

func TestCustomTemplateDoesNotExpandPlaceholdersInsideValues(t *testing.T) {
	out := renderCustomTemplate("{{TITLE}}\n{{EZPOST}}\n[NFO]{{NFO}}[/NFO]\n", templateVars{
		Title:     "Movie {{PASSWORD}}",
		Header:    "hdr1",
		Groups:    "alt.binaries.movies",
		Password:  "TopSecret123",
		Timestamp: 1732082640,
		NFO:       "Gruss {{PASSWORD}} und {{HEADER}}",
	})
	if !strings.Contains(out, "Gruss {{PASSWORD}} und {{HEADER}}") {
		t.Fatalf("placeholder inside NFO was unexpectedly expanded:\n%s", out)
	}
	if strings.Count(out, "TopSecret123") != 1 {
		t.Fatalf("expected password only once in EZPOST, got:\n%s", out)
	}
}
