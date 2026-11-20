package sysproc

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeOutFileNameCases(t *testing.T) {
	if got := SafeOutFileName("  bad<>name?.mkv... ", ".nzb"); got != "bad__name_.mkv.nzb" {
		t.Fatalf("unexpected sanitized name: %q", got)
	}
	if got := SafeOutFileName("...", ".nzb"); got != "release.nzb" {
		t.Fatalf("expected fallback release.nzb, got %q", got)
	}
	if got := SafeOutFileName("con.mkv", ".nzb"); strings.HasPrefix(strings.ToUpper(got), "CON.") {
		t.Fatalf("reserved Windows name not prefixed: %q", got)
	}
	longTitle := strings.Repeat("Äöüäöß漢字", 60)
	gotLong := SafeOutFileName(longTitle, ".nzb")
	if len(gotLong) > maxNameBytes || !utf8.ValidString(gotLong) {
		t.Fatalf("byte limit/UTF-8 failure: len=%d valid=%v %q", len(gotLong), utf8.ValidString(gotLong), gotLong)
	}
}
