package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"usenet-goup/internal/nntp"
)

const customTemplatesSubdir = "templates"

func normalizeTemplateCategory(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "games", "game":
		return "games"
	case "misc":
		return "misc"
	default:
		return "movies_series"
	}
}

// ensureCustomTemplatesDir legt <appDir>/templates an und schreibt bei Erststart
// für alle drei Bereiche ("movies_series.txt", "games.txt", "misc.txt") eine klar
// aufgebaute, direkt editierbare BB-Code-Vorlagendatei, falls noch keine vorhanden ist.
func ensureCustomTemplatesDir(appDir string, logFn func(string)) string {
	dir := filepath.Join(appDir, customTemplatesSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		if logFn != nil {
			logFn(fmt.Sprintf("Hinweis: Konnte Vorlagen-Ordner %s nicht anlegen: %v", dir, err))
		}
		return ""
	}

	starter := map[string]string{
		"movies_series.txt": `# Vorlage / Template: Movies & Series / Filme & Serien (movies_series.txt)
# Einfach die BB-Codes und Platzhalter nach Wunsch anordnen oder anpassen.
# Simply arrange or edit the BB-Codes and placeholders as you like.
#
# Platzhalter / Placeholders:
#   {{TITLE}}     -> Release-Titel / Release title
#   {{COVER_URL}} -> Cover-URL / Cover image URL
#   {{SYNOPSIS}}  -> Beschreibung / Plot summary
#   {{IMDB_ID}}   -> IMDb-ID (z.B. tt1234567)
#   {{EZPOST}}    -> [HIDE][EZPOST]Title|Header|Groups|Password|{Timestamp}[/EZPOST][/HIDE]
#   {{NFO}}       -> NFO-Inhalt / NFO content
#   {{MEDIAINFO}} -> MediaInfo-Inhalt / MediaInfo content
#
# Hinweis / Note: {{EZPOST}} immer in einer eigenen Zeile ohne " oder Style-Tags drumherum lassen.

[SIZE="3"][COLOR=Tomato][B]{{TITLE}}[/B][/COLOR][/SIZE]

[IMG]{{COVER_URL}}[/IMG]

{{SYNOPSIS}}

[imdb]{{IMDB_ID}}[/imdb]

{{EZPOST}}

[SIZE="2"][COLOR=DeepSkyBlue]NFO:[/COLOR][/SIZE]
[SPOILER][NFO]{{NFO}}[/NFO][/SPOILER]

[SIZE="2"][COLOR=DeepSkyBlue]MediaInfo:[/COLOR][/SIZE]
[SPOILER][NFO]{{MEDIAINFO}}[/NFO][/SPOILER]
`,
		"games.txt": `# Vorlage / Template: Games - PC / Console (games.txt)
# Einfach die BB-Codes und Platzhalter nach Wunsch anordnen oder anpassen.
# Simply arrange or edit the BB-Codes and placeholders as you like.
#
# Platzhalter / Placeholders:
#   {{TITLE}}     -> Release-Titel / Release title
#   {{COVER_URL}} -> Cover-URL / Cover image URL
#   {{EZPOST}}    -> [HIDE][EZPOST]Title|Header|Groups|Password|{Timestamp}[/EZPOST][/HIDE]
#   {{NFO}}       -> NFO-Inhalt / NFO content
#
# Hinweis / Note: {{EZPOST}} immer in einer eigenen Zeile ohne " oder Style-Tags drumherum lassen.

[SIZE="3"][COLOR=Tomato][B]{{TITLE}}[/B][/COLOR][/SIZE]

[IMG]{{COVER_URL}}[/IMG]

{{EZPOST}}

[SIZE="2"][COLOR=DeepSkyBlue]NFO:[/COLOR][/SIZE]
[SPOILER][NFO]{{NFO}}[/NFO][/SPOILER]
`,
		"misc.txt": `# Vorlage / Template: Misc - E-Book / Software / Audio (misc.txt)
# Einfach die BB-Codes und Platzhalter nach Wunsch anordnen oder anpassen.
# Simply arrange or edit the BB-Codes and placeholders as you like.
#
# Platzhalter / Placeholders:
#   {{TITLE}}     -> Release-Titel / Release title
#   {{COVER_URL}} -> Cover-URL / Cover image URL
#   {{EZPOST}}    -> [HIDE][EZPOST]Title|Header|Groups|Password|{Timestamp}[/EZPOST][/HIDE]
#   {{NFO}}       -> NFO-Inhalt / NFO content
#
# Hinweis / Note: {{EZPOST}} immer in einer eigenen Zeile ohne " oder Style-Tags drumherum lassen.

[SIZE="3"][COLOR=Tomato][B]{{TITLE}}[/B][/COLOR][/SIZE]

[IMG]{{COVER_URL}}[/IMG]

{{EZPOST}}

[SIZE="2"][COLOR=DeepSkyBlue]NFO:[/COLOR][/SIZE]
[SPOILER][NFO]{{NFO}}[/NFO][/SPOILER]
`,
	}

	createdAny := false
	for name, content := range starter {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if wErr := os.WriteFile(p, []byte(strings.ReplaceAll(content, "\n", "\r\n")), 0o600); wErr == nil {
				createdAny = true
			}
		}
	}
	if createdAny && logFn != nil {
		logFn(fmt.Sprintf("BB-Code-Vorlagen (movies_series.txt, games.txt, misc.txt) angelegt unter: %s", dir))
	}
	return dir
}

type templateVars struct {
	Title     string
	Header    string
	Groups    string
	Password  string
	Timestamp int64
	NFO       string
	MediaInfo string
	CoverURL  string
	IMDbID    string
	Synopsis  string
}

