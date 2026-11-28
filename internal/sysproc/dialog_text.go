package sysproc

// dialogTitle: Fenstertitel des Dateidialogs in der Sprache der Oberfläche.
func dialogTitle(mode string, en bool) string {
	switch {
	case mode == "folder" || mode == "nzbdir":
		return pick(en, "Select folder", "Ordner auswählen")
	case mode == "nfo":
		return pick(en, "Select NFO file", "NFO-Datei auswählen")
	default:
		return pick(en, "Select file", "Datei auswählen")
	}
}

func allFilesLabel(en bool) string { return pick(en, "All files", "Alle Dateien") }

func pick(en bool, enText, deText string) string {
	if en {
		return enText
	}
	return deText
}
