package storage

import (
	"regexp"
	"strconv"
	"strings"
)

// SizeGBLoose parses controller / lsblk size strings into decimal GB.
// Accepts "400.000 MB", "1.746 TB", "400M", "18.2T", etc.
func SizeGBLoose(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	re := regexp.MustCompile(`(?i)([\d.]+)\s*(TB|T|GB|G|MB|M)?`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	switch strings.ToUpper(m[2]) {
	case "TB", "T":
		return v * 1000
	case "MB", "M":
		return v / 1000
	case "GB", "G", "":
		return v
	default:
		return v
	}
}
