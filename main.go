package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"usenet-goup/internal/sysproc"
)

//go:embed web/*
var webFS embed.FS

// version wird beim Build per -ldflags "-X main.version=..." überschrieben.
var version = "v1.38.0"

const listenAddr = "127.0.0.1:8095"

func main() {
	appDir, legacyDir := getDataDir()
	a := newApp(appDir, legacyDir)
	a.crashLog.StartFreezeWatchdog()
	a.crashLog.AppendEvent("START", fmt.Sprintf("Usenet GoUp %s gestartet (os=%s, arch=%s, pid=%d)", version, runtime.GOOS, runtime.GOARCH, os.Getpid()))

	defer func() {
		if r := recover(); r != nil {
			a.crashLog.RecordPanic("main", r)
		}
	}()

	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		a.crashLog.RecordPanic("embed-web", err)
		log.Fatal(err)
	}

	url := "http://" + listenAddr
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		// Port belegt: läuft bereits eine Instanz, nur den Browser darauf öffnen statt still zu sterben.
		probe := &http.Client{Timeout: 1500 * time.Millisecond}
		if resp, pErr := probe.Get(url + "/api/system-info"); pErr == nil {
			resp.Body.Close()
			a.crashLog.AppendEvent("ALREADY-RUNNING", "Port belegt, öffne bestehende Instanz")
			openBrowser(url)
			return
		}
		a.crashLog.AppendEvent("SERVER-FATAL", err.Error())
		log.Fatal(err)
	}

	fmt.Printf("Usenet GoUp %s gestartet unter %s (Config: %s | CrashLog: %s)\n", version, url, a.configPath, a.crashLog.Path())
	if n := a.cleanupOnStart(); n > 0 {
		fmt.Printf("%d verwaiste Arbeitsordner vom letzten Lauf entfernt.\n", n)
	}
	a.cleanOnSignal()
	go openBrowser(url)

	srv := &http.Server{
		Handler:           a.routes(webRoot),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.Serve(ln); err != nil {
		a.crashLog.AppendEvent("SERVER-FATAL", err.Error())
		log.Fatal(err)
	}
}

func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		sysproc.HideWindow(cmd)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
