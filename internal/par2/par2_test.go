package par2

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParparSliceSize(t *testing.T) {
	cases := map[int64]string{
		0:         "768K",
		10 << 30:  "768K",
		24 << 30:  "1536K",
		100 << 30: "3840K",
	}
	for in, want := range cases {
		if got := parparSliceSize(in); got != want {
			t.Errorf("%d: got %s want %s", in, got, want)
		}
	}
}

// Eine Engine, die "Erfolg" meldet, aber nichts erzeugt, darf nicht still durchgehen.
func TestCreatePar2FailsWhenNothingCreated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Shell-Attrappe nur unter Unix")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "parpar"), []byte("#!/bin/sh\necho 100%\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	work := t.TempDir()
	f := filepath.Join(work, "a.bin")
	_ = os.WriteFile(f, []byte("daten"), 0o600)
	files, err := CreatePar2(context.Background(), work, "a", EngineParPar, 10, []string{f}, func(string) {}, nil)
	if err == nil {
		t.Fatalf("erwartet Fehler, got %v", files)
	}
}
