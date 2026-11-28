package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"usenet-goup/internal/archiver"
	"usenet-goup/internal/par2"
	"usenet-goup/internal/sysproc"
)

// maxDirectFiles begrenzt "Nur hochladen" (jede Datei bleibt während des Uploads offen).
var maxDirectFiles = 5000

// Systemdateien, die beim Direkt-Upload eines Ordners nie mit hochgeladen werden.
var directSkipNames = map[string]bool{
	"thumbs.db":   true,
	"desktop.ini": true,
	".ds_store":   true,
	"ehthumbs.db": true,
}

func skipDirectFile(name string) bool {
	low := strings.ToLower(name)
	return directSkipNames[low] || strings.HasPrefix(name, "._")
}

// collectDirectFiles liefert die Dateien für "Nur hochladen" (unverändert, 1:1):
// eine Einzeldatei oder alle Dateien direkt im Ordner. Unterordner mit Inhalt werden
// abgelehnt (die NZB kennt keine Ordner, Namen könnten kollidieren). Verknüpfungen
// (Symlinks) werden nicht verfolgt, damit nichts außerhalb des gewählten Ordners hochgeht.
func collectDirectFiles(normSrc string) (inputDir string, files []string, skipped []string, err error) {
	info, err := os.Lstat(normSrc)
	if err != nil {
		return "", nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, nil, errors.New("Der Quellpfad ist eine Verknüpfung (Symlink). Bitte das eigentliche Ziel auswählen.")
	}
	if info.Mode().IsRegular() {
		if info.Size() == 0 {
			return "", nil, nil, errors.New("Die Datei ist leer.")
		}
		return filepath.Dir(normSrc), []string{normSrc}, nil, nil
	}
	if !info.IsDir() {
		return "", nil, nil, errors.New("Der Quellpfad ist weder Datei noch Ordner.")
	}
	entries, err := os.ReadDir(normSrc)
	if err != nil {
		return "", nil, nil, err
	}
	var subdirs []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.Type()&os.ModeSymlink != 0:
			skipped = append(skipped, name+" (Verknüpfung)")
		case e.IsDir():
			if sysproc.CalcPathSize(filepath.Join(normSrc, name)) > 0 {
				subdirs = append(subdirs, name)
			}
		case !e.Type().IsRegular():
			skipped = append(skipped, name)
		case skipDirectFile(name):
			skipped = append(skipped, name)
		default:
			fi, iErr := e.Info()
			if iErr != nil || fi.Size() == 0 {
				skipped = append(skipped, name+" (leer)")
				continue
			}
			files = append(files, filepath.Join(normSrc, name))
		}
	}
	if len(subdirs) > 0 {
		sort.Strings(subdirs)
		return "", nil, nil, fmt.Errorf("Der Ordner enthält Unterordner (%s). \"Nur hochladen\" lädt nur Dateien direkt im Ordner hoch, weil die NZB keine Ordner kennt. Bitte Unterordner einzeln hochladen oder \"Packen und hochladen\" verwenden.", strings.Join(subdirs, ", "))
	}
	if len(files) == 0 {
		return "", nil, nil, errors.New("Im Ordner liegen keine Dateien zum Hochladen.")
	}
	if len(files) > maxDirectFiles {
		return "", nil, nil, fmt.Errorf("Der Ordner enthält %d Dateien, \"Nur hochladen\" erlaubt höchstens %d. Bitte aufteilen oder \"Packen und hochladen\" verwenden.", len(files), maxDirectFiles)
	}
	sort.Strings(files)
	return normSrc, files, skipped, nil
}

// prepareDirect: keine Kopie, kein Archiv. Optional PAR2 in einen eigenen Arbeitsordner.
func (a *app) prepareDirect(ctx context.Context, req PipelineRequest, normSrc, displaySrc string) (preparedPackage, *jobError) {
	inputDir, files, skipped, err := collectDirectFiles(normSrc)
	if err != nil {
		return preparedPackage{}, newJobError(http.StatusBadRequest, "INPUT", err)
	}
	a.broker.Log(fmt.Sprintf("=== Nur hochladen: %d Dateien unverändert aus %s ===", len(files), filepath.Base(displaySrc)))
	if len(skipped) > 0 {
		a.broker.Log(fmt.Sprintf("Übersprungen: %s", strings.Join(skipped, ", ")))
	}

	pct := req.EffectivePar2Pct()
	if pct <= 0 {
		return preparedPackage{files: files, direct: true}, nil
	}

	jobDir, jErr := a.createJobDirFor(directWorkSpace(sysproc.CalcPathSize(normSrc), pct), normSrc)
	if jErr != nil {
		return preparedPackage{}, jErr
	}
	baseName := strings.TrimSuffix(filepath.Base(displaySrc), filepath.Ext(displaySrc))
	if info, sErr := os.Stat(normSrc); sErr == nil && info.IsDir() {
		baseName = filepath.Base(displaySrc)
	}
	if req.Obfuscate {
		baseName = archiver.RandomString(req.ObfuscationLength)
	}
	a.crashLog.SetState("phase2:par2")
	par2Files, err := par2.CreatePar2In(ctx, inputDir, jobDir, baseName, req.Par2Engine, pct, files, a.broker.Log, a.broker.Progress)
	if err != nil {
		_ = os.RemoveAll(jobDir)
		return preparedPackage{}, newJobError(http.StatusInternalServerError, "PAR2", err)
	}
	return preparedPackage{files: append(files, par2Files...), jobDir: jobDir, direct: true}, nil
}

// directUploadNames: Bei "Nur hochladen" mit selbst erstellten PAR2 und Verschleierung gehen
// die Originaldateien unter Zufallsnamen raus; PAR2 stellt beim Entpacken die echten Namen
// wieder her. Ohne eigene PAR2 bleiben die Namen original (sonst wären sie nicht rekonstruierbar).
func directUploadNames(pkg preparedPackage, obfuscate bool, length int) []string {
	if !pkg.direct || !obfuscate || pkg.jobDir == "" {
		return nil
	}
	jobPrefix := filepath.Clean(sysproc.NormalizePath(pkg.jobDir)) + string(filepath.Separator)
	names := make([]string, len(pkg.files))
	used := map[string]bool{}
	for i, f := range pkg.files {
		if strings.HasPrefix(filepath.Clean(sysproc.NormalizePath(f)), jobPrefix) {
			continue
		}
		n := archiver.RandomString(length)
		for used[n] {
			n = archiver.RandomString(length)
		}
		used[n] = true
		names[i] = n
	}
	return names
}
