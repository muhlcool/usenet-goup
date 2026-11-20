package nntp

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"usenet-goup/internal/sysproc"
)

type NZB struct {
	XMLName xml.Name  `xml:"nzb"`
	Xmlns   string    `xml:"xmlns,attr"`
	Head    *NZBHead  `xml:"head,omitempty"`
	Files   []NZBFile `xml:"file"`
}

type NZBHead struct {
	Meta []NZBMeta `xml:"meta"`
}

type NZBMeta struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type NZBFile struct {
	Poster   string       `xml:"poster,attr"`
	Date     int64        `xml:"date,attr"`
	Subject  string       `xml:"subject,attr"`
	Groups   []string     `xml:"groups>group"`
	Segments []NZBSegment `xml:"segments>segment"`
}

type NZBSegment struct {
	Bytes  int    `xml:"bytes,attr"`
	Number int    `xml:"number,attr"`
	MsgID  string `xml:",chardata"`
}

func sanitizeXMLText(s string) string {
	valid := strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	b.Grow(len(valid))
	for _, r := range valid {
		if r == 0x09 || r == 0x0A || r == 0x0D ||
			(r >= 0x20 && r <= 0xD7FF) ||
			(r >= 0xE000 && r <= 0xFFFD) ||
			(r >= 0x10000 && r <= 0x10FFFF) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func WriteNZB(outputPath string, title string, password string, files []NZBFile) error {
	for i := range files {
		files[i].Poster = sanitizeXMLText(files[i].Poster)
		files[i].Subject = sanitizeXMLText(files[i].Subject)
		for g := range files[i].Groups {
			files[i].Groups[g] = sanitizeXMLText(files[i].Groups[g])
		}
		sort.Slice(files[i].Segments, func(a, b int) bool {
			return files[i].Segments[a].Number < files[i].Segments[b].Number
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Subject < files[j].Subject
	})

	meta := []NZBMeta{
		{Type: "title", Value: sanitizeXMLText(title)},
		{Type: "created", Value: time.Now().UTC().Format(time.RFC3339)},
	}
	if password != "" {
		meta = append(meta, NZBMeta{Type: "password", Value: sanitizeXMLText(password)})
	}

	nzb := NZB{
		Xmlns: "http://www.newzbin.com/DTD/2003/nzb",
		Head:  &NZBHead{Meta: meta},
		Files: files,
	}

	data, err := xml.MarshalIndent(nzb, "", "  ")
	if err != nil {
		return err
	}

	fullXML := append([]byte(xml.Header), data...)
	return os.WriteFile(sysproc.NormalizePath(outputPath), fullXML, 0o600)
}

var cp437High = [128]rune{
	'Ç', 'ü', 'é', 'â', 'ä', 'à', 'å', 'ç', 'ê', 'ë', 'è', 'ï', 'î', 'ì', 'Ä', 'Å',
	'É', 'æ', 'Æ', 'ô', 'ö', 'ò', 'û', 'ù', 'ÿ', 'Ö', 'Ü', '¢', '£', '¥', '₧', 'ƒ',
	'á', 'í', 'ó', 'ú', 'ñ', 'Ñ', 'ª', 'º', '¿', '⌐', '¬', '½', '¼', '¡', '«', '»',
	'░', '▒', '▓', '│', '┤', '╡', '╢', '╖', '╕', '╣', '║', '╗', '╝', '╜', '╛', '┐',
	'└', '┴', '┬', '├', '─', '┼', '╞', '╟', '╚', '╔', '╩', '╦', '╠', '═', '╬', '╧',
	'╨', '╤', '╥', '╙', '╘', '╒', '╓', '╫', '╪', '┘', '┌', '█', '▄', '▌', '▐', '▀',
	'α', 'ß', 'Γ', 'π', 'Σ', 'σ', 'µ', 'τ', 'Φ', 'Θ', 'Ω', 'δ', '∞', 'φ', 'ε', '∩',
	'≡', '±', '≥', '≤', '⌠', '⌡', '÷', '≈', '°', '∙', '·', '√', 'ⁿ', '²', '■', '\u00A0',
}

func DecodeNFOBytes(raw []byte) string {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	raw = bytes.ReplaceAll(raw, []byte("\r"), []byte("\n"))

	hasHigh := false
	for _, b := range raw {
		if b >= 0x80 {
			hasHigh = true
			break
		}
	}
	if !hasHigh {
		return strings.TrimSpace(string(raw))
	}

	if utf8.Valid(raw) {
		return strings.TrimSpace(string(raw))
	}

	var sb strings.Builder
	sb.Grow(len(raw) * 2)
	for _, b := range raw {
		if b < 0x80 {
			sb.WriteByte(b)
		} else {
			sb.WriteRune(cp437High[b-0x80])
		}
	}
	return strings.TrimSpace(sb.String())
}

func FindAndReadNFO(sourcePath string, explicitNfoPath string) (string, string) {
	if strings.TrimSpace(explicitNfoPath) != "" {
		normExplicit := sysproc.NormalizePath(explicitNfoPath)
		if raw, err := os.ReadFile(normExplicit); err == nil {
			return DecodeNFOBytes(raw), sysproc.DisplayPath(normExplicit)
		}
	}

	normSrc := sysproc.NormalizePath(sourcePath)
	info, err := os.Stat(normSrc)
	if err != nil {
		return "", ""
	}

	if !info.IsDir() {
		baseNoExt := strings.TrimSuffix(normSrc, filepath.Ext(normSrc))
		for _, ext := range []string{".nfo", ".NFO"} {
			candidate := baseNoExt + ext
			if raw, err := os.ReadFile(candidate); err == nil {
				return DecodeNFOBytes(raw), sysproc.DisplayPath(candidate)
			}
		}
		parentDir := filepath.Dir(normSrc)
		if entries, err := os.ReadDir(parentDir); err == nil {
			var found []string
			for _, e := range entries {
				if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".nfo") {
					found = append(found, filepath.Join(parentDir, e.Name()))
				}
			}
			if len(found) == 1 {
				if raw, err := os.ReadFile(found[0]); err == nil {
					return DecodeNFOBytes(raw), sysproc.DisplayPath(found[0])
				}
			}
		}
		return "", ""
	}

	dirName := filepath.Base(sysproc.DisplayPath(normSrc))
	entries, err := os.ReadDir(normSrc)
	if err != nil {
		return "", ""
	}
	var firstNfo string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".nfo") {
			full := filepath.Join(normSrc, e.Name())
			if firstNfo == "" {
				firstNfo = full
			}
			if strings.EqualFold(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), dirName) {
				if raw, err := os.ReadFile(full); err == nil {
					return DecodeNFOBytes(raw), sysproc.DisplayPath(full)
				}
			}
		}
	}
	if firstNfo != "" {
		if raw, err := os.ReadFile(firstNfo); err == nil {
			return DecodeNFOBytes(raw), sysproc.DisplayPath(firstNfo)
		}
	}
	return "", ""
}

