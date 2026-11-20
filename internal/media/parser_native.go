package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

type parsedTrack struct {
	kind       string // "Video", "Audio", "Text"
	trackNum   int
	codecID    string
	format     string
	formatInfo string
	commercial string
	profile    string
	width      uint64
	height     uint64
	fps        float64
	channels   uint64
	sampleRate float64
	language   string
	title      string
	bitRateBps float64
	streamSize int64
	defaultFlg bool
	forcedFlg  bool
	durationMs float64
}

type parsedMediaSummary struct {
	container      string
	fileSize       int64
	durationMs     float64
	overallBitRate float64
	frameRate      float64
	writingApp     string
	writingLibrary string
	encodedDate    string
	tracks         []parsedTrack
}

func formatSizeGiB(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	gib := float64(b) / (1024 * 1024 * 1024)
	if gib >= 1.0 {
		return fmt.Sprintf("%.2f GiB", gib)
	}
	mib := float64(b) / (1024 * 1024)
	if mib >= 1.0 {
		return fmt.Sprintf("%.1f MiB", mib)
	}
	kib := float64(b) / 1024
	return fmt.Sprintf("%.1f KiB", kib)
}

func formatDurationMinSec(ms float64) string {
	if ms <= 0 {
		return ""
	}
	totalSec := int64(ms / 1000.0)
	mins := totalSec / 60
	secs := totalSec % 60
	if mins >= 60 {
		hrs := mins / 60
		mins = mins % 60
		return fmt.Sprintf("%d h %d min %d s", hrs, mins, secs)
	}
	return fmt.Sprintf("%d min %d s", mins, secs)
}

func formatBitrateKbps(bps float64) string {
	if bps <= 0 {
		return ""
	}
	kbps := int64(bps / 1000.0)
	s := fmt.Sprintf("%d", kbps)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	if len(s) > 0 {
		parts = append([]string{s}, parts...)
	}
	return strings.Join(parts, " ") + " kb/s"
}

func normalizeLangCode(code string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	switch c {
	case "ger", "deu", "de":
		return "German"
	case "eng", "en":
		return "English"
	case "fra", "fre", "fr":
		return "French"
	case "spa", "es":
		return "Spanish"
	case "ita", "it":
		return "Italian"
	case "jpn", "ja":
		return "Japanese"
	case "und", "":
		return ""
	default:
		return strings.ToUpper(c[:1]) + c[1:]
	}
}

func mapVideoCodec(codecID string) (fmtName, fmtInfo, profile string) {
	u := strings.ToUpper(strings.TrimSpace(codecID))
	switch {
	case strings.Contains(u, "HEVC") || strings.Contains(u, "HVC1") || strings.Contains(u, "H265") || strings.Contains(u, "X265"):
		return "HEVC", "High Efficiency Video Coding", "Main"
	case strings.Contains(u, "AVC") || strings.Contains(u, "H264") || strings.Contains(u, "X264"):
		return "AVC", "Advanced Video Codec", "High"
	case strings.Contains(u, "AV1"):
		return "AV1", "AOMedia Video 1", "Main"
	case strings.Contains(u, "VP9"):
		return "VP9", "Google VP9", ""
	case strings.Contains(u, "MPEG2") || u == "V_MPEG2":
		return "MPEG Video", "MPEG-2 Video", "Main"
	default:
		return codecID, "", ""
	}
}

func mapAudioCodec(codecID string) (fmtName, fmtInfo, commercial string) {
	u := strings.ToUpper(strings.TrimSpace(codecID))
	switch {
	case strings.Contains(u, "EAC3") || strings.Contains(u, "EC-3"):
		return "E-AC-3", "Enhanced AC-3", "Dolby Digital Plus"
	case strings.Contains(u, "AC3") || strings.Contains(u, "AC-3"):
		return "AC-3", "Audio Coding 3", "Dolby Digital"
	case strings.Contains(u, "DTS"):
		return "DTS", "Digital Theater Systems", "DTS"
	case strings.Contains(u, "TRUEHD"):
		return "MLP FBA", "Meridian Lossless Packing FBA", "Dolby TrueHD"
	case strings.Contains(u, "AAC") || strings.Contains(u, "MP4A"):
		return "AAC", "Advanced Audio Codec", ""
	case strings.Contains(u, "OPUS"):
		return "Opus", "Opus Audio", ""
	case strings.Contains(u, "FLAC"):
		return "FLAC", "Free Lossless Audio Codec", ""
	case strings.Contains(u, "MP3") || u == "A_MPEG/L3":
		return "MPEG Audio", "MPEG-1 Audio layer 3", ""
	default:
		return codecID, "", ""
	}
}

