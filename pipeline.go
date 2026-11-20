package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"usenet-goup/internal/archiver"
	"usenet-goup/internal/config"
	"usenet-goup/internal/media"
	"usenet-goup/internal/nntp"
	"usenet-goup/internal/par2"
	"usenet-goup/internal/sysproc"
)

// PipelineRequest ist ein Upload-Auftrag aus der Oberfläche.
type PipelineRequest struct {
	config.JobSettings
	DryRun     bool              `json:"dryRun"`
	Server     nntp.ServerConfig `json:"server"`
	SourcePath string            `json:"sourcePath"`
	NfoPath    string            `json:"nfoPath"`
	CoverPath  string            `json:"coverPath"`
}

func sanitizeRequest(req *PipelineRequest) {
	req.JobSettings.Sanitize()
	config.SanitizeServer(&req.Server)
}

// jobError trägt HTTP-Status und Kategorie (für crash-freeze.log) eines fehlgeschlagenen Schritts.
type jobError struct {
	status int
	kind   string
	err    error
}

func newJobError(status int, kind string, err error) *jobError {
	return &jobError{status: status, kind: kind, err: err}
}

// preparedPackage: fertige Upload-Dateien (frisch gepackt oder aus dem Cache).
type preparedPackage struct {
	files    []string
	jobDir   string
	password string
}

func (a *app) handleRun(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	ctx, finishJob, ok := a.jobs.start()
	if !ok {
		sendJSONError(w, http.StatusConflict, "Es läuft bereits ein Job. Bitte warten oder abbrechen.")
		return
	}
	defer finishJob()

	defer func() {
		if rec := recover(); rec != nil {
			a.crashLog.RecordPanic("api/run", rec)
			a.crashLog.SetState("idle")
			sendJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Unerwarteter Fehler (Details in %s): %v", a.crashLog.Path(), rec))
		}
	}()

	var req PipelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	sanitizeRequest(&req)

	result, jErr := a.runJob(ctx, req)
	if jErr != nil {
		msg := jErr.err.Error()
		if ctx.Err() != nil {
			msg = "Job abgebrochen."
		}
		a.broker.Log(fmt.Sprintf("Abbruch (%s): %s", jErr.kind, msg))
		a.crashLog.AppendEvent(jErr.kind+"-ERROR", jErr.err.Error())
		sendJSONError(w, jErr.status, msg)
		return
	}
	sendJSON(w, http.StatusOK, result)
}