// cleanEmptyBBLines entfernt Zeilen oder BB-Code-Blöcke, deren eingesetzter Wert leer ist,
// damit z.B. [IMG][/IMG], [imdb][/imdb], [NFO][/NFO] oder [SPOILER][NFO][/NFO][/SPOILER]
// bei fehlendem Wert nicht leer stehen bleiben.
func cleanEmptyBBLines(text string) string {
	lines := strings.Split(text, "\n")
	var kept []string
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		trimmed := strings.TrimSpace(ln)

		// Mehrzeiliger [NFO]\n[/NFO]-Block mit leerem Inhalt
		if strings.EqualFold(trimmed, "[NFO]") && i+1 < len(lines) && strings.EqualFold(strings.TrimSpace(lines[i+1]), "[/NFO]") {
			if len(kept) > 0 && isSectionHeadingLine(strings.TrimSpace(kept[len(kept)-1])) {
				kept = kept[:len(kept)-1]
			}
			i++
			continue
		}

		// Einzelne BB-Code-Zeilen (inkl. [SPOILER][NFO][/NFO][/SPOILER]) mit leerem Inhalt entfernen
		if isEmptyTagLine(trimmed) {
			if len(kept) > 0 && isSectionHeadingLine(strings.TrimSpace(kept[len(kept)-1])) {
				kept = kept[:len(kept)-1]
			}
			continue
		}
		kept = append(kept, ln)
	}

	// Verwaiste Überschriften (NFO: / MediaInfo:) ohne nachfolgenden Spoiler-/NFO-Block entfernen
	var finalKept []string
	for i := 0; i < len(kept); i++ {
		curTrim := strings.TrimSpace(kept[i])
		if isSectionHeadingLine(curTrim) {
			hasBlock := false
			for j := i + 1; j < len(kept); j++ {
				nextTrim := strings.TrimSpace(kept[j])
				if nextTrim == "" {
					continue
				}
				low := strings.ToLower(nextTrim)
				if strings.Contains(low, "[spoiler]") || strings.Contains(low, "[nfo]") {
					hasBlock = true
				}
				break
			}
			if !hasBlock {
				continue
			}
		}
		finalKept = append(finalKept, kept[i])
	}

	out := strings.Join(finalKept, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out) + "\n"
}

func isEmptyTagLine(trimmed string) bool {
	for _, pair := range [][2]string{
		{"[IMG]", "[/IMG]"},
		{"[img]", "[/img]"},
		{"[imdb]", "[/imdb]"},
		{"[IMDB]", "[/IMDB]"},
		{"[NFO]", "[/NFO]"},
		{"[nfo]", "[/nfo]"},
		{"[SPOILER]", "[/SPOILER]"},
		{"[spoiler]", "[/spoiler]"},
	} {
		if strings.HasPrefix(trimmed, pair[0]) && strings.HasSuffix(trimmed, pair[1]) {
			inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, pair[0]), pair[1]))
			if inner == "" || strings.EqualFold(inner, "[NFO][/NFO]") {
				return true
			}
		}
	}
	return false
}

func isSectionHeadingLine(trimmed string) bool {
	upper := strings.ToUpper(trimmed)
	return strings.Contains(upper, "NFO:") || strings.Contains(upper, "MEDIAINFO:")
}

func renderCustomTemplate(raw string, v templateVars) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	var bodyLines []string
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		bodyLines = append(bodyLines, ln)
	}
	out := strings.Join(bodyLines, "\n")

	ezpost := fmt.Sprintf("[HIDE][EZPOST]%s|%s|%s|%s|{%d}[/EZPOST][/HIDE]",
		nntp.SanitizeEZPostField(v.Title),
		nntp.SanitizeEZPostField(v.Header),
		nntp.SanitizeEZPostField(v.Groups),
		v.Password,
		v.Timestamp)

	r := strings.NewReplacer(
		"{{EZPOST}}", ezpost,
		"{{TITLE}}", strings.TrimSpace(v.Title),
		"{{HEADER}}", strings.TrimSpace(v.Header),
		"{{GROUPS}}", strings.TrimSpace(v.Groups),
		"{{PASSWORD}}", v.Password,
		"{{TIMESTAMP}}", fmt.Sprintf("%d", v.Timestamp),
		"{{COVER_URL}}", strings.TrimSpace(v.CoverURL),
		"{{SYNOPSIS}}", strings.TrimSpace(v.Synopsis),
		"{{IMDB_ID}}", strings.TrimSpace(v.IMDbID),
		"{{NFO}}", strings.TrimSpace(v.NFO),
		"{{MEDIAINFO}}", strings.TrimSpace(v.MediaInfo),
	)
	out = r.Replace(out)
	return cleanEmptyBBLines(out)
}

func loadAndRenderCustomTemplate(appDir, templateType string, v templateVars, logFn func(string)) (string, bool) {
	cat := normalizeTemplateCategory(templateType)
	candidates := []string{filepath.Join(appDir, customTemplatesSubdir, cat+".txt")}
	if cat == "movies_series" {
		candidates = append(candidates, filepath.Join(appDir, customTemplatesSubdir, "movies.txt"))
	}
	for _, filePath := range candidates {
		rawBytes, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		rendered := renderCustomTemplate(string(rawBytes), v)
		if strings.TrimSpace(rendered) != "" {
			if logFn != nil {
				logFn(fmt.Sprintf("Eigene BB-Code-Vorlage verwendet: %s", filePath))
			}
			return rendered, true
		}
	}
	return "", false
}
