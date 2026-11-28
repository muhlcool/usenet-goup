package archiver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNativeSplitAndGuards(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	_ = os.WriteFile(src, make([]byte, 2*1024*1024+5), 0o600)

	if _, _, _, err := CreateArchive(context.Background(), PackOptions{SourcePath: src, OutputDir: filepath.Join(dir, "o0"), Engine: EngineNative, PartSizeMB: 0}, func(string) {}, nil); err == nil {
		t.Fatal("PartSizeMB=0 muss abgelehnt werden (früher Endlosschleife)")
	}
	files, _, _, err := CreateArchive(context.Background(), PackOptions{SourcePath: src, OutputDir: filepath.Join(dir, "o1"), Engine: EngineNative, PartSizeMB: 1}, func(string) {}, nil)
	if err != nil || len(files) != 3 {
		t.Fatalf("got %d files, err %v", len(files), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := CreateArchive(ctx, PackOptions{SourcePath: src, OutputDir: filepath.Join(dir, "o2"), Engine: EngineNative, PartSizeMB: 1}, func(string) {}, nil); err == nil {
		t.Fatal("Abbruch nicht erkannt")
	}
}

func TestRandomStringBounds(t *testing.T) {
	for in, want := range map[int]int{-5: 16, 0: 16, 3: 16, 4: 4, 24: 24, 128: 128, 129: 128, 1 << 30: 128} {
		if got := len(RandomString(in)); got != want {
			t.Errorf("RandomString(%d): Länge %d, erwartet %d", in, got, want)
		}
	}
	if RandomString(24) == RandomString(24) {
		t.Fatal("Zufallswerte wiederholen sich")
	}
}

// Ein Packer, der "Erfolg" meldet, aber nichts schreibt, darf nicht als Erfolg durchgehen.
func TestPackerWithoutOutputFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Shell-Skript als Ersatz-Packer nur unter Unix")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "7z"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	_ = os.WriteFile(src, make([]byte, 1024), 0o600)
	_, _, _, err := CreateArchive(context.Background(), PackOptions{SourcePath: src, OutputDir: filepath.Join(dir, "out"), Engine: Engine7Zip, PartSizeMB: 1}, func(string) {}, nil)
	if err == nil {
		t.Fatal("leeres Ergebnis des Packers muss ein Fehler sein")
	}
}
