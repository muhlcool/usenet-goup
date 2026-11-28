package main

import (
	"regexp"
	"sort"
	"strings"
)

// Englische Fassung aller Meldungen, die Oberfläche und Log zeigen. Die Meldungen entstehen im
// Code auf Deutsch; toEN übersetzt die fertige Zeile über ihr Format. Platzhalter (%s, %d, ...)
// stehen im Englischen in derselben Reihenfolge, ihr Inhalt wird selbst wieder übersetzt
// (z. B. ein deutscher Fehler innerhalb von "Abbruch (%s): %s").
// Bewusst eine Liste aus Paaren statt einer Map: gofmt richtet Map-Werte je nach Go-Version
// unterschiedlich aus, Paare werden nie ausgerichtet.
// TestAllMessagesHaveEnglish stellt sicher, dass keine neue deutsche Meldung fehlt.
var enFormats = [][2]string{
	// Job-Ablauf
	{"=== Starte Job für: %s ===", "=== Starting job for: %s ==="},
	{"=== Nur hochladen: %d Dateien unverändert aus %s ===", "=== Upload only: %d files unchanged from %s ==="},
	{"=== Übernehme verifiziertes Paket (%d Dateien) ohne erneutes Verpacken ===", "=== Reusing verified package (%d files) without repacking ==="},
	{"=== ERFOLG! NZB gespeichert unter: %s ===", "=== SUCCESS! NZB saved to: %s ==="},
	{"Abbruch (%s): %s", "Aborted (%s): %s"},
	{"Abbruch angefordert — beende laufende Schritte...", "Cancellation requested, stopping running steps..."},
	{"Job abgebrochen.", "Job cancelled."},
	{"Es läuft bereits ein Job. Bitte warten oder abbrechen.", "A job is already running. Please wait or cancel it."},
	{"Es läuft gerade ein Job. Cache und Temp-Ordner werden danach bereinigt.", "A job is running. Cache and temp folder will be cleaned up afterwards."},
	{"Unerwarteter Fehler (Details in %s): %v", "Unexpected error (details in %s): %v"},
	{"Anfrage zu groß", "Request too large"},
	{"Temp-Ordner bereinigt: %d alte usenet-goup-* Verzeichnisse entfernt.", "Temp folder cleaned: removed %d old usenet-goup-* folders."},
	{"Arbeitsordner: %s (%s).", "Work folder: %s (%s)."},
	{"kein Arbeitsordner mit genug Platz (benötigt ca. %s): %s", "no work folder with enough space (needs about %s): %s"},
	{"Hinweis: Ein gespeichertes Passwort/API-Key konnte nicht entschlüsselt werden — bitte neu eingeben und speichern.", "Note: A saved password could not be decrypted, please enter it again and save."},
	{"Hinweis: Konnte Vorlagen-Ordner %s nicht anlegen: %v", "Note: Could not create template folder %s: %v"},

	// Eingaben
	{`Das %s Passwort darf keine Zeichen [ ] " | enthalten.`, `The %s password must not contain [ ] " | characters.`},
	{"Das %s Passwort ist zu lang (höchstens %d Zeichen).", "The %s password is too long (at most %d characters)."},
	{"Das %s Passwort darf keine Steuerzeichen enthalten.", "The %s password must not contain control characters."},
	{"manuelle", "manual"},
	{"vorhandene", "existing"},
	{"Posting-Vorlage abgeschaltet: nur die NZB wurde erstellt.", "Posting template switched off: only the NZB was created."},
	{"Kein Quellpfad angegeben.", "No source path given."},
	{"Quellpfad nicht verwendbar: %s (%v)", "Source path not usable: %s (%v)"},
	{"Ausgabeordner ist kein Ordner: %s", "Output folder is not a folder: %s"},
	{"Ausgabeordner nicht verwendbar: %s (%v)", "Output folder not usable: %s (%v)"},
	{"Kein NNTP-Host konfiguriert.", "No NNTP host configured."},
	{"Kein NNTP-Host angegeben.", "No NNTP host given."},
	{"NFO-Datei erkannt und dekodiert: %s", "NFO file detected and decoded: %s"},
	{"Der Quellpfad ist eine Verknüpfung (Symlink). Bitte das eigentliche Ziel auswählen.", "The source path is a link (symlink). Please select the actual target."},
	{"Die Datei ist leer.", "The file is empty."},
	{"Der Quellpfad ist weder Datei noch Ordner.", "The source path is neither a file nor a folder."},
	{`Der Ordner enthält Unterordner (%s). "Nur hochladen" lädt nur Dateien direkt im Ordner hoch, weil die NZB keine Ordner kennt. Bitte Unterordner einzeln hochladen oder "Packen und hochladen" verwenden.`, `The folder contains subfolders (%s). "Upload only" uploads only files directly inside the folder, because an NZB has no folders. Please upload subfolders one by one or use "Pack and upload".`},
	{"Im Ordner liegen keine Dateien zum Hochladen.", "The folder contains no files to upload."},
	{`Der Ordner enthält %d Dateien, "Nur hochladen" erlaubt höchstens %d. Bitte aufteilen oder "Packen und hochladen" verwenden.`, `The folder contains %d files, "Upload only" allows at most %d. Please split it up or use "Pack and upload".`},
	{"Übersprungen: %s", "Skipped: %s"},
	{"kein normaler Ordner und keine normale Datei (Gerät, Pipe oder Systemordner)", "not a regular folder or file (device, pipe or system folder)"},
	{"Datei zu groß (%d Bytes, erlaubt %d)", "File too large (%d bytes, allowed %d)"},
	{"Datei zu groß (erlaubt %d Bytes)", "File too large (allowed %d bytes)"},

	// Packen
	{"Obfuscation aktiv: Tarnname '%s' (Länge %d) generiert.", "Obfuscation active: random name '%s' (length %d) generated."},
	{"Starte RAR5-Erstellung (%.1f MB | %d MB Parts)...", "Starting RAR5 creation (%.1f MB | %d MB parts)..."},
	{"Starte 7-Zip-Erstellung (%.1f MB | %d MB Parts)...", "Starting 7-Zip creation (%.1f MB | %d MB parts)..."},
	{"Starte natives Go-Splitting (%.1f MB | %d MB Parts, Originalname für PAR2-Rename gesichert)...", "Starting native Go splitting (%.1f MB | %d MB parts, original name kept for PAR2 rename)..."},
	{"Hinweis: Natives Go-Splitting unterstützt keine Passwortverschlüsselung (Upload erfolgt unverschlüsselt).", "Note: Native Go splitting does not support passwords (upload is unencrypted)."},
	{"natives Splitting unterstützt nur Einzeldateien (für Ordner bitte RAR5 oder 7-Zip wählen)", "native splitting supports single files only (choose RAR5 or 7-Zip for folders)"},
	{"Erfolgreich verpackt: %d Teil-Dateien erstellt.", "Packed successfully: %d part files created."},
	{"Packer-Fehler: %v | Output: %s", "Packer error: %v | Output: %s"},
	{"Packer meldete Erfolg, hat aber keine Dateien erzeugt (Programm defekt oder falsche Version?)", "Packer reported success but created no files (broken program or wrong version?)"},
	{"Verpacken abgebrochen", "Packing cancelled"},
	{"RAR Binary nicht gefunden", "RAR program not found"},
	{"7-Zip Binary nicht gefunden", "7-Zip program not found"},
	{"ungültige Part-Größe: %d MB", "invalid part size: %d MB"},
	{"ungültige Part-Größe", "invalid part size"},
	{"konnte Ausgabeverzeichnis nicht erstellen: %w", "could not create output folder: %w"},
	{"Zufälliges Archiv-Passwort generiert (%d Zeichen): %s", "Random archive password generated (%d characters): %s"},
	{"Zufälliges Archiv-Passwort generiert (%d Zeichen, nur in der Oberfläche angezeigt).", "Random archive password generated (%d characters, shown in the interface only)."},
	{"Verwendetes Archiv-Passwort: (nur in der Oberfläche angezeigt)", "Archive password used: (shown in the interface only)"},
	{"Dry-Run abgeschlossen! %d Dateien liegen bereit in: %s", "Dry run finished! %d files ready in: %s"},
	{"Dry-Run abgeschlossen! %d Dateien werden unverändert hochgeladen (nichts zu erstellen).", "Dry run finished! %d files will be uploaded unchanged (nothing to create)."},
	{"Hinweis: Paket-Cache ungültig (%s) — verpacke neu...", "Note: Package cache invalid (%s), packing again..."},
	{"Parameter oder Quelldatei wurden geändert", "settings or source file changed"},
	{"Temp-Datei fehlt oder wurde verändert (%s)", "temp file missing or changed (%s)"},
	{"Hinweis: Das fertige Paket bleibt erhalten, ein erneuter Start lädt ohne Neupacken hoch.", "Note: The finished package is kept, starting again uploads without repacking."},

	// PAR2
	{"Keine PAR2-Engine gefunden (parpar / par2 fehlt)", "No PAR2 engine found (parpar / par2 missing)"},
	{"keine PAR2-Engine installiert (bitte parpar oder par2 im Programmordner ablegen)", "no PAR2 engine installed (place parpar or par2 in the program folder)"},
	{"PAR2-Erstellung übersprungen (Redundanz = 0% bzw. abgeschaltet).", "PAR2 creation skipped (redundancy 0% or switched off)."},
	{"ParPar Binary (parpar.exe) nicht gefunden", "ParPar program (parpar.exe) not found"},
	{"par2 Binary (par2.exe) nicht gefunden", "par2 program (par2.exe) not found"},
	{"Starte ParPar (%d%% Redundanz, %s Slices)...", "Starting ParPar (%d%% redundancy, %s slices)..."},
	{"Starte par2cmdline-turbo (%d%% Redundanz)...", "Starting par2cmdline-turbo (%d%% redundancy)..."},
	{"PAR2-Fehler: %v | Output: %s", "PAR2 error: %v | Output: %s"},
	{"PAR2 abgebrochen", "PAR2 cancelled"},
	{"PAR2-Engine meldete Erfolg, hat aber keine .par2-Dateien erzeugt (Programm defekt oder falsche Version?)", "PAR2 engine reported success but created no .par2 files (broken program or wrong version?)"},
	{"Dateinamen werden verschleiert hochgeladen (Originale bleiben unverändert, PAR2 stellt die Namen wieder her).", "Filenames are uploaded obfuscated (originals stay unchanged, PAR2 restores the names)."},
	{"Hinweis: Ohne eigene PAR2 werden nur die Subject-Header verschleiert, die Dateinamen bleiben original.", "Note: Without PAR2 only the subject headers are obfuscated, filenames stay original."},

	// Upload
	{"Zufälliger From-Header aktiv: %s", "Random From header active: %s"},
	{"Starte Streaming-Upload: %d Dateien | %d Segmente | %.2f MB | Port %d (%s) | %d Verbindungen...", "Starting streaming upload: %d files | %d segments | %.2f MB | port %d (%s) | %d connections..."},
	{"%d Segmente konnten nicht hochgeladen werden (kein Fortschritt mit dem Provider)", "%d segments could not be uploaded (no progress with the provider)"},
	{"Hinweis: %d Segmente offen — neue Runde mit %d Verbindungen.", "Note: %d segments left, new round with %d connections."},
	{"Hinweis: Provider-Verbindungslimit erreicht (%v) — reduziere auf %d aktive Verbindungen.", "Note: Provider connection limit reached (%v), reducing to %d active connections."},
	{"Worker %d Fehler bei %s (Part %d): %w", "Worker %d error on %s (part %d): %w"},
	{"Worker %d Lesefehler bei %s (Part %d): %w", "Worker %d read error on %s (part %d): %w"},
	{"Upload abgebrochen: %w", "Upload cancelled: %w"},
	{"keine Dateien zum Hochladen", "no files to upload"},
	{"interner Fehler: %d Upload-Namen für %d Dateien", "internal error: %d upload names for %d files"},
	{"interner Fehler: Segment %d von %s fehlt", "internal error: segment %d of %s missing"},
	{"POST nicht erlaubt (%d): %s", "POST not allowed (%d): %s"},
	{"Serverantwort zu lang", "server reply too long"},
	{"Artikel bereits vorhanden (Duplikat)", "article already exists (duplicate)"},
	{"Alle %d angeforderten Verbindungen erfolgreich akzeptiert.", "All %d requested connections accepted."},
	{"Provider erlaubt maximal %d von %d angeforderten Verbindungen (%s).", "Provider allows at most %d of %d requested connections (%s)."},
	{"NZB gespeichert: %s", "NZB saved: %s"},
	{"NZB-Fehler im Zielordner (%v) — NZB gerettet nach: %s", "NZB error in output folder (%v), NZB saved instead to: %s"},
	{"Posting-Vorlage gespeichert unter: %s", "Posting template saved to: %s"},
	{"Warnung bei Posting-Vorlage: %v", "Posting template warning: %v"},

	// Prüfung nach dem Upload
	{"Prüfe, ob alle %d Teile auf dem Server angekommen sind ...", "Checking that all %d parts arrived on the server ..."},
	{"Prüfung OK: alle %d Teile sind auf dem Server.", "Check OK: all %d parts are on the server."},
	{"Prüfung: %d von %d Teilen noch nicht auf dem Server, erneute Prüfung in %s ...", "Check: %d of %d parts not on the server yet, checking again in %s ..."},
	{"Prüfung: Verbindung fehlgeschlagen: %w", "Check: connection failed: %w"},
	{"Prüfung: ungültige Message-ID %q", "Check: invalid message ID %q"},
	{"Prüfung: unerwartete Serverantwort auf STAT (%d): %s", "Check: unexpected server reply to STAT (%d): %s"},
	{"Hinweis: Prüfung nach dem Upload nicht möglich (%v). Die NZB wurde trotzdem gespeichert.", "Note: Check after upload not possible (%v). The NZB was saved anyway."},
	{"WARNUNG: %d von %d Teilen fehlen auf dem Server. Die NZB ist so unvollständig, bitte den Upload wiederholen (das Paket bleibt erhalten).", "WARNING: %d of %d parts are missing on the server. The NZB is incomplete, please repeat the upload (the package is kept)."},

	// Medien und Metadaten
	{"MediaInfo über CLI ausgelesen: %s", "MediaInfo read via CLI: %s"},
	{"Media-Streams über ffprobe ausgelesen: %s", "Media streams read via ffprobe: %s"},
	{"Basis-MediaInfo (Go-Fallback) erstellt für: %s", "Basic MediaInfo (built-in parser) created for: %s"},
	{"IMDb-ID %s für '%s' über Websuche ermittelt.", "IMDb ID %s for '%s' found via web search."},
	{"Cover & Metadaten (Priorität 1: TMDB IMDb %s) gefunden: %s", "Cover & metadata (priority 1: TMDB IMDb %s) found: %s"},
	{"Cover & Metadaten (Priorität 1: TMDB-Suche '%s') gefunden: %s", "Cover & metadata (priority 1: TMDB search '%s') found: %s"},
	{"Cover (Fallback TVMaze) gefunden: %s", "Cover (fallback TVMaze) found: %s"},
	{"Lokales Cover erkannt (%s), kein automatischer Upload: bitte manuell hochladen und Link in die Vorlage einsetzen.", "Local cover found (%s), no automatic upload: please upload it yourself and insert the link into the template."},
	{"Online-Metadaten: frage TMDB / TVMaze / DuckDuckGo nach Cover, Beschreibung und IMDb-ID.", "Online metadata: asking TMDB / TVMaze / DuckDuckGo for cover, description and IMDb ID."},

	// System
	{"Schlüsselverzeichnis nicht gesetzt", "key folder not set"},
	{"ungültiger Geheimtext", "invalid encrypted value"},
	{"Passwort konnte nicht entschlüsselt werden (Schlüssel geändert?): %w", "password could not be decrypted (key changed?): %w"},
	{"kein Zwischenablage-Programm gefunden (wl-copy, xclip oder xsel installieren)", "no clipboard program found (install wl-copy, xclip or xsel)"},
	{"Zwischenablage ist belegt", "clipboard is busy"},
	{"kein Dateidialog gefunden (zenity oder kdialog installieren) — Pfad bitte manuell eintragen", "no file dialog found (install zenity or kdialog), please enter the path manually"},

	// Nachgetragen (über die Aufruf-Suche im Test gefunden)
	{"BB-Code-Vorlagen (movies_series.txt, games.txt, misc.txt) angelegt unter: %s", "BB code templates (movies_series.txt, games.txt, misc.txt) created in: %s"},
	{"Eigene BB-Code-Vorlage verwendet: %s", "Custom BB code template used: %s"},
	{"unbekannte Engine: %s", "unknown engine: %s"},
	{"MediaInfo nativ in Go ausgelesen: %s", "MediaInfo read by the built-in parser: %s"},
	{"NNTP Greeting fehlgeschlagen (%d): %s %v", "NNTP greeting failed (%d): %s %v"},
	{"NNTP Auth fehlgeschlagen (%d): %s", "NNTP login failed (%d): %s"},
	{"NNTP Auth abgelehnt (%d): %s", "NNTP login rejected (%d): %s"},
	{"Artikel abgelehnt (%d): %s", "article rejected (%d): %s"},
	{"Fortschritt: %d/%d Segmente (%.1f%%) | Netto: %.2f MB/s | Brutto: %.2f MB/s (%.0f Mbit/s)", "Progress: %d/%d segments (%.1f%%) | net: %.2f MB/s | gross: %.2f MB/s (%.0f Mbit/s)"},
	{"PAR2 abgeschlossen: %d Reparaturdateien erzeugt.", "PAR2 finished: %d recovery files created."},
	{"Startfehler %s: %w", "start error %s: %w"},
	{"Verwendetes Archiv-Passwort: %s", "Archive password used: %s"},

	// Fortschrittsanzeige
	{"Phase 1/3: Archiv-Verpackung", "Phase 1/3: Packing"},
	{"Phase 1/3: RAR5-Verpackung", "Phase 1/3: RAR5 packing"},
	{"Phase 1/3: 7-Zip-Verpackung", "Phase 1/3: 7-Zip packing"},
	{"Phase 1/3: Natives Go-Splitting", "Phase 1/3: Native Go splitting"},
	{"Phase 2/3: PAR2-Erstellung", "Phase 2/3: PAR2 creation"},
	{"Phase 2/3: ParPar-Berechnung", "Phase 2/3: ParPar calculation"},
	{"Phase 2/3: par2-Berechnung", "Phase 2/3: par2 calculation"},
	{"Phase 3/3: NNTP-Upload", "Phase 3/3: NNTP upload"},
	{"Phase 3/3: NNTP-Upload abgeschlossen", "Phase 3/3: NNTP upload finished"},
	{"Phase 3/3: NNTP-Upload abgebrochen", "Phase 3/3: NNTP upload stopped"},
}

