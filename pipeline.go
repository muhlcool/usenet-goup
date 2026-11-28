package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

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
	// ExistingPassword: Passwort eines vom Nutzer selbst verschlüsselten Archivs ("Nur hochladen").
	// Kommt nur in NZB und Vorlage, wird nie in config.json gespeichert.
	ExistingPassword string `json:"existingPassword"`
}

// maxPasswordLen begrenzt Passwörter (NZB, Vorlage, Kommandozeile der Packer).
const maxPasswordLen = 128

// checkPassword: Zeichen, an denen Vorlage, Foren oder Packer scheitern, und Steuerzeichen.
func checkPassword(pw, what string) error {
	switch {
	case len(pw) > maxPasswordLen:
		return fmt.Errorf("Das %s Passwort ist zu lang (höchstens %d Zeichen).", what, maxPasswordLen)
	case strings.ContainsAny(pw, `[]"|`):
		return fmt.Errorf(`Das %s Passwort darf keine Zeichen [ ] " | enthalten.`, what)
	case strings.ContainsFunc(pw, unicode.IsControl):
		return fmt.Errorf("Das %s Passwort darf keine Steuerzeichen enthalten.", what)
	}
	return nil
}

func sanitizeRequest(req *PipelineRequest) {
	req.JobSettings.Sanitize()
	config.SanitizeServer(&req.Server)
	// Leerzeichen am Rand stammen fast immer aus Kopieren/Einfügen.
	req.ExistingPassword = strings.TrimSpace(req.ExistingPassword)
}

// verifyRecheck: Wartezeit, bevor fehlende Teile ein zweites Mal geprüft werden.
var verifyRecheck = 30 * time.Second

// verifyUpload prüft per STAT, ob alle Teile auf dem Server liegen. Liefert die Zahl fehlender
// Teile. Läuft immer; ein Fehler der Prüfung selbst bricht den Job nicht ab (die NZB existiert bereits).
func (a *app) verifyUpload(ctx context.Context, req PipelineRequest, res *nntp.UploadResult) int {
	a.crashLog.SetState("phase3:verify")
	ids := nntp.MessageIDs(res.NZBFiles)
	a.broker.Log(fmt.Sprintf("Prüfe, ob alle %d Teile auf dem Server angekommen sind ...", len(ids)))
	v, err := a.verify(ctx, req.Server, ids, verifyRecheck, a.broker.Log)
	switch {
	case err != nil:
		a.broker.Log(fmt.Sprintf("Hinweis: Prüfung nach dem Upload nicht möglich (%v). Die NZB wurde trotzdem gespeichert.", err))
		return 0
	case len(v.Missing) > 0:
		a.broker.Log(fmt.Sprintf("WARNUNG: %d von %d Teilen fehlen auf dem Server. Die NZB ist so unvollständig, bitte den Upload wiederholen (das Paket bleibt erhalten).", len(v.Missing), v.Checked))
		return len(v.Missing)
	default:
		a.broker.Log(fmt.Sprintf("Prüfung OK: alle %d Teile sind auf dem Server.", v.Checked))
		return 0
	}
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
	jobDir   string // nur eigene Arbeitsdateien; bei "Nur hochladen" ohne PAR2 leer
	password string
	direct   bool // files enthält Originaldateien des Nutzers: niemals löschen/umbenennen
}