func mapSubtitleCodec(codecID string) (fmtName, codecInfo string) {
	u := strings.ToUpper(strings.TrimSpace(codecID))
	switch {
	case strings.Contains(u, "UTF8") || strings.Contains(u, "SRT") || u == "S_TEXT/UTF8":
		return "UTF-8", "UTF-8 Plain Text"
	case strings.Contains(u, "ASS") || strings.Contains(u, "SSA"):
		return "ASS", "Advanced SubStation Alpha"
	case strings.Contains(u, "PGS") || strings.Contains(u, "HDMV"):
		return "PGS", "Presentation Graphic Stream"
	case strings.Contains(u, "VOBSUB"):
		return "VobSub", "DVD Subtitle"
	default:
		return codecID, ""
	}
}

// readEBMLVInt reads a variable-length integer (ID or size) from EBML data
func readEBMLVInt(data []byte, pos int, maskID bool) (val uint64, nextPos int, ok bool) {
	if pos >= len(data) {
		return 0, pos, false
	}
	first := data[pos]
	if first == 0 {
		return 0, pos + 1, false
	}
	length := 1
	for bit := byte(0x80); bit != 0; bit >>= 1 {
		if (first & bit) != 0 {
			break
		}
		length++
		if length > 8 {
			return 0, pos + 1, false
		}
	}
	if pos+length > len(data) {
		return 0, pos + length, false
	}
	var v uint64
	if maskID {
		v = uint64(first)
	} else {
		mask := byte(0xFF) >> length
		v = uint64(first & mask)
	}
	for i := 1; i < length; i++ {
		v = (v << 8) | uint64(data[pos+i])
	}
	return v, pos + length, true
}

func readEBMLUint(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = (v << 8) | uint64(x)
	}
	return v
}