// SanitizeEZPostField entfernt Zeichen, die den [EZPOST]-Parser im Forum brechen (", [, ], |).
func SanitizeEZPostField(s string) string {
	s = strings.ReplaceAll(s, `"`, "")
	s = strings.ReplaceAll(s, "[", "")
	s = strings.ReplaceAll(s, "]", "")
	s = strings.ReplaceAll(s, "|", "")
	return strings.TrimSpace(s)
}

func WritePostingTemplate(nzbPath string, releaseTitle string, usedHeader string, groups []string, password string, postTimestamp int64, nfoContent string, mediaInfoContent string, coverURL string, imdbID string, synopsis string, uiLang string) (string, string, error) {
	return WritePostingTemplateWithType(nzbPath, releaseTitle, usedHeader, groups, password, postTimestamp, nfoContent, mediaInfoContent, coverURL, imdbID, synopsis, uiLang, "movies_series")
}

func WritePostingTemplateWithType(nzbPath string, releaseTitle string, usedHeader string, groups []string, password string, postTimestamp int64, nfoContent string, mediaInfoContent string, coverURL string, imdbID string, synopsis string, uiLang string, templateType string) (string, string, error) {
	txtPath := strings.TrimSuffix(nzbPath, filepath.Ext(nzbPath)) + ".txt"
	groupsJoined := strings.Join(groups, ",")
	tt := strings.ToLower(strings.TrimSpace(templateType))
	isMinimal := tt == "games" || tt == "game" || tt == "misc"

	var headerBlock strings.Builder
	fmt.Fprintf(&headerBlock, `[SIZE="3"][COLOR=Tomato][B]%s[/B][/COLOR][/SIZE]`+"\n\n", strings.TrimSpace(releaseTitle))
	if strings.TrimSpace(coverURL) != "" {
		fmt.Fprintf(&headerBlock, "[IMG]%s[/IMG]\n\n", strings.TrimSpace(coverURL))
	}
	if !isMinimal {
		if strings.TrimSpace(synopsis) != "" {
			fmt.Fprintf(&headerBlock, "%s\n\n", strings.TrimSpace(synopsis))
		}
		if strings.TrimSpace(imdbID) != "" {
			fmt.Fprintf(&headerBlock, "[imdb]%s[/imdb]\n\n", strings.TrimSpace(imdbID))
		}
	}

	var nfoSection string
	if strings.TrimSpace(nfoContent) != "" {
		nfoSection = fmt.Sprintf("\n"+`[SIZE="2"][COLOR=DeepSkyBlue]NFO:[/COLOR][/SIZE]`+"\n[NFO]%s[/NFO]\n", strings.TrimSpace(nfoContent))
	}

	var mediaSection string
	if !isMinimal && strings.TrimSpace(mediaInfoContent) != "" {
		mediaSection = fmt.Sprintf("\n"+`[SIZE="2"][COLOR=DeepSkyBlue]MediaInfo:[/COLOR][/SIZE]`+"\n[NFO]%s[/NFO]\n", strings.TrimSpace(mediaInfoContent))
	}

	ezpostLine := fmt.Sprintf("[HIDE][EZPOST]%s|%s|%s|%s|{%d}[/EZPOST][/HIDE]",
		SanitizeEZPostField(releaseTitle),
		SanitizeEZPostField(usedHeader),
		SanitizeEZPostField(groupsJoined),
		password,
		postTimestamp)

	template := fmt.Sprintf(`%s
%s
%s%s`, strings.TrimRight(headerBlock.String(), "\n"), ezpostLine, nfoSection, mediaSection)

	if err := os.WriteFile(sysproc.NormalizePath(txtPath), []byte(template), 0o600); err != nil {
		return "", template, err
	}
	return sysproc.DisplayPath(txtPath), template, nil
}

func FormatSubject(baseSubject string, fileIndex int, totalFiles int, fileName string, partNum int, totalParts int) string {
	return fmt.Sprintf("%s [%02d/%02d] - \"%s\" yEnc (%d/%d)", baseSubject, fileIndex, totalFiles, fileName, partNum, totalParts)
}
