package archiver

import (
	"context"
	"os"
	"path/filepath"
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
