package par2

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"usenet-goup/internal/sysproc"
)

type Par2EngineType string

const (
	EngineAuto   Par2EngineType = "auto"
	EngineParPar Par2EngineType = "parpar"
	EngineTurbo  Par2EngineType = "turbo"
)

type AvailablePar2Engine struct {
	ID      Par2EngineType `json:"id"`
	Name    string         `json:"name"`
	Path    string         `json:"path"`
	Default bool           `json:"default"`
}

func FindParParBinary() string {
	return sysproc.FindTool([]string{"parpar"}, []string{
		`C:\Program Files\ParPar\parpar.exe`,
		`/usr/local/bin/parpar`,
		`/usr/bin/parpar`,
		`/opt/homebrew/bin/parpar`,
	})
}

func FindTurboBinary() string {
	return sysproc.FindTool([]string{"par2", "par2create"}, []string{
		`C:\Program Files\SABnzbd\win\par2\x64\par2.exe`,
		`C:\Program Files\SABnzbd\par2.exe`,
		`C:\Program Files (x86)\SABnzbd\win\par2\par2.exe`,
		`C:\Program Files\MultiPar\par2j64.exe`,
		`/usr/local/bin/par2`,
		`/usr/bin/par2`,
		`/opt/homebrew/bin/par2`,
	})
}

func DetectPar2Engines() []AvailablePar2Engine {
	var engines []AvailablePar2Engine

	parparPath := FindParParBinary()
	turboPath := FindTurboBinary()

	if parparPath != "" {
		engines = append(engines, AvailablePar2Engine{
			ID:      EngineParPar,
			Name:    "ParPar (Gefunden - Ultra-Schnell)",
			Path:    parparPath,
			Default: true,
		})
	}
	if turboPath != "" {
		engines = append(engines, AvailablePar2Engine{
			ID:      EngineTurbo,
			Name:    "par2cmdline-turbo / par2 (Gefunden)",
			Path:    turboPath,
			Default: len(engines) == 0,
		})
	}

	if len(engines) == 0 {
		engines = append(engines, AvailablePar2Engine{
			ID:      EngineAuto,
			Name:    "Keine PAR2-Engine gefunden (parpar / par2 fehlt)",
			Path:    "",
			Default: true,
		})
	}

	return engines
}