// enPhrases: Teilstücke, die in zusammengesetzten Meldungen stecken (Listen, Klammern).
var enPhrases = strings.NewReplacer(
	" (Verknüpfung)", " (link)",
	" (leer)", " (empty)",
	"Unverschlüsselt (Klartext)", "Unencrypted (plain text)",
	" liegt im Arbeitsspeicher", " is in RAM",
	": frei ", ": free ",
)

type enRule struct {
	re  *regexp.Regexp
	out []string // Englisch, zerlegt: Text, Platzhalter, Text, ...
}

var (
	fmtVerb = regexp.MustCompile(`%%|%[-+# 0]*\d*(?:\.\d+)?[sdvqwfx]`)
	enRules = buildEnRules()
)

// splitFormat zerlegt ein Format in Text und Platzhalter; %% wird zu einem Prozentzeichen.
func splitFormat(f string) (texts []string, verbs int) {
	last := 0
	cur := ""
	for _, m := range fmtVerb.FindAllStringIndex(f, -1) {
		cur += f[last:m[0]]
		if f[m[0]:m[1]] == "%%" {
			cur += "%"
		} else {
			texts = append(texts, cur)
			cur = ""
			verbs++
		}
		last = m[1]
	}
	return append(texts, cur+f[last:]), verbs
}