// runJob: Quelle prüfen → packen + PAR2 (oder Cache) → hochladen → NZB → Metadaten + Vorlage.
func (a *app) runJob(ctx context.Context, req PipelineRequest) (map[string]any, *jobError) {
	if !req.AutoPassword && strings.ContainsAny(req.ArchivePassword, `[]"|` ) {
		return nil, newJobError(http.StatusBadRequest, "INPUT", errors.New(`Das manuelle Passwort darf keine Zeichen [ ] " | enthalten.`))
	}
	normSrc := sysproc.NormalizePath(req.SourcePath)
	displaySrc := sysproc.DisplayPath(normSrc)
	if normSrc == "" {
		return nil, newJobError(http.StatusBadRequest, "INPUT", errors.New("Kein Quellpfad angegeben."))
	}
	if _, err := os.Stat(normSrc); err != nil {
		return nil, newJobError(http.StatusBadRequest, "INPUT", fmt.Errorf("Quellpfad nicht lesbar: %s (%v)", displaySrc, err))
	}

	a.crashLog.SetState("job:" + filepath.Base(displaySrc))
	a.crashLog.AppendEvent("JOB-START", fmt.Sprintf("source=%s engine=%s partMB=%d par2=%d%% dryRun=%v", displaySrc, req.Engine, req.PartSizeMB, req.Par2Pct, req.DryRun))
	defer a.crashLog.SetState("idle")

	_ = ensureCustomTemplatesDir(a.appDir, a.broker.Log)

	nfoContent, foundNfoPath := nntp.FindAndReadNFO(normSrc, req.NfoPath)
	if foundNfoPath != "" {
		a.broker.Log(fmt.Sprintf("NFO-Datei erkannt und dekodiert: %s", foundNfoPath))
	}
	var mediaInfoContent string
	if req.IncludeMediaInfo && normalizeTemplateCategory(req.TemplateType) == "movies_series" {
		mediaInfoContent = media.ExtractMediaInfo(normSrc, a.broker.Log)
	}

	fp := buildFingerprint(req)
	pkg, jErr := a.preparePackage(ctx, req, normSrc, displaySrc, fp)
	if jErr != nil {
		return nil, jErr
	}

	if req.DryRun {
		a.dryCache.Store(fp, pkg.jobDir, pkg.files, pkg.password)
		a.broker.Log(fmt.Sprintf("Dry-Run abgeschlossen! %d Dateien liegen bereit in: %s", len(pkg.files), sysproc.DisplayPath(pkg.jobDir)))
		if pkg.password != "" {
			a.broker.Log(fmt.Sprintf("Verwendetes Archiv-Passwort: %s", pkg.password))
		}
		return map[string]any{"status": "dryrun_ok", "tempDir": sysproc.DisplayPath(pkg.jobDir), "fileCount": len(pkg.files)}, nil
	}

	if req.Server.Host == "" {
		// Paket behalten, damit nach Eintragen des Hosts nicht neu verpackt werden muss.
		a.dryCache.Store(fp, pkg.jobDir, pkg.files, pkg.password)
		return nil, newJobError(http.StatusBadRequest, "INPUT", errors.New("Kein NNTP-Host konfiguriert."))
	}

	a.crashLog.SetState("phase3:upload")
	uploadRes, err := a.upload(ctx, req.Server, pkg.files, req.Obfuscate, req.ObfuscationLength, a.broker.Log, a.broker.Progress, a.crashLog.Heartbeat)
	if err != nil {
		// Fertiges Paket behalten: ein erneuter Klick lädt ohne Neupacken hoch.
		a.dryCache.Store(fp, pkg.jobDir, pkg.files, pkg.password)
		a.broker.Log("Hinweis: Das fertige Paket bleibt erhalten, ein erneuter Start lädt ohne Neupacken hoch.")
		return nil, newJobError(http.StatusInternalServerError, "UPLOAD", err)
	}

	// NZB SOFORT nach dem Upload schreiben: Metadaten-/Cover-Suche darf sie nie gefährden.
	origTitle := filepath.Base(displaySrc)
	nzbFullPath, err := a.writeNZB(req, normSrc, origTitle, pkg.password, uploadRes)
	if err != nil {
		return nil, newJobError(http.StatusInternalServerError, "NZB", err)
	}
	displayNzb := sysproc.DisplayPath(nzbFullPath)
	a.dryCache.Clear(true)
	_ = os.RemoveAll(sysproc.NormalizePath(pkg.jobDir))

	a.crashLog.SetState("phase4:meta")
	imdbID, synopsis, coverURL, coverPreview := a.resolveMetadata(req, normSrc, origTitle, nfoContent)

	releaseTitle := strings.TrimSuffix(origTitle, filepath.Ext(origTitle))
	if info, statErr := os.Stat(normSrc); statErr == nil && info.IsDir() {
		releaseTitle = origTitle
	}
	tVars := templateVars{
		Title:     releaseTitle,
		Header:    uploadRes.UsedHeader,
		Groups:    strings.Join(uploadRes.Groups, ","),
		Password:  pkg.password,
		Timestamp: uploadRes.PostTimestamp,
		NFO:       nfoContent,
		MediaInfo: mediaInfoContent,
		CoverURL:  coverURL,
		IMDbID:    imdbID,
		Synopsis:  synopsis,
	}
	var txtPath, postingTemplate string
	if customBody, ok := loadAndRenderCustomTemplate(a.appDir, req.TemplateType, tVars, a.broker.Log); ok {
		txtPath = strings.TrimSuffix(nzbFullPath, filepath.Ext(nzbFullPath)) + ".txt"
		postingTemplate = customBody
		if wErr := os.WriteFile(sysproc.NormalizePath(txtPath), []byte(customBody), 0o600); wErr != nil {
			err = wErr
		} else {
			err = nil
			txtPath = sysproc.DisplayPath(txtPath)
		}
	} else {
		txtPath, postingTemplate, err = nntp.WritePostingTemplateWithType(nzbFullPath, releaseTitle, uploadRes.UsedHeader, uploadRes.Groups, pkg.password, uploadRes.PostTimestamp, nfoContent, mediaInfoContent, coverURL, imdbID, synopsis, req.Language, req.TemplateType)
	}
	if err != nil {
		a.broker.Log(fmt.Sprintf("Warnung bei Posting-Vorlage: %v", err))
	}

	a.broker.Log(fmt.Sprintf("=== ERFOLG! NZB gespeichert unter: %s ===", displayNzb))
	if txtPath != "" {
		a.broker.Log(fmt.Sprintf("Posting-Vorlage gespeichert unter: %s", txtPath))
	}
	a.crashLog.AppendEvent("JOB-DONE", "nzb="+displayNzb)
	return map[string]any{"status": "ok", "nzbPath": displayNzb, "txtPath": txtPath, "coverPreview": coverPreview, "postingTemplate": postingTemplate}, nil
}

