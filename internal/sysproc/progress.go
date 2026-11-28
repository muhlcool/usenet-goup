package sysproc

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

type ProgressStats struct {
	Phase         string  `json:"phase"`
	PhaseLabel    string  `json:"phaseLabel"`
	Active        bool    `json:"active"`
	CompletedSegs int64   `json:"completedSegs"`
	TotalSegs     int64   `json:"totalSegs"`
	Percent       float64 `json:"percent"`
	NettoMBs      float64 `json:"nettoMBs"`
	BruttoMBs     float64 `json:"bruttoMBs"`
	BruttoMbits   float64 `json:"bruttoMbits"`
	UploadedNetMB float64 `json:"uploadedNetMB"`
	TotalNetMB    float64 `json:"totalNetMB"`
}

func CalcPathSize(root string) int64 {
	var total int64
	normRoot := NormalizePath(root)
	_ = filepath.WalkDir(normRoot, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, iErr := d.Info(); iErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func CalcDirCurrentSize(dir string, suffixFilter string) (int64, int64) {
	var total int64
	var count int64
	entries, err := os.ReadDir(NormalizePath(dir))
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if suffixFilter != "" && filepath.Ext(e.Name()) != suffixFilter {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
			count++
		}
	}
	return total, count
}

var pctRegex = regexp.MustCompile(`(\d{1,3}(?:\.\d+)?)\s*%`)

func ReadPercentStream(r io.Reader, onPercent func(float64)) {
	buf := make([]byte, 512)
	var tail string
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := tail + string(buf[:n])
			matches := pctRegex.FindAllStringSubmatch(chunk, -1)
			if len(matches) > 0 {
				last := matches[len(matches)-1][1]
				if val, pErr := strconv.ParseFloat(last, 64); pErr == nil && val >= 0 && val <= 100 {
					onPercent(val)
				}
			}
			if len(chunk) > 32 {
				tail = chunk[len(chunk)-32:]
			} else {
				tail = chunk
			}
		}
		if err != nil {
			return
		}
	}
}