func readEBMLFloat(b []byte) float64 {
	var v float64
	switch len(b) {
	case 4:
		v = float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		v = math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

func parseMatroskaFile(filePath string, fileSize int64) (*parsedMediaSummary, bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	// Read first 16 MB for EBML Header, Info and Tracks (covers typical MKV headers completely)
	const maxScan = 16 * 1024 * 1024
	toRead := fileSize
	if toRead > maxScan {
		toRead = maxScan
	}
	buf := make([]byte, toRead)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, false
	}
	data := buf[:n]

	// Check EBML Header signature 0x1A45DFA3
	if len(data) < 8 || !bytes.HasPrefix(data, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return nil, false
	}

	summary := &parsedMediaSummary{
		container: "Matroska",
		fileSize:  fileSize,
	}

	var timecodeScale uint64 = 1000000 // default 1 ms = 1,000,000 ns
	var rawDuration float64

	var parseInfoElem func(b []byte)
	parseInfoElem = func(b []byte) {
		pos := 0
		for pos < len(b) {
			id, p2, ok := readEBMLVInt(b, pos, true)
			if !ok {
				break
			}
			sz, p3, ok2 := readEBMLVInt(b, p2, false)
			if !ok2 || sz > uint64(len(b)-p3) {
				break
			}
			val := b[p3 : p3+int(sz)]
			switch id {
			case 0x2AD7B1: // TimecodeScale
				if u := readEBMLUint(val); u > 0 {
					timecodeScale = u
				}
			case 0x4489: // Duration
				rawDuration = readEBMLFloat(val)
			case 0x4D80: // MuxingApp
				if summary.writingLibrary == "" {
					summary.writingLibrary = strings.TrimRight(string(val), "\x00")
				}
			case 0x5741: // WritingApp
				summary.writingApp = strings.TrimRight(string(val), "\x00")
			case 0x4461: // DateUTC (nanoseconds since 2001-01-01)
				if len(val) == 8 {
					ns := int64(binary.BigEndian.Uint64(val))
					// 2001-01-01T00:00:00Z unix timestamp is 978307200
					sec := 978307200 + ns/1000000000
					summary.encodedDate = fmt.Sprintf("UTC epoch %d", sec)
				}
			}
			pos = p3 + int(sz)
		}
	}

	parseTrackEntry := func(b []byte) {
		var tr parsedTrack
		tr.defaultFlg = true
		var defaultDurationNs uint64
		var vidW, vidH uint64
		var dispW, dispH uint64
		var audCh uint64
		var audSr float64

		pos := 0
		for pos < len(b) {
			id, p2, ok := readEBMLVInt(b, pos, true)
			if !ok {
				break
			}
			sz, p3, ok2 := readEBMLVInt(b, p2, false)
			if !ok2 || sz > uint64(len(b)-p3) {
				break
			}
			val := b[p3 : p3+int(sz)]
			switch id {
			case 0xD7: // TrackNumber
				tr.trackNum = int(readEBMLUint(val))
			case 0x83: // TrackType (1=video, 2=audio, 17=subtitle)
				switch readEBMLUint(val) {
				case 1:
					tr.kind = "Video"
				case 2:
					tr.kind = "Audio"
				case 17:
					tr.kind = "Text"
				}
			case 0x88: // FlagDefault
				tr.defaultFlg = readEBMLUint(val) != 0
			case 0x55AA: // FlagForced
				tr.forcedFlg = readEBMLUint(val) != 0
			case 0x23E383: // DefaultDuration
				defaultDurationNs = readEBMLUint(val)
			case 0x536E: // Name
				tr.title = strings.TrimRight(string(val), "\x00")
			case 0x22B59C: // Language
				if tr.language == "" {
					tr.language = strings.TrimRight(string(val), "\x00")
				}
			case 0x22B59D: // LanguageBCP47
				bcp := strings.TrimRight(string(val), "\x00")
				if bcp != "" {
					tr.language = bcp
				}
			case 0x86: // CodecID
				tr.codecID = strings.TrimRight(string(val), "\x00")
			case 0xE0: // Video sub-element
				vpos := 0
				for vpos < len(val) {
					vid, vp2, vok := readEBMLVInt(val, vpos, true)
					if !vok {
						break
					}
					vsz, vp3, vok2 := readEBMLVInt(val, vp2, false)
					if !vok2 || vsz > uint64(len(val)-vp3) {
						break
					}
					vval := val[vp3 : vp3+int(vsz)]
					switch vid {
					case 0xB0: // PixelWidth
						vidW = readEBMLUint(vval)
					case 0xBA: // PixelHeight
						vidH = readEBMLUint(vval)
					case 0x54B0: // DisplayWidth
						dispW = readEBMLUint(vval)
					case 0x54BA: // DisplayHeight
						dispH = readEBMLUint(vval)
					}
					vpos = vp3 + int(vsz)
				}
			case 0xE1: // Audio sub-element
				apos := 0
				for apos < len(val) {
					aid, ap2, aok := readEBMLVInt(val, apos, true)
					if !aok {
						break
					}
					asz, ap3, aok2 := readEBMLVInt(val, ap2, false)
					if !aok2 || asz > uint64(len(val)-ap3) {
						break
					}
					aval := val[ap3 : ap3+int(asz)]
					switch aid {
					case 0xB5: // SamplingFrequency
						audSr = readEBMLFloat(aval)
					case 0x9F: // Channels
						audCh = readEBMLUint(aval)
					}
					apos = ap3 + int(asz)
				}
			}
			pos = p3 + int(sz)
		}

		if tr.kind == "" {
			return
		}
		tr.width = vidW
		tr.height = vidH
		_ = dispW
		_ = dispH
		tr.channels = audCh
		tr.sampleRate = audSr

		if tr.kind == "Video" {
			tr.format, tr.formatInfo, tr.profile = mapVideoCodec(tr.codecID)
			if defaultDurationNs > 0 {
				tr.fps = 1e9 / float64(defaultDurationNs)
				if summary.frameRate == 0 {
					summary.frameRate = tr.fps
				}
			}
		} else if tr.kind == "Audio" {
			tr.format, tr.formatInfo, tr.commercial = mapAudioCodec(tr.codecID)
		} else if tr.kind == "Text" {
			tr.format, tr.formatInfo = mapSubtitleCodec(tr.codecID)
		}

		summary.tracks = append(summary.tracks, tr)
	}

	var parseTracksElem func(b []byte)
	parseTracksElem = func(b []byte) {
		pos := 0
		for pos < len(b) {
			id, p2, ok := readEBMLVInt(b, pos, true)
			if !ok {
				break
			}
			sz, p3, ok2 := readEBMLVInt(b, p2, false)
			if !ok2 || sz > uint64(len(b)-p3) {
				break
			}
			if id == 0xAE { // TrackEntry
				parseTrackEntry(b[p3 : p3+int(sz)])
			}
			pos = p3 + int(sz)
		}
	}

	var walkTopLevel func(b []byte)
	walkTopLevel = func(b []byte) {
		pos := 0
		for pos < len(b) {
			id, p2, ok := readEBMLVInt(b, pos, true)
			if !ok {
				break
			}
			sz, p3, ok2 := readEBMLVInt(b, p2, false)
			if !ok2 {
				break
			}
			end := len(b)
			if sz < uint64(len(b)-p3) && sz <= uint64(math.MaxInt) {
				end = p3 + int(sz)
			}
			switch id {
			case 0x18538067: // Segment -> recurse inside segment children
				walkTopLevel(b[p3:end])
			case 0x1549A966: // Info
				parseInfoElem(b[p3:end])
			case 0x1654AE6B: // Tracks
				parseTracksElem(b[p3:end])
			}
			pos = end
		}
	}

	walkTopLevel(data)

	if rawDuration > 0 && timecodeScale > 0 {
		summary.durationMs = (rawDuration * float64(timecodeScale)) / 1e6
	}
	if summary.durationMs > 0 && fileSize > 0 {
		summary.overallBitRate = (float64(fileSize) * 8.0) / (summary.durationMs / 1000.0)
	}

	return summary, len(summary.tracks) > 0 || summary.durationMs > 0 || summary.writingApp != ""
}

func parseMP4File(filePath string, fileSize int64) (*parsedMediaSummary, bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	const maxScan = 8 * 1024 * 1024
	toRead := fileSize
	if toRead > maxScan {
		toRead = maxScan
	}
	buf := make([]byte, toRead)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, false
	}
	data := buf[:n]

	summary := &parsedMediaSummary{
		container: "MPEG-4",
		fileSize:  fileSize,
	}

	var scanAtoms func(b []byte)
	scanAtoms = func(b []byte) {
		pos := 0
		for pos+8 <= len(b) {
			sz32 := binary.BigEndian.Uint32(b[pos : pos+4])
			atomType := string(b[pos+4 : pos+8])
			hdrLen := 8
			sz64 := uint64(sz32)
			if sz32 == 1 && pos+16 <= len(b) {
				sz64 = binary.BigEndian.Uint64(b[pos+8 : pos+16])
				hdrLen = 16
			}
			if sz64 < uint64(hdrLen) || sz64 > uint64(len(b)-pos) || sz64 > uint64(math.MaxInt) {
				break
			}
			atomSize := int(sz64)
			payload := b[pos+hdrLen : pos+atomSize]
			switch atomType {
			case "moov", "trak", "mdia", "minf", "stbl", "udta", "meta":
				scanAtoms(payload)
			case "mvhd":
				if len(payload) >= 24 {
					ver := payload[0]
					if ver == 0 && len(payload) >= 20 {
						ts := binary.BigEndian.Uint32(payload[12:16])
						dur := binary.BigEndian.Uint32(payload[16:20])
						if ts > 0 {
							summary.durationMs = (float64(dur) / float64(ts)) * 1000.0
						}
					} else if ver == 1 && len(payload) >= 32 {
						ts := binary.BigEndian.Uint32(payload[20:24])
						dur := binary.BigEndian.Uint64(payload[24:32])
						if ts > 0 {
							summary.durationMs = (float64(dur) / float64(ts)) * 1000.0
						}
					}
				}
			case "tkhd":
				if len(payload) >= 84 {
					wFixed := binary.BigEndian.Uint32(payload[len(payload)-8 : len(payload)-4])
					hFixed := binary.BigEndian.Uint32(payload[len(payload)-4:])
					w := uint64(wFixed >> 16)
					h := uint64(hFixed >> 16)
					if w > 0 && h > 0 {
						summary.tracks = append(summary.tracks, parsedTrack{
							kind:       "Video",
							format:     "AVC",
							formatInfo: "Advanced Video Codec",
							width:      w,
							height:     h,
						})
					}
				}
			case "stsd":
				if len(payload) >= 16 {
					entryData := payload[8:]
					if len(entryData) >= 8 {
						fourCC := strings.ToUpper(string(entryData[4:8]))
						switch fourCC {
						case "AVC1", "AVC3":
							for i := len(summary.tracks) - 1; i >= 0; i-- {
								if summary.tracks[i].kind == "Video" {
									summary.tracks[i].format, summary.tracks[i].formatInfo, _ = mapVideoCodec("AVC")
									break
								}
							}
						case "HVC1", "HEV1":
							for i := len(summary.tracks) - 1; i >= 0; i-- {
								if summary.tracks[i].kind == "Video" {
									summary.tracks[i].format, summary.tracks[i].formatInfo, _ = mapVideoCodec("HEVC")
									break
								}
							}
						case "MP4A":
							ch := uint64(2)
							sr := float64(48000)
							if len(entryData) >= 28 {
								ch = uint64(binary.BigEndian.Uint16(entryData[24:26]))
								sr = float64(binary.BigEndian.Uint32(entryData[28:32]) >> 16)
							}
							summary.tracks = append(summary.tracks, parsedTrack{
								kind:       "Audio",
								format:     "AAC",
								formatInfo: "Advanced Audio Codec",
								channels:   ch,
								sampleRate: sr,
							})
						case "EC-3":
							summary.tracks = append(summary.tracks, parsedTrack{
								kind:       "Audio",
								format:     "E-AC-3",
								formatInfo: "Enhanced AC-3",
								commercial: "Dolby Digital Plus",
								channels:   6,
								sampleRate: 48000,
							})
						case "AC-3":
							summary.tracks = append(summary.tracks, parsedTrack{
								kind:       "Audio",
								format:     "AC-3",
								formatInfo: "Audio Coding 3",
								commercial: "Dolby Digital",
								channels:   6,
								sampleRate: 48000,
							})
						}
					}
				}
			}
			pos += atomSize
		}
	}

	scanAtoms(data)

	if summary.durationMs > 0 && fileSize > 0 {
		summary.overallBitRate = (float64(fileSize) * 8.0) / (summary.durationMs / 1000.0)
	}
	return summary, len(summary.tracks) > 0 || summary.durationMs > 0
}

