package sysproc

import (
	"os"
	"testing"
)

func TestFreeDiskBytes(t *testing.T) {
	if free := FreeDiskBytes(os.TempDir()); free <= 0 {
		t.Fatalf("freier Platz nicht ermittelbar: %d", free)
	}
	if FreeDiskBytes("/gibt/es/nicht/xyz") != -1 {
		t.Fatal("ungültiger Pfad muss -1 liefern")
	}
}