// preparePackage nimmt ein gültiges Paket aus dem Cache oder packt neu (Archiv + PAR2).
func (a *app) preparePackage(ctx context.Context, req PipelineRequest, normSrc, displaySrc, fp string) (preparedPackage, *jobError) {
	if !req.DryRun {
		files, dir, pwd, invalidReason := a.dryCache.ValidateAndGet(fp)
		if len(files) > 0 {
			a.broker.Log(fmt.Sprintf("=== Übernehme verifiziertes Paket (%d Dateien) ohne erneutes Verpacken ===", len(files)))
			return preparedPackage{files: files, jobDir: dir, password: pwd}, nil
		}
		if invalidReason != "" {
			a.broker.Log(fmt.Sprintf("Hinweis: Paket-Cache ungültig (%s) — verpacke neu...", invalidReason))
		}
	}
	a.dryCache.Clear(true)

	password := req.ArchivePassword
	if req.Engine == archiver.EngineNative {
		password = ""
	} else if req.AutoPassword {
		password = archiver.RandomString(req.PasswordLength)
		a.broker.Log(fmt.Sprintf("Zufälliges Archiv-Passwort generiert (%d Zeichen): %s", len(password), password))
	}

	jobDir, jErr := a.createJobDir(req, normSrc)
	if jErr != nil {
		return preparedPackage{}, jErr
	}
	failCleanup := func(kind string, err error) (preparedPackage, *jobError) {
		_ = os.RemoveAll(jobDir)
		return preparedPackage{}, newJobError(http.StatusInternalServerError, kind, err)
	}
	a.broker.Log(fmt.Sprintf("=== Starte Job für: %s ===", filepath.Base(displaySrc)))

	a.crashLog.SetState("phase1:pack")
	packedFiles, archiveName, nativeObfBase, err := archiver.CreateArchive(ctx, archiver.PackOptions{
		SourcePath:        normSrc,
		OutputDir:         jobDir,
		Engine:            req.Engine,
		PartSizeMB:        req.PartSizeMB,
		Password:          password,
		Obfuscate:         req.Obfuscate,
		ObfuscationLength: req.ObfuscationLength,
	}, a.broker.Log, a.broker.Progress)
	if err != nil {
		return failCleanup("PACK", err)
	}

	a.crashLog.SetState("phase2:par2")
	par2Files, err := par2.CreatePar2(ctx, jobDir, archiveName, req.Par2Engine, req.Par2Pct, packedFiles, a.broker.Log, a.broker.Progress)
	if err != nil {
		return failCleanup("PAR2", err)
	}

	if nativeObfBase != "" && req.Obfuscate {
		if packedFiles, err = archiver.ObfuscateSplitParts(packedFiles, nativeObfBase); err != nil {
			return failCleanup("RENAME", err)
		}
	}
	return preparedPackage{files: append(packedFiles, par2Files...), jobDir: jobDir, password: password}, nil
}