func buildEnRules() []enRule {
	pairs := append([][2]string(nil), enFormats...)
	// Längere (spezifischere) Formate zuerst.
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i][0]) > len(pairs[j][0]) })
	rules := make([]enRule, 0, len(pairs))
	for _, pair := range pairs {
		k := pair[0]
		texts, _ := splitFormat(k)
		var pat strings.Builder
		pat.WriteString(`(?s)^`)
		for i, t := range texts {
			if i > 0 {
				pat.WriteString(`(.*?)`)
			}
			pat.WriteString(regexp.QuoteMeta(t))
		}
		pat.WriteString(`$`)
		out, _ := splitFormat(pair[1])
		rules = append(rules, enRule{re: regexp.MustCompile(pat.String()), out: out})
	}
	return rules
}

// toEN übersetzt eine fertige deutsche Meldung; Unbekanntes bleibt unverändert.
func toEN(msg string) string { return translateEN(msg, 0) }

func translateEN(msg string, depth int) string {
	if msg == "" || depth > 3 {
		return msg
	}
	for _, r := range enRules {
		m := r.re.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		var b strings.Builder
		for i, t := range r.out {
			if i > 0 && i < len(m) {
				b.WriteString(translateEN(m[i], depth+1))
			}
			b.WriteString(t)
		}
		return enPhrases.Replace(b.String())
	}
	return enPhrases.Replace(msg)
}
