package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"usenet-goup/internal/sysproc"
)

type cachedFileMeta struct {
	Path    string
	Size    int64
	ModTime int64
}

// DryRunCache hält ein fertig gepacktes Paket (Dry-Run oder fehlgeschlagener Upload),
// damit der nächste Upload ohne erneutes Packen startet.
type DryRunCache struct {
	mu                sync.Mutex
	Fingerprint       string
	JobDir            string
	Files             []cachedFileMeta
	EffectivePassword string
}

// resetLocked leert den Cache (Aufrufer hält mu); deleteDir löscht auch den Paketordner.
func (c *DryRunCache) resetLocked(deleteDir bool) {
	if deleteDir && c.JobDir != "" {
		_ = os.RemoveAll(sysproc.NormalizePath(c.JobDir))
	}
	c.Fingerprint = ""
	c.JobDir = ""
	c.Files = nil
	c.EffectivePassword = ""
}

func (c *DryRunCache) Clear(deleteDir bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked(deleteDir)
}

// buildFingerprint: ändert sich Quelle oder ein Pack-Parameter, ist das Paket ungültig.
func buildFingerprint(req PipelineRequest) string {
	normSrc := sysproc.NormalizePath(req.SourcePath)
	srcSize := sysproc.CalcPathSize(normSrc)
	srcMod := int64(-1)
	if info, err := os.Stat(normSrc); err == nil {
		srcMod = info.ModTime().UnixNano()
	}
	manualPwd := ""
	if !req.AutoPassword {
		manualPwd = req.ArchivePassword
	}
	return fmt.Sprintf("%s|%s|%d|%d|%s|%d|%s|%d|%v|%d|%s|%v|%d",
		req.UploadMode, normSrc, srcSize, srcMod,
		req.Engine, req.PartSizeMB, req.Par2Engine, req.EffectivePar2Pct(),
		req.AutoPassword, req.PasswordLength, manualPwd,
		req.Obfuscate, req.ObfuscationLength)
}

// ValidateAndGet liefert die gecachten Dateien, wenn Fingerprint und alle Dateien unverändert sind.
// invalidReason ist gesetzt, wenn ein vorhandener Cache verworfen wurde.
func (c *DryRunCache) ValidateAndGet(fp string) (paths []string, jobDir, pwd, invalidReason string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Fingerprint == "" || len(c.Files) == 0 {
		return nil, "", "", ""
	}
	if c.Fingerprint != fp {
		c.resetLocked(true)
		return nil, "", "", "Parameter oder Quelldatei wurden geändert"
	}
	paths = make([]string, 0, len(c.Files))
	for _, fm := range c.Files {
		info, err := os.Stat(sysproc.NormalizePath(fm.Path))
		if err != nil || info.IsDir() || info.Size() != fm.Size || info.ModTime().UnixNano() != fm.ModTime {
			name := filepath.Base(sysproc.DisplayPath(fm.Path))
			c.resetLocked(true)
			return nil, "", "", fmt.Sprintf("Temp-Datei fehlt oder wurde verändert (%s)", name)
		}
		paths = append(paths, fm.Path)
	}
	return paths, c.JobDir, c.EffectivePassword, ""
}

func (c *DryRunCache) Store(fp string, jobDir string, paths []string, pwd string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.JobDir != "" && c.JobDir != jobDir {
		_ = os.RemoveAll(sysproc.NormalizePath(c.JobDir))
	}
	metas := make([]cachedFileMeta, 0, len(paths))
	for _, p := range paths {
		if info, err := os.Stat(sysproc.NormalizePath(p)); err == nil {
			metas = append(metas, cachedFileMeta{Path: p, Size: info.Size(), ModTime: info.ModTime().UnixNano()})
		}
	}
	c.Fingerprint = fp
	c.JobDir = jobDir
	c.Files = metas
	c.EffectivePassword = pwd
}
