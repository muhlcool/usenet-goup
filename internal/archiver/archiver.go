package archiver

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"usenet-goup/internal/sysproc"
)

type EngineType string

const (
	EngineRAR    EngineType = "rar"
	Engine7Zip   EngineType = "7zip"
	EngineNative EngineType = "native"
)

type AvailableEngine struct {
	ID      EngineType `json:"id"`
	Name    string     `json:"name"`
	Path    string     `json:"path"`
	Default bool       `json:"default"`
}

type PackOptions struct {
	SourcePath        string
	OutputDir         string
	Engine            EngineType
	PartSizeMB        int
	Password          string
	Obfuscate         bool
	ObfuscationLength int
}

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// maxRandomLen: Obergrenze für Zufallsnamen und Passwörter.
const maxRandomLen = 128

func RandomString(length int) string {
	if length < 4 {
		length = 16
	}
	// Fester Puffer: die Speichergröße hängt nie vom übergebenen Wert ab.
	var buf [maxRandomLen]byte
	b := buf[:min(length, maxRandomLen)]
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			b[i] = charset[i%len(charset)]
		} else {
			b[i] = charset[n.Int64()]
		}
	}
	return string(b)
}

// Bekannte Installationspfade (nur eine Stelle, früher doppelt und uneinheitlich gepflegt).
var (
	rarPaths = []string{
		`C:\Program Files\WinRAR\Rar.exe`,
		`C:\Program Files (x86)\WinRAR\Rar.exe`,
		`/usr/local/bin/rar`,
		`/usr/bin/rar`,
		`/opt/homebrew/bin/rar`,
	}
	sevenZipPaths = []string{
		`C:\Program Files\7-Zip\7z.exe`,
		`C:\Program Files (x86)\7-Zip\7z.exe`,
		`/usr/local/bin/7zz`,
		`/opt/homebrew/bin/7zz`,
		`/usr/bin/7z`,
		`/usr/local/bin/7z`,
	}
)

func findRar() string      { return sysproc.FindTool([]string{"rar"}, rarPaths) }
func findSevenZip() string { return sysproc.FindTool([]string{"7zz", "7z"}, sevenZipPaths) }

func DetectEngines() []AvailableEngine {
	var engines []AvailableEngine
	if rarPath := findRar(); rarPath != "" {
		engines = append(engines, AvailableEngine{
			ID:      EngineRAR,
			Name:    "RAR5",
			Path:    rarPath,
			Default: true,
		})
	}
	if sevenZipPath := findSevenZip(); sevenZipPath != "" {
		engines = append(engines, AvailableEngine{
			ID:      Engine7Zip,
			Name:    "7-Zip",
			Path:    sevenZipPath,
			Default: len(engines) == 0,
		})
	}
	engines = append(engines, AvailableEngine{
		ID:      EngineNative,
		Name:    "Split only",
		Path:    "internal",
		Default: len(engines) == 0,
	})
	return engines
}