// createJobDir legt einen eindeutigen Temp-Ordner (0700) in os.TempDir() an, nach Prüfung des freien Platzes.
func (a *app) createJobDir(req PipelineRequest, normSrc string) (string, *jobError) {
	workBase := os.TempDir()
	need := requiredWorkSpace(sysproc.CalcPathSize(normSrc), req.Par2Pct)
	if free := sysproc.FreeDiskBytes(workBase); free >= 0 && free < need {
		return "", newJobError(http.StatusInternalServerError, "DISKSPACE", fmt.Errorf("zu wenig Platz im Temp-Ordner %s: frei %s, benötigt ca. %s",
			sysproc.DisplayPath(workBase), formatGB(free), formatGB(need)))
	}
	dir, err := os.MkdirTemp(workBase, "usenet-goup-*")
	if err != nil {
		return "", newJobError(http.StatusInternalServerError, "TEMP", err)
	}
	return dir, nil
}

// writeNZB schreibt die NZB in den Zielordner; scheitert das, wird sie in den Temp-Ordner gerettet.
func (a *app) writeNZB(req PipelineRequest, normSrc, origTitle, password string, res *nntp.UploadResult) (string, error) {
	outDir := sysproc.NormalizePath(req.NzbOutDir)
	if outDir == "" {
		outDir = filepath.Dir(normSrc)
	}
	nzbPath := filepath.Join(outDir, sysproc.SafeOutFileName(origTitle, ".nzb"))
	if err := nntp.WriteNZB(nzbPath, origTitle, password, res.NZBFiles); err != nil {
		rescue := filepath.Join(os.TempDir(), sysproc.SafeOutFileName(origTitle, ".nzb"))
		if rErr := nntp.WriteNZB(rescue, origTitle, password, res.NZBFiles); rErr != nil {
			return "", err
		}
		a.broker.Log(fmt.Sprintf("NZB-Fehler im Zielordner (%v) — NZB gerettet nach: %s", err, rescue))
		nzbPath = rescue
	}
	a.broker.Log(fmt.Sprintf("NZB gespeichert: %s", sysproc.DisplayPath(nzbPath)))
	a.crashLog.AppendEvent("NZB-WRITTEN", sysproc.DisplayPath(nzbPath))
	return nzbPath, nil
}

// resolveMetadata: IMDb, Beschreibung und Cover.
// Priorität beim Cover: manuell gewählt > TMDB/TVMaze > Bild im Quellordner.
func (a *app) resolveMetadata(req PipelineRequest, normSrc, origTitle, nfoContent string) (imdbID, synopsis, coverURL, coverPreview string) {
	isMinimal := normalizeTemplateCategory(req.TemplateType) != "movies_series"
	var metaCover string
	if !isMinimal && req.AutoSearchCover {
		meta := a.fetchMeta(&http.Client{Timeout: 25 * time.Second}, origTitle, nfoContent, req.TmdbApiKey, req.Language, a.broker.Log)
		imdbID, synopsis, metaCover = meta.IMDbID, meta.Synopsis, meta.CoverURL
	}
	var candidates []string
	if manual := strings.TrimSpace(req.CoverPath); manual != "" {
		candidates = append(candidates, manual)
	}
	if !isMinimal && metaCover != "" {
		candidates = append(candidates, metaCover)
	}
	candidates = append(candidates, "") // "" = Bild im Quellordner suchen
	for _, c := range candidates {
		if coverURL, coverPreview = media.ResolveCoverForTemplate(normSrc, c, req.ImageHoster, req.ImgbbApiKey, a.broker.Log); coverURL != "" {
			break
		}
	}
	return imdbID, synopsis, coverURL, coverPreview
}
