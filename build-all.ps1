# Build-Skript für Usenet GoUp (Windows, Linux, macOS)
# Bricht beim ersten Fehler ab und meldet klar, ob alles geklappt hat.
$ErrorActionPreference = "Stop"

$Version = "v1.35"
$LdFlags = "-s -w -X main.version=$Version"
$OutDir  = Join-Path $PSScriptRoot "dist"

# Führt einen go-Befehl aus und stoppt das Skript, wenn er fehlschlägt.
function Invoke-Go {
    & go @args
    if ($LASTEXITCODE -ne 0) { throw "Fehler bei: go $($args -join ' ')" }
}

# Alle Zielsysteme: Name der Datei, Betriebssystem, Prozessor
$Targets = @(
    @{ Name = "usenet-goup-windows-amd64.exe"; OS = "windows"; Arch = "amd64" },
    @{ Name = "usenet-goup-linux-amd64";       OS = "linux";   Arch = "amd64" },
    @{ Name = "usenet-goup-linux-arm64";       OS = "linux";   Arch = "arm64" },
    @{ Name = "usenet-goup-darwin-amd64";      OS = "darwin";  Arch = "amd64" },
    @{ Name = "usenet-goup-darwin-arm64";      OS = "darwin";  Arch = "arm64" }
)

try {
    # Alte Builds löschen, damit in dist nur frische Dateien liegen
    if (Test-Path $OutDir) { Remove-Item $OutDir -Recurse -Force }
    New-Item -ItemType Directory -Path $OutDir | Out-Null

    Write-Host "Formatiere und teste den Code ..." -ForegroundColor Cyan
    & gofmt -w .
    if ($LASTEXITCODE -ne 0) { throw "Fehler bei: gofmt -w ." }
    Invoke-Go vet ./...
    Invoke-Go test ./...

    foreach ($t in $Targets) {
        Write-Host "Baue $($t.Name) ..." -ForegroundColor Cyan
        $env:GOOS   = $t.OS
        $env:GOARCH = $t.Arch
        $flags = $LdFlags
        if ($t.OS -eq "windows") { $flags = "$LdFlags -H=windowsgui" }
        Invoke-Go vet ./...
        Invoke-Go build -trimpath "-ldflags=$flags" -o (Join-Path $OutDir $t.Name) .
    }

    Write-Host ""
    Write-Host "ERFOLG: Alle 5 Builds liegen in $OutDir" -ForegroundColor Green
}
catch {
    Write-Host ""
    Write-Host "ABGEBROCHEN: $_" -ForegroundColor Red
    exit 1
}
finally {
    # Zurück auf normal, damit spätere go-Befehle wieder für Windows bauen
    Remove-Item Env:GOOS   -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
}