func CreateArchive(ctx context.Context, opts PackOptions, logFn func(string), progressFn func(sysproc.ProgressStats)) ([]string, string, string, error) {
	normSrc := sysproc.NormalizePath(opts.SourcePath)
	normOut := sysproc.NormalizePath(opts.OutputDir)

	if opts.PartSizeMB < 1 {
		return nil, "", "", fmt.Errorf("ungültige Part-Größe: %d MB", opts.PartSizeMB)
	}
	if err := os.MkdirAll(normOut, 0o700); err != nil {
		return nil, "", "", fmt.Errorf("konnte Ausgabeverzeichnis nicht erstellen: %w", err)
	}

	totalSrcBytes := sysproc.CalcPathSize(normSrc)
	totalSrcMB := float64(totalSrcBytes) / (1024 * 1024)
	estParts := int64(1)
	if opts.PartSizeMB > 0 && totalSrcBytes > 0 {
		partBytes := int64(opts.PartSizeMB) * 1024 * 1024
		estParts = (totalSrcBytes + partBytes - 1) / partBytes
		if estParts < 1 {
			estParts = 1
		}
	}

	baseName := filepath.Base(sysproc.DisplayPath(normSrc))
	archiveName := baseName
	if opts.Obfuscate {
		archiveName = RandomString(opts.ObfuscationLength)
		logFn(fmt.Sprintf("Obfuscation aktiv: Tarnname '%s' (Länge %d) generiert.", archiveName, len(archiveName)))
	}

	var cmd *exec.Cmd
	phaseLabel := "Phase 1/3: Archiv-Verpackung"

	switch opts.Engine {
	case EngineRAR:
		rarPath := findRar()
		if rarPath == "" {
			return nil, "", "", fmt.Errorf("RAR Binary nicht gefunden")
		}
		outArchive := filepath.Join(normOut, archiveName+".rar")
		args := []string{"a", "-m0", "-ma5", "-r", "-ep1", fmt.Sprintf("-v%dm", opts.PartSizeMB)}
		if opts.Password != "" {
			args = append(args, "-hp"+opts.Password)
		}
		args = append(args, outArchive, normSrc)
		phaseLabel = "Phase 1/3: RAR5-Verpackung"
		logFn(fmt.Sprintf("Starte RAR5-Erstellung (%.1f MB | %d MB Parts)...", totalSrcMB, opts.PartSizeMB))
		cmd = exec.CommandContext(ctx, rarPath, args...)

	case Engine7Zip:
		szPath := findSevenZip()
		if szPath == "" {
			return nil, "", "", fmt.Errorf("7-Zip Binary nicht gefunden")
		}
		outArchive := filepath.Join(normOut, archiveName+".7z")
		args := []string{"a", "-mx0", "-bsp1", fmt.Sprintf("-v%dm", opts.PartSizeMB)}
		if opts.Password != "" {
			args = append(args, "-p"+opts.Password, "-mhe=on")
		}
		args = append(args, outArchive, normSrc)
		phaseLabel = "Phase 1/3: 7-Zip-Verpackung"
		logFn(fmt.Sprintf("Starte 7-Zip-Erstellung (%.1f MB | %d MB Parts)...", totalSrcMB, opts.PartSizeMB))
		cmd = exec.CommandContext(ctx, szPath, args...)

	case EngineNative:
		info, err := os.Stat(normSrc)
		if err != nil {
			return nil, "", "", err
		}
		if info.IsDir() {
			return nil, "", "", fmt.Errorf("natives Splitting unterstützt nur Einzeldateien (für Ordner bitte RAR5 oder 7-Zip wählen)")
		}
		if opts.Password != "" {
			logFn("Hinweis: Natives Go-Splitting unterstützt keine Passwortverschlüsselung (Upload erfolgt unverschlüsselt).")
		}
		logFn(fmt.Sprintf("Starte natives Go-Splitting (%.1f MB | %d MB Parts, Originalname für PAR2-Rename gesichert)...", totalSrcMB, opts.PartSizeMB))
		files, err := splitFileNative(ctx, normSrc, normOut, baseName, int64(opts.PartSizeMB)*1024*1024, totalSrcBytes, estParts, progressFn)
		return files, archiveName, archiveName, err

	default:
		return nil, "", "", fmt.Errorf("unbekannte Engine: %s", opts.Engine)
	}

	startTime := time.Now()
	stderr, waitErr := sysproc.RunWithProgress(cmd, func(cliPct float64, elapsed time.Duration) {
		if progressFn == nil {
			return
		}
		writtenBytes, fileCnt := sysproc.CalcDirCurrentSize(normOut, "")
		pct := cliPct
		if pct == 0 && totalSrcBytes > 0 {
			pct = min(float64(writtenBytes)/float64(totalSrcBytes)*100, 99)
		}
		speed := sysproc.SpeedMBs(writtenBytes, elapsed)
		progressFn(sysproc.ProgressStats{
			Phase:         "pack",
			PhaseLabel:    phaseLabel,
			Active:        true,
			CompletedSegs: fileCnt,
			TotalSegs:     estParts,
			Percent:       pct,
			NettoMBs:      speed,
			BruttoMBs:     speed,
			UploadedNetMB: float64(writtenBytes) / (1024 * 1024),
			TotalNetMB:    totalSrcMB,
		})
	})
	if ctx.Err() != nil {
		return nil, "", "", fmt.Errorf("Verpacken abgebrochen")
	}
	if waitErr != nil {
		return nil, "", "", fmt.Errorf("Packer-Fehler: %v | Output: %s", waitErr, stderr)
	}

	entries, err := os.ReadDir(normOut)
	if err != nil {
		return nil, "", "", err
	}
	var createdFiles []string
	var finalBytes int64
	for _, entry := range entries {
		if !entry.IsDir() {
			full := filepath.Join(normOut, entry.Name())
			createdFiles = append(createdFiles, full)
			if info, iErr := entry.Info(); iErr == nil {
				finalBytes += info.Size()
			}
		}
	}

	if progressFn != nil {
		speedMBs := sysproc.SpeedMBs(finalBytes, time.Since(startTime))
		progressFn(sysproc.ProgressStats{
			Phase:         "pack",
			PhaseLabel:    phaseLabel,
			Active:        false,
			CompletedSegs: int64(len(createdFiles)),
			TotalSegs:     int64(len(createdFiles)),
			Percent:       100,
			NettoMBs:      speedMBs,
			BruttoMBs:     speedMBs,
			UploadedNetMB: float64(finalBytes) / (1024 * 1024),
			TotalNetMB:    float64(finalBytes) / (1024 * 1024),
		})
	}

	if len(createdFiles) == 0 {
		return nil, "", "", errors.New("Packer meldete Erfolg, hat aber keine Dateien erzeugt (Programm defekt oder falsche Version?)")
	}
	logFn(fmt.Sprintf("Erfolgreich verpackt: %d Teil-Dateien erstellt.", len(createdFiles)))
	return createdFiles, archiveName, "", nil
}

