package yenc

import (
	"hash/crc32"
	"strconv"
	"strings"
)

const lineLength = 128

// critical[b] == true: kodiertes Byte muss immer escaped werden (NUL, LF, CR, '=').
var critical = [256]bool{0x00: true, 0x0A: true, 0x0D: true, 0x3D: true}

// MaxEncodedSize liefert eine sichere Obergrenze für die Puffergröße eines Segments.
func MaxEncodedSize(dataLen int, fileName string) int {
	// Worst case: jedes Byte escaped (x2) + CRLF pro Zeile + Header/Trailer.
	return dataLen*2 + (dataLen/lineLength+2)*2 + len(fileName) + 256
}

// sanitizeName entfernt Zeichen, die die yEnc-Kopfzeile oder NNTP-Header brechen würden.
func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return '_'
		}
		return r
	}, s)
}

// AppendSegment kodiert data als yEnc-Segment und hängt es an dst an (dst wird ab Länge 0 beschrieben).
// Mit einem pro Worker wiederverwendeten dst entstehen keine Allokationen pro Segment.
// Der Ausgabestrom ist bereits für NNTP dot-gestuffed (Punkt am Zeilenanfang wird escaped).
func AppendSegment(dst, data []byte, fileName string, fileSize int64, partNum, totalParts int, offset int64) []byte {
	name := sanitizeName(fileName)
	if need := MaxEncodedSize(len(data), name); cap(dst) < need {
		dst = make([]byte, 0, need)
	}
	dst = dst[:0]

	multi := totalParts > 1
	if multi {
		dst = append(dst, "=ybegin part="...)
		dst = strconv.AppendInt(dst, int64(partNum), 10)
		dst = append(dst, " total="...)
		dst = strconv.AppendInt(dst, int64(totalParts), 10)
		dst = append(dst, ' ')
	} else {
		dst = append(dst, "=ybegin "...)
	}
	dst = append(dst, "line="...)
	dst = strconv.AppendInt(dst, lineLength, 10)
	dst = append(dst, " size="...)
	dst = strconv.AppendInt(dst, fileSize, 10)
	dst = append(dst, " name="...)
	dst = append(dst, name...)
	dst = append(dst, '\r', '\n')
	if multi {
		dst = append(dst, "=ypart begin="...)
		dst = strconv.AppendInt(dst, offset+1, 10)
		dst = append(dst, " end="...)
		dst = strconv.AppendInt(dst, offset+int64(len(data)), 10)
		dst = append(dst, '\r', '\n')
	}

	col := 0
	last := len(data) - 1
	for i, b := range data {
		e := b + 42
		escape := critical[e]
		if !escape {
			switch e {
			case '.':
				// Punkt am Zeilenanfang: escapen statt NNTP-Dot-Stuffing (yEnc-Empfehlung).
				escape = col == 0
			case ' ', '\t':
				// Whitespace am Zeilenanfang/-ende wird von manchen Servern/Clients abgeschnitten.
				escape = col == 0 || col == lineLength-1 || i == last
			}
		}
		if escape {
			dst = append(dst, '=', e+64)
			col += 2
		} else {
			dst = append(dst, e)
			col++
		}
		if col >= lineLength {
			dst = append(dst, '\r', '\n')
			col = 0
		}
	}
	if col > 0 {
		dst = append(dst, '\r', '\n')
	}

	crc := crc32.ChecksumIEEE(data)
	dst = append(dst, "=yend size="...)
	dst = strconv.AppendInt(dst, int64(len(data)), 10)
	if multi {
		dst = append(dst, " part="...)
		dst = strconv.AppendInt(dst, int64(partNum), 10)
		dst = append(dst, " pcrc32="...)
	} else {
		dst = append(dst, " crc32="...)
	}
	dst = appendHex8(dst, crc)
	dst = append(dst, '\r', '\n')
	return dst
}

func appendHex8(dst []byte, v uint32) []byte {
	const hexdigits = "0123456789abcdef"
	for shift := 28; shift >= 0; shift -= 4 {
		dst = append(dst, hexdigits[(v>>uint(shift))&0xF])
	}
	return dst
}