func CreatePar2(ctx context.Context, workDir string, baseName string, engine Par2EngineType, redundancyPct int, files []string, logFn func(string), progressFn func(sysproc.ProgressStats)) ([]string, error) {
	if redundancyPct <= 0 {
		logFn("PAR2-Erstellung übersprungen (Redundanz = 0%).")
		return nil, nil
	}

	normWorkDir := sysproc.NormalizePath(workDir)
	var totalInputBytes int64
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			totalInputBytes += info.Size()
		}
	}
	totalInputMB := float64(totalInputBytes) / (1024 * 1024)

	selected := engine
	if selected == "" || selected == EngineAuto {
		if FindParParBinary() != "" {
			selected = EngineParPar
		} else if FindTurboBinary() != "" {
			selected = EngineTurbo
		}
	}

	// Relativ zum Arbeitsordner aufrufen: hält die Kommandozeile kurz (Windows-Limit 32.767 Zeichen).
	par2Base := baseName + ".par2"
	relFiles := make([]string, 0, len(files))
	for _, f := range files {
		if rel, err := filepath.Rel(normWorkDir, sysproc.NormalizePath(f)); err == nil && !strings.HasPrefix(rel, "..") {
			relFiles = append(relFiles, rel)
		} else {
			relFiles = append(relFiles, f)
		}
	}
	sliceArg := parparSliceSize(totalInputBytes)
	var cmd *exec.Cmd
	phaseLabel := "Phase 2/3: PAR2-Erstellung"

	switch selected {
	case EngineParPar:
		bin := FindParParBinary()
		if bin == "" {
			return nil, fmt.Errorf("ParPar Binary (parpar.exe) nicht gefunden")
		}
		args := []string{
			"-s", sliceArg,
			"-r", fmt.Sprintf("%d%%", redundancyPct),
			"--progress=stdout",
			"-o", par2Base,
		}
		args = append(args, relFiles...)
		phaseLabel = "Phase 2/3: ParPar-Berechnung"
		logFn(fmt.Sprintf("Starte ParPar (%d%% Redundanz, %s Slices)...", redundancyPct, sliceArg))
		cmd = exec.CommandContext(ctx, bin, args...)

	case EngineTurbo:
		bin := FindTurboBinary()
		if bin == "" {
			return nil, fmt.Errorf("par2 Binary (par2.exe) nicht gefunden")
		}
		args := []string{"c", fmt.Sprintf("-r%d", redundancyPct), par2Base}
		args = append(args, relFiles...)
		phaseLabel = "Phase 2/3: par2-Berechnung"
		logFn(fmt.Sprintf("Starte par2cmdline-turbo (%d%% Redundanz)...", redundancyPct))
		cmd = exec.CommandContext(ctx, bin, args...)

	default:
		return nil, fmt.Errorf("keine PAR2-Engine installiert (bitte parpar oder par2 im Programmordner ablegen)")
	}

	cmd.Dir = sysproc.DisplayPath(normWorkDir)
	startTime := time.Now()
	stderr, waitErr := sysproc.RunWithProgress(cmd, func(pct float64, elapsed time.Duration) {
		if progressFn == nil {
			return
		}
		parBytes, parCount := sysproc.CalcDirCurrentSize(normWorkDir, ".par2")
		processedBytes := int64(pct / 100 * float64(totalInputBytes))
		speed := sysproc.SpeedMBs(processedBytes, elapsed)
		progressFn(sysproc.ProgressStats{
			Phase:         "par2",
			PhaseLabel:    phaseLabel,
			Active:        true,
			CompletedSegs: parCount,
			TotalSegs:     int64(len(files)),
			Percent:       pct,
			NettoMBs:      speed,
			BruttoMBs:     speed,
			UploadedNetMB: float64(parBytes) / (1024 * 1024),
			TotalNetMB:    totalInputMB,
		})
	})
	if ctx.Err() != nil {
		return nil, fmt.Errorf("PAR2 abgebrochen")
	}
	if waitErr != nil {
		return nil, fmt.Errorf("PAR2-Fehler: %v | Output: %s", waitErr, stderr)
	}

	entries, err := os.ReadDir(normWorkDir)
	if err != nil {
		return nil, err
	}

	var par2Files []string
	var finalParBytes int64
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(strings.ToLower(e.Name()), ".par2") {
			full := filepath.Join(normWorkDir, e.Name())
			par2Files = append(par2Files, full)
			if info, iErr := e.Info(); iErr == nil {
				finalParBytes += info.Size()
			}
		}
	}

	if progressFn != nil {
		speedMBs := sysproc.SpeedMBs(totalInputBytes, time.Since(startTime))
		progressFn(sysproc.ProgressStats{
			Phase:         "par2",
			PhaseLabel:    phaseLabel,
			Active:        false,
			CompletedSegs: int64(len(par2Files)),
			TotalSegs:     int64(len(par2Files)),
			Percent:       100,
			NettoMBs:      speedMBs,
			BruttoMBs:     speedMBs,
			UploadedNetMB: float64(finalParBytes) / (1024 * 1024),
			TotalNetMB:    totalInputMB,
		})
	}

	logFn(fmt.Sprintf("PAR2 abgeschlossen: %d Reparaturdateien erzeugt.", len(par2Files)))
	return par2Files, nil
}

// parparSliceSize hält die Zahl der Eingabe-Slices unter dem PAR2-Maximum (32.768).
// Basis sind 768 KiB (= Segmentgröße); bei großen Releases ein Vielfaches davon.
func parparSliceSize(totalBytes int64) string {
	const base = 768 * 1024
	const maxSlices = 32000
	mult := int64(1)
	if totalBytes > 0 {
		need := (totalBytes + maxSlices - 1) / maxSlices
		mult = (need + base - 1) / base
		if mult < 1 {
			mult = 1
		}
	}
	return fmt.Sprintf("%dK", mult*768)
}