func ObfuscateSplitParts(files []string, obfuscatedBase string) ([]string, error) {
	if obfuscatedBase == "" {
		return files, nil
	}
	renamed := make([]string, len(files))
	for i, f := range files {
		dir := filepath.Dir(f)
		ext := filepath.Ext(f)
		newPath := filepath.Join(dir, obfuscatedBase+ext)
		if err := os.Rename(f, newPath); err != nil {
			return nil, err
		}
		renamed[i] = newPath
	}
	return renamed, nil
}

func splitFileNative(ctx context.Context, srcPath, outDir, baseName string, chunkSize int64, totalSrcBytes int64, estParts int64, progressFn func(sysproc.ProgressStats)) ([]string, error) {
	if chunkSize <= 0 {
		return nil, fmt.Errorf("ungültige Part-Größe")
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var created []string
	partNum := 1
	buf := make([]byte, 1024*1024)
	var totalWritten int64
	startTime := time.Now()
	lastReport := time.Now()
	totalMB := float64(totalSrcBytes) / (1024 * 1024)

	report := func(done bool) {
		if progressFn == nil {
			return
		}
		elapsed := time.Since(startTime).Seconds()
		speedMBs := 0.0
		if elapsed > 0 {
			speedMBs = (float64(totalWritten) / (1024 * 1024)) / elapsed
		}
		pct := 100.0
		if !done && totalSrcBytes > 0 {
			pct = (float64(totalWritten) / float64(totalSrcBytes)) * 100
		}
		cnt := int64(len(created))
		if !done {
			cnt = int64(partNum)
		}
		totParts := estParts
		if done {
			totParts = int64(len(created))
		}
		progressFn(sysproc.ProgressStats{
			Phase:         "pack",
			PhaseLabel:    "Phase 1/3: Natives Go-Splitting",
			Active:        !done,
			CompletedSegs: cnt,
			TotalSegs:     totParts,
			Percent:       pct,
			NettoMBs:      speedMBs,
			BruttoMBs:     speedMBs,
			UploadedNetMB: float64(totalWritten) / (1024 * 1024),
			TotalNetMB:    totalMB,
		})
	}

	for {
		partPath := filepath.Join(outDir, fmt.Sprintf("%s.%03d", baseName, partNum))
		outFile, err := os.Create(partPath)
		if err != nil {
			return nil, err
		}

		var written int64
		for written < chunkSize {
			if ctx.Err() != nil {
				outFile.Close()
				return nil, fmt.Errorf("Verpacken abgebrochen")
			}
			toRead := int64(len(buf))
			if chunkSize-written < toRead {
				toRead = chunkSize - written
			}
			n, readErr := f.Read(buf[:toRead])
			if n > 0 {
				if _, wErr := outFile.Write(buf[:n]); wErr != nil {
					outFile.Close()
					return nil, wErr
				}
				written += int64(n)
				totalWritten += int64(n)
				if time.Since(lastReport) >= 350*time.Millisecond {
					report(false)
					lastReport = time.Now()
				}
			}
			if readErr == io.EOF {
				if cErr := outFile.Close(); cErr != nil {
					return nil, cErr
				}
				if written > 0 {
					created = append(created, partPath)
				} else {
					_ = os.Remove(partPath)
				}
				report(true)
				return created, nil
			}
			if readErr != nil {
				outFile.Close()
				return nil, readErr
			}
		}
		if cErr := outFile.Close(); cErr != nil {
			return nil, cErr
		}
		created = append(created, partPath)
		partNum++
	}
}