func renderMediaInfoText(fileName string, s *parsedMediaSummary) string {
	var b strings.Builder

	// General section
	b.WriteString("General\n")
	fmt.Fprintf(&b, "Complete name                            : %s\n", fileName)
	fmt.Fprintf(&b, "Format                                   : %s\n", s.container)
	if s.fileSize > 0 {
		fmt.Fprintf(&b, "File size                                : %s\n", formatSizeGiB(s.fileSize))
	}
	if dur := formatDurationMinSec(s.durationMs); dur != "" {
		fmt.Fprintf(&b, "Duration                                 : %s\n", dur)
	}
	if br := formatBitrateKbps(s.overallBitRate); br != "" {
		fmt.Fprintf(&b, "Overall bit rate                         : %s\n", br)
	}
	if s.frameRate > 0 {
		fmt.Fprintf(&b, "Frame rate                               : %.3f FPS\n", s.frameRate)
	}
	if s.writingApp != "" {
		fmt.Fprintf(&b, "Writing application                      : %s\n", s.writingApp)
	}
	if s.writingLibrary != "" {
		fmt.Fprintf(&b, "Writing library                          : %s\n", s.writingLibrary)
	}

	vidIdx := 0
	audIdx := 0
	txtIdx := 0

	for _, tr := range s.tracks {
		switch tr.kind {
		case "Video":
			vidIdx++
			b.WriteString("\nVideo\n")
			if tr.trackNum > 0 {
				fmt.Fprintf(&b, "ID                                       : %d\n", tr.trackNum)
			}
			if tr.format != "" {
				fmt.Fprintf(&b, "Format                                   : %s\n", tr.format)
			}
			if tr.formatInfo != "" {
				fmt.Fprintf(&b, "Format/Info                              : %s\n", tr.formatInfo)
			}
			if tr.profile != "" {
				fmt.Fprintf(&b, "Format profile                           : %s\n", tr.profile)
			}
			if tr.codecID != "" {
				fmt.Fprintf(&b, "Codec ID                                 : %s\n", tr.codecID)
			}
			if dur := formatDurationMinSec(s.durationMs); dur != "" {
				fmt.Fprintf(&b, "Duration                                 : %s\n", dur)
			}
			if tr.width > 0 && tr.height > 0 {
				fmt.Fprintf(&b, "Width                                    : %d pixels\n", tr.width)
				fmt.Fprintf(&b, "Height                                   : %d pixels\n", tr.height)
			}
			if tr.fps > 0 {
				fmt.Fprintf(&b, "Frame rate                               : %.3f FPS\n", tr.fps)
			}
			if tr.streamSize > 0 {
				fmt.Fprintf(&b, "Stream size                              : %s\n", formatSizeGiB(tr.streamSize))
			}
			fmt.Fprintf(&b, "Default                                  : %s\n", boolYesNo(tr.defaultFlg))
			fmt.Fprintf(&b, "Forced                                   : %s\n", boolYesNo(tr.forcedFlg))

		case "Audio":
			audIdx++
			b.WriteString(fmt.Sprintf("\nAudio #%d\n", audIdx))
			if tr.trackNum > 0 {
				fmt.Fprintf(&b, "ID                                       : %d\n", tr.trackNum)
			}
			if tr.format != "" {
				fmt.Fprintf(&b, "Format                                   : %s\n", tr.format)
			}
			if tr.formatInfo != "" {
				fmt.Fprintf(&b, "Format/Info                              : %s\n", tr.formatInfo)
			}
			if tr.commercial != "" {
				fmt.Fprintf(&b, "Commercial name                          : %s\n", tr.commercial)
			}
			if tr.codecID != "" {
				fmt.Fprintf(&b, "Codec ID                                 : %s\n", tr.codecID)
			}
			if dur := formatDurationMinSec(s.durationMs); dur != "" {
				fmt.Fprintf(&b, "Duration                                 : %s\n", dur)
			}
			if tr.channels > 0 {
				fmt.Fprintf(&b, "Channel(s)                               : %d channels\n", tr.channels)
			}
			if tr.sampleRate > 0 {
				fmt.Fprintf(&b, "Sampling rate                            : %.1f kHz\n", tr.sampleRate/1000.0)
			}
			if lang := normalizeLangCode(tr.language); lang != "" {
				fmt.Fprintf(&b, "Language                                 : %s\n", lang)
			}
			if tr.title != "" {
				fmt.Fprintf(&b, "Title                                    : %s\n", tr.title)
			}
			if tr.streamSize > 0 {
				fmt.Fprintf(&b, "Stream size                              : %s\n", formatSizeGiB(tr.streamSize))
			}
			fmt.Fprintf(&b, "Default                                  : %s\n", boolYesNo(tr.defaultFlg))
			fmt.Fprintf(&b, "Forced                                   : %s\n", boolYesNo(tr.forcedFlg))

		case "Text":
			txtIdx++
			b.WriteString(fmt.Sprintf("\nText #%d\n", txtIdx))
			if tr.trackNum > 0 {
				fmt.Fprintf(&b, "ID                                       : %d\n", tr.trackNum)
			}
			if tr.format != "" {
				fmt.Fprintf(&b, "Format                                   : %s\n", tr.format)
			}
			if tr.codecID != "" {
				fmt.Fprintf(&b, "Codec ID                                 : %s\n", tr.codecID)
			}
			if tr.codecID != "" {
				_, info := mapSubtitleCodec(tr.codecID)
				if info != "" {
					fmt.Fprintf(&b, "Codec ID/Info                            : %s\n", info)
				}
			}
			if lang := normalizeLangCode(tr.language); lang != "" {
				fmt.Fprintf(&b, "Language                                 : %s\n", lang)
			}
			if tr.title != "" {
				fmt.Fprintf(&b, "Title                                    : %s\n", tr.title)
			}
			fmt.Fprintf(&b, "Default                                  : %s\n", boolYesNo(tr.defaultFlg))
			fmt.Fprintf(&b, "Forced                                   : %s\n", boolYesNo(tr.forcedFlg))
		}
	}

	_ = filepath.Ext(fileName)
	return strings.TrimSpace(b.String())
}

func boolYesNo(v bool) string {
	if v {
		return "Yes"
	}
	return "No"
}

func parseNativeMediaSummary(mediaPath string, fileSize int64) string {
	fileName := filepath.Base(mediaPath)
	ext := strings.ToLower(filepath.Ext(fileName))

	if ext == ".mkv" || ext == ".webm" {
		if sum, ok := parseMatroskaFile(mediaPath, fileSize); ok {
			return renderMediaInfoText(fileName, sum)
		}
	}
	if ext == ".mp4" || ext == ".m4v" || ext == ".mov" {
		if sum, ok := parseMP4File(mediaPath, fileSize); ok {
			return renderMediaInfoText(fileName, sum)
		}
	}
	return ""
}