// removeJobDir löscht ausschließlich den eigenen Arbeitsordner, nie Quelldateien.
func (p preparedPackage) removeJobDir() {
	if p.jobDir != "" {
		_ = os.RemoveAll(sysproc.NormalizePath(p.jobDir))
	}
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
	if !decodeJSONBody(w, r, maxRunJSONBody, &req) {
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
	if !req.Direct() && !req.AutoPassword {
		if err := checkPassword(req.ArchivePassword, "manuelle"); err != nil {
			return nil, newJobError(http.StatusBadRequest, "INPUT", err)
		}
	}
	if req.Direct() {
		if err := checkPassword(req.ExistingPassword, "vorhandene"); err != nil {
			return nil, newJobError(http.StatusBadRequest, "INPUT", err)
		}
	}
	withTemplate := !req.NoPostingTemplate
	normSrc := sysproc.NormalizePath(req.SourcePath)
	displaySrc := sysproc.DisplayPath(normSrc)
	if normSrc == "" {
		return nil, newJobError(http.StatusBadRequest, "INPUT", errors.New("Kein Quellpfad angegeben."))
	}
	if _, err := sysproc.CheckPlainPath(normSrc); err != nil {
		return nil, newJobError(http.StatusBadRequest, "INPUT", fmt.Errorf("Quellpfad nicht verwendbar: %s (%v)", displaySrc, err))
	}
	if out := sysproc.NormalizePath(req.NzbOutDir); out != "" {
		if info, err := sysproc.CheckPlainPath(out); err == nil && !info.IsDir() {
			return nil, newJobError(http.StatusBadRequest, "INPUT", fmt.Errorf("Ausgabeordner ist kein Ordner: %s", sysproc.DisplayPath(out)))
		} else if errors.Is(err, sysproc.ErrNotPlainPath) {
			return nil, newJobError(http.StatusBadRequest, "INPUT", fmt.Errorf("Ausgabeordner nicht verwendbar: %s (%v)", sysproc.DisplayPath(out), err))
		}
	}

	a.crashLog.SetState("job:" + filepath.Base(displaySrc))
	a.crashLog.AppendEvent("JOB-START", fmt.Sprintf("source=%s mode=%s engine=%s partMB=%d par2=%d%% dryRun=%v", displaySrc, req.UploadMode, req.Engine, req.PartSizeMB, req.EffectivePar2Pct(), req.DryRun))
	defer a.crashLog.SetState("idle")

	if withTemplate {
		_ = ensureCustomTemplatesDir(a.appDir, a.broker.Log)
	}

	nfoContent, foundNfoPath := nntp.FindAndReadNFO(normSrc, req.NfoPath)
	if foundNfoPath != "" {
		a.broker.Log(fmt.Sprintf("NFO-Datei erkannt und dekodiert: %s", foundNfoPath))
	}
	var mediaInfoContent string
	if withTemplate && req.IncludeMediaInfo && normalizeTemplateCategory(req.TemplateType) == "movies_series" {
		mediaInfoContent = media.ExtractMediaInfo(normSrc, a.broker.Log)
	}

	fp := buildFingerprint(req)
	pkg, jErr := a.preparePackage(ctx, req, normSrc, displaySrc, fp)
	if jErr != nil {
		return nil, jErr
	}
	if pkg.direct {
		// Nichts gepackt: ein Passwort gibt es nur, wenn der Nutzer sein Archiv selbst verschlüsselt hat.
		pkg.password = req.ExistingPassword
	}

	if req.DryRun {
		a.dryCache.Store(fp, pkg.jobDir, pkg.files, pkg.password)
		if pkg.jobDir == "" {
			a.broker.Log(fmt.Sprintf("Dry-Run abgeschlossen! %d Dateien werden unverändert hochgeladen (nichts zu erstellen).", len(pkg.files)))
			return map[string]any{"status": "dryrun_ok", "tempDir": "", "fileCount": len(pkg.files)}, nil
		}
		a.broker.Log(fmt.Sprintf("Dry-Run abgeschlossen! %d Dateien liegen bereit in: %s", len(pkg.files), sysproc.DisplayPath(pkg.jobDir)))
		if pkg.password != "" {
			a.broker.LogSecret(fmt.Sprintf("Verwendetes Archiv-Passwort: %s", pkg.password), "Verwendetes Archiv-Passwort: (nur in der Oberfläche angezeigt)")
		}
		return map[string]any{"status": "dryrun_ok", "tempDir": sysproc.DisplayPath(pkg.jobDir), "fileCount": len(pkg.files)}, nil
	}

	if req.Server.Host == "" {
		// Paket behalten, damit nach Eintragen des Hosts nicht neu verpackt werden muss.
		a.dryCache.Store(fp, pkg.jobDir, pkg.files, pkg.password)
		return nil, newJobError(http.StatusBadRequest, "INPUT", errors.New("Kein NNTP-Host konfiguriert."))
	}

	a.crashLog.SetState("phase3:upload")
	uploadNames := directUploadNames(pkg, req.Obfuscate, req.ObfuscationLength)
	if uploadNames != nil {
		a.broker.Log("Dateinamen werden verschleiert hochgeladen (Originale bleiben unverändert, PAR2 stellt die Namen wieder her).")
	} else if pkg.direct && req.Obfuscate {
		a.broker.Log("Hinweis: Ohne eigene PAR2 werden nur die Subject-Header verschleiert, die Dateinamen bleiben original.")
	}
	uploadRes, err := a.upload(ctx, req.Server, pkg.files, uploadNames, req.Obfuscate, req.ObfuscationLength, a.broker.Log, a.broker.Progress, a.crashLog.Heartbeat)
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

	missing := a.verifyUpload(ctx, req, uploadRes)
	if missing > 0 {
		// Paket behalten: ein erneuter Start lädt ohne Neupacken noch einmal hoch.
		a.dryCache.Store(fp, pkg.jobDir, pkg.files, pkg.password)
	} else {
		a.dryCache.Clear(true)
		pkg.removeJobDir()
	}

	if !withTemplate {
		a.broker.Log(fmt.Sprintf("=== ERFOLG! NZB gespeichert unter: %s ===", displayNzb))
		a.broker.Log("Posting-Vorlage abgeschaltet: nur die NZB wurde erstellt.")
		a.crashLog.AppendEvent("JOB-DONE", "nzb="+displayNzb)
		return map[string]any{"status": "ok", "nzbPath": displayNzb, "verifyMissing": missing}, nil
	}

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
	return map[string]any{"status": "ok", "nzbPath": displayNzb, "txtPath": txtPath, "coverPreview": coverPreview, "postingTemplate": postingTemplate, "verifyMissing": missing}, nil
}

// preparePackage nimmt ein gültiges Paket aus dem Cache oder packt neu (Archiv + PAR2).
func (a *app) preparePackage(ctx context.Context, req PipelineRequest, normSrc, displaySrc, fp string) (preparedPackage, *jobError) {
	if !req.DryRun {
		files, dir, pwd, invalidReason := a.dryCache.ValidateAndGet(fp)
		if len(files) > 0 {
			a.broker.Log(fmt.Sprintf("=== Übernehme verifiziertes Paket (%d Dateien) ohne erneutes Verpacken ===", len(files)))
			return preparedPackage{files: files, jobDir: dir, password: pwd, direct: req.Direct()}, nil
		}
		if invalidReason != "" {
			a.broker.Log(fmt.Sprintf("Hinweis: Paket-Cache ungültig (%s) — verpacke neu...", invalidReason))
		}
	}
	a.dryCache.Clear(true)

	if req.Direct() {
		return a.prepareDirect(ctx, req, normSrc, displaySrc)
	}

	password := req.ArchivePassword
	if req.Engine == archiver.EngineNative {
		password = ""
	} else if req.AutoPassword {
		password = archiver.RandomString(req.PasswordLength)
		a.broker.LogSecret(fmt.Sprintf("Zufälliges Archiv-Passwort generiert (%d Zeichen): %s", len(password), password),
			fmt.Sprintf("Zufälliges Archiv-Passwort generiert (%d Zeichen, nur in der Oberfläche angezeigt).", len(password)))
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
	par2Files, err := par2.CreatePar2(ctx, jobDir, archiveName, req.Par2Engine, req.EffectivePar2Pct(), packedFiles, a.broker.Log, a.broker.Progress)
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

// workBases liefert die Kandidaten für den Arbeitsordner: zuerst das System-Temp
// (respektiert TMPDIR/TEMP), dann "work" im Datenordner der App (auf der Platte).
func (a *app) workBases() []string {
	return []string{os.TempDir(), filepath.Join(a.appDir, "work")}
}

// createJobDir legt einen eindeutigen Arbeitsordner (0700) an. Liegt das System-Temp im
// RAM (tmpfs, z. B. Raspberry Pi) oder ist es zu klein, wird automatisch der Ordner
// "work" im Datenordner genutzt. RAM-Ordner nur als letzte Wahl, wenn sonst nichts passt.
func (a *app) createJobDir(req PipelineRequest, normSrc string) (string, *jobError) {
	return a.createJobDirFor(requiredWorkSpace(sysproc.CalcPathSize(normSrc), req.EffectivePar2Pct()), normSrc)
}

// createJobDirFor: wie createJobDir, aber mit vorgegebenem Platzbedarf in Bytes.
func (a *app) createJobDirFor(need int64, _ string) (string, *jobError) {
	bases := a.workBases()
	var notes []string
	for pass := 0; pass < 2; pass++ {
		for i, base := range bases {
			if i > 0 {
				if err := os.MkdirAll(base, 0o700); err != nil {
					continue
				}
			}
			ram := sysproc.IsRAMBacked(base)
			if ram && pass == 0 {
				notes = append(notes, fmt.Sprintf("%s liegt im Arbeitsspeicher", sysproc.DisplayPath(base)))
				continue
			}
			if free := sysproc.FreeDiskBytes(base); free >= 0 && free < need {
				if pass == 0 {
					notes = append(notes, fmt.Sprintf("%s: frei %s", sysproc.DisplayPath(base), formatGB(free)))
				}
				continue
			}
			dir, err := os.MkdirTemp(base, "usenet-goup-*")
			if err != nil {
				continue
			}
			if len(notes) > 0 {
				a.broker.Log(fmt.Sprintf("Arbeitsordner: %s (%s).", sysproc.DisplayPath(base), strings.Join(notes, ", ")))
			}
			return dir, nil
		}
	}
	return "", newJobError(http.StatusInternalServerError, "DISKSPACE", fmt.Errorf("kein Arbeitsordner mit genug Platz (benötigt ca. %s): %s",
		formatGB(need), strings.Join(notes, "; ")))
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
// Cover: TMDB/TVMaze (nur mit Online-Metadaten), sonst Hinweis auf ein Bild im Quellordner.
func (a *app) resolveMetadata(req PipelineRequest, normSrc, origTitle, nfoContent string) (imdbID, synopsis, coverURL, coverPreview string) {
	isMinimal := normalizeTemplateCategory(req.TemplateType) != "movies_series"
	var metaCover string
	switch {
	case isMinimal:
	case req.OnlineMetadata:
		a.broker.Log("Online-Metadaten: frage TMDB / TVMaze / DuckDuckGo nach Cover, Beschreibung und IMDb-ID.")
		meta := a.fetchMeta(&http.Client{Timeout: 25 * time.Second}, origTitle, nfoContent, "", req.Language, a.broker.Log)
		imdbID, synopsis, metaCover = meta.IMDbID, meta.Synopsis, meta.CoverURL
	default:
		// Ohne Zustimmung keine Anfrage an Dritte: IMDb-ID nur aus NFO oder Release-Namen.
		imdbID = media.LocalIMDbID(origTitle, nfoContent)
	}
	// Ohne Online-Cover wird nur ein Bild im Quellordner gemeldet (kein Upload).
	coverURL, coverPreview = media.ResolveCoverForTemplate(normSrc, metaCover, a.broker.Log)
	return imdbID, synopsis, coverURL, coverPreview
}
