// Package config enthält die gespeicherten Einstellungen (config.json) und die Job-Parameter,
// die Konfiguration und Upload-Auftrag gemeinsam haben.
package config

import (
	"usenet-goup/internal/archiver"
	"usenet-goup/internal/nntp"
	"usenet-goup/internal/par2"
)

// JobSettings: Pack-, PAR2-, Cover- und Ausgabe-Einstellungen. Wird in AppConfig (gespeichert)
// und im Upload-Auftrag eingebettet; das JSON-Format bleibt dadurch flach wie bisher.
type JobSettings struct {
	NzbOutDir         string              `json:"nzbOutDir"`
	Engine            archiver.EngineType `json:"engine"`
	PartSizeMB        int                 `json:"partSizeMB"`
	Par2Engine        par2.Par2EngineType `json:"par2Engine"`
	Par2Pct           int                 `json:"par2Pct"`
	AutoPassword      bool                `json:"autoPassword"`
	PasswordLength    int                 `json:"passwordLength"`
	ArchivePassword   string              `json:"archivePassword"`
	Obfuscate         bool                `json:"obfuscate"`
	ObfuscationLength int                 `json:"obfuscationLength"`
	IncludeMediaInfo  bool                `json:"includeMediaInfo"`
	ImageHoster       string              `json:"imageHoster"`
	ImgbbApiKey       string              `json:"imgbbApiKey"`
	AutoSearchCover   bool                `json:"autoSearchCover"`
	TmdbApiKey        string              `json:"tmdbApiKey"`
	Language          string              `json:"language"`
	TemplateType      string              `json:"templateType"`
}

// AppConfig ist der Inhalt von config.json.
type AppConfig struct {
	nntp.ServerConfig
	JobSettings
}

// Default liefert die Werkseinstellungen.
func Default() AppConfig {
	return AppConfig{
		ServerConfig: nntp.ServerConfig{
			Port:            563,
			Connections:     40,
			Newsgroup:       "alt.binaries.misc",
			Poster:          "poster <poster@example.invalid>",
			RandomizePoster: true,
			MaxRetries:      3,
		},
		JobSettings: JobSettings{
			PartSizeMB:        50,
			Par2Pct:           10,
			AutoPassword:      true,
			PasswordLength:    24,
			Obfuscate:         true,
			ObfuscationLength: 24,
			IncludeMediaInfo:  true,
			ImageHoster:       "pixhost",
			AutoSearchCover:   true,
			Language:          "de",
			TemplateType:      "movies_series",
		},
	}
}

// SecretFields: alle Werte, die nur verschlüsselt in config.json stehen.
func (c *AppConfig) SecretFields() []*string {
	return []*string{&c.Password, &c.ArchivePassword, &c.ImgbbApiKey, &c.TmdbApiKey}
}

func clampInt(v, lo, hi, def int) int {
	if v == 0 {
		return def
	}
	return max(lo, min(v, hi))
}

// Sanitize begrenzt alle Zahlen serverseitig (das min/max im HTML schützt nicht).
func (j *JobSettings) Sanitize() {
	j.PartSizeMB = clampInt(j.PartSizeMB, 1, 10000, 50)
	j.Par2Pct = max(0, min(j.Par2Pct, 100))
	j.PasswordLength = clampInt(j.PasswordLength, 8, 128, 24)
	j.ObfuscationLength = clampInt(j.ObfuscationLength, 8, 128, 24)
	switch j.TemplateType {
	case "movies_series", "games", "misc":
	case "movies", "standard":
		j.TemplateType = "movies_series"
	case "game":
		j.TemplateType = "games"
	default:
		j.TemplateType = "movies_series"
	}
}

// SanitizeServer begrenzt Verbindungen und Wiederholungen.
func SanitizeServer(s *nntp.ServerConfig) {
	s.Connections = clampInt(s.Connections, 1, nntp.MaxConnections, 20)
	s.MaxRetries = clampInt(s.MaxRetries, 1, 20, 3)
}
