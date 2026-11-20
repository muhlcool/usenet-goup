package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"usenet-goup/internal/archiver"
	"usenet-goup/internal/config"
	"usenet-goup/internal/crypto"
	"usenet-goup/internal/media"
	"usenet-goup/internal/nntp"
	"usenet-goup/internal/par2"
	"usenet-goup/internal/sysproc"
)

// app bündelt den Zustand des lokalen Servers. Die Handler sind Methoden, damit sie
// ohne laufenden Server per httptest geprüft werden können.
type app struct {
	appDir   string
	crashLog *sysproc.CrashLogger
	broker   *LogBroker
	dryCache *DryRunCache
	jobs     *jobControl

	cfgMu          sync.Mutex
	configPath     string
	readConfigPath string // kann auf eine alte config.json neben der Programmdatei zeigen (Migration)

	// Austauschbar für Tests.
	upload    func(ctx context.Context, cfg nntp.ServerConfig, files []string, obfuscate bool, headerLen int, logFn func(string), progressFn func(sysproc.ProgressStats), heartbeatFn func()) (*nntp.UploadResult, error)
	fetchMeta func(client *http.Client, releaseName, nfoContent, tmdbKey, lang string, logFn func(string)) media.MediaMeta
	exit      func(code int)
}

func newApp(appDir, legacyDir string) *app {
	crypto.SetKeyDir(appDir)
	crashLog := sysproc.NewCrashLogger(appDir)
	a := &app{
		appDir:     appDir,
		crashLog:   crashLog,
		broker:     NewLogBroker(crashLog),
		dryCache:   &DryRunCache{},
		jobs:       &jobControl{},
		configPath: filepath.Join(appDir, "config.json"),
		upload:     nntp.UploadFiles,
		fetchMeta:  media.FetchReleaseMediaMeta,
		exit:       os.Exit,
	}
	a.readConfigPath = a.configPath
	if _, err := os.Stat(a.configPath); err != nil && legacyDir != "" {
		if _, lErr := os.Stat(filepath.Join(legacyDir, "config.json")); lErr == nil {
			a.readConfigPath = filepath.Join(legacyDir, "config.json")
		}
	}
	_ = ensureCustomTemplatesDir(appDir, nil)
	return a
}

func (a *app) routes(webRoot fs.FS) http.Handler {
	mux := http.NewServeMux()
	fileServer := http.FileServer(http.FS(webRoot))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
	mux.Handle("/api/logs", a.broker)
	mux.HandleFunc("/api/system-info", a.handleSystemInfo)
	mux.HandleFunc("/api/diagnostics", a.handleDiagnostics)
	mux.HandleFunc("/api/cache/clear", a.handleCacheClear)
	mux.HandleFunc("/api/cancel", a.handleCancel)
	mux.HandleFunc("/api/shutdown", a.handleShutdown)
	mux.HandleFunc("/api/engines", func(w http.ResponseWriter, r *http.Request) { sendJSON(w, http.StatusOK, archiver.DetectEngines()) })
	mux.HandleFunc("/api/par2-engines", func(w http.ResponseWriter, r *http.Request) { sendJSON(w, http.StatusOK, par2.DetectPar2Engines()) })
	mux.HandleFunc("/api/clipboard", a.handleClipboard)
	mux.HandleFunc("/api/browse", a.handleBrowse)
	mux.HandleFunc("/api/test-connection", a.handleTestConnection)
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/run", a.handleRun)
	return securityMiddleware(mux)
}

// --- Konfiguration ---------------------------------------------------------

func (a *app) loadConfig() (config.AppConfig, error) {
	a.cfgMu.Lock()
	path := a.readConfigPath
	a.cfgMu.Unlock()

	cfg := config.Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, nil // noch keine Konfiguration: Werkseinstellungen
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	for _, f := range cfg.SecretFields() {
		dec, err := crypto.DecryptPassword(*f)
		if err != nil {
			// Nie den verschlüsselten String an die UI geben: er würde sonst als Passwort benutzt.
			a.broker.Log("Hinweis: Ein gespeichertes Passwort/API-Key konnte nicht entschlüsselt werden — bitte neu eingeben und speichern.")
			a.crashLog.AppendEvent("CONFIG-DECRYPT", err.Error())
			dec = ""
		}
		*f = dec
	}
	return cfg, nil
}

func (a *app) saveConfig(cfg config.AppConfig) error {
	for _, f := range cfg.SecretFields() {
		enc, err := crypto.EncryptPassword(*f)
		if err != nil {
			return err
		}
		*f = enc
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if err := os.WriteFile(a.configPath, data, 0o600); err != nil {
		return err
	}
	a.readConfigPath = a.configPath
	return nil
}

func (a *app) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := a.loadConfig()
		if err != nil {
			sendJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sendJSON(w, http.StatusOK, cfg)
	case http.MethodPost:
		var cfg config.AppConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			sendJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.saveConfig(cfg); err != nil {
			sendJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sendJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": a.configPath})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- kleine Handler --------------------------------------------------------

func (a *app) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, http.StatusOK, map[string]string{
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"appDir":       a.appDir,
		"crashLogPath": a.crashLog.Path(),
		"version":      version,
	})
}

func (a *app) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var diag struct {
		Kind   string `json:"kind"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&diag); err == nil && diag.Detail != "" {
		kind := strings.TrimSpace(diag.Kind)
		if kind == "" {
			kind = "CLIENT-DIAG"
		}
		a.crashLog.AppendEvent(kind, diag.Detail)
	}
	sendJSON(w, http.StatusOK, map[string]string{"status": "recorded", "logPath": a.crashLog.Path()})
}

func (a *app) handleCacheClear(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	a.dryCache.Clear(true)
	sendJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

func (a *app) handleCancel(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	aborted := a.jobs.abort()
	if aborted {
		a.broker.Log("Abbruch angefordert — beende laufende Schritte...")
	}
	sendJSON(w, http.StatusOK, map[string]bool{"cancelled": aborted})
}

func (a *app) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	a.crashLog.SetState("shutting_down")
	a.crashLog.AppendEvent("SHUTDOWN", "Sauberes Beenden über UI angefordert")
	var cfg config.AppConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err == nil && cfg.Host != "" {
		if err := a.saveConfig(cfg); err != nil {
			a.crashLog.AppendEvent("CONFIG-ERROR", err.Error())
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	sendJSON(w, http.StatusOK, map[string]string{"status": "shutting_down"})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		// Laufenden Job abbrechen, damit rar/7z/parpar nicht als Waisen weiterlaufen.
		if a.jobs.abort() {
			a.jobs.waitIdle(5 * time.Second)
		}
		a.dryCache.Clear(true)
		time.Sleep(80 * time.Millisecond)
		a.exit(0)
	}()
}

func (a *app) handleClipboard(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024*1024)).Decode(&payload); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if err := sysproc.CopyTextToClipboard(payload.Text); err != nil {
		sendJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *app) handleBrowse(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	selected, err := sysproc.OpenDialog(mode)
	if err != nil {
		a.crashLog.AppendEvent("DIALOG-ERROR", "mode="+mode+" err="+err.Error())
		sendJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	sendJSON(w, http.StatusOK, map[string]string{"path": selected})
}

func (a *app) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var cfg nntp.ServerConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		sendJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, nntp.TestConnection(cfg))
}
