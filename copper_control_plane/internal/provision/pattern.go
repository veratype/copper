package provision

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Pattern placeholders:
//
//	{name}     inventory server name
//	{n}        1-based index (1, 2, 3, …)
//	{nn}       zero-padded to 2 digits (01, 02, …)
//	{nnn}      zero-padded to 3 digits
//	{n+10}     index + offset (also {nn+10}, {nnn+5}, …)
//
// Used for batch hostnames (e.g. "node-{nn}") and static addresses
// (e.g. "10.0.0.{n+10}/24").
var patternToken = regexp.MustCompile(`\{(name|n{1,3}(?:\+\d+)?)\}`)

// anyBraceToken matches any {...} so typos like {myhost} fail closed.
var anyBraceToken = regexp.MustCompile(`\{[^}]*\}`)

// ExpandPattern replaces {name} / {n} / {nn} / {n+K} tokens.
// index is 1-based. Empty pat returns name (or "" if name is empty).
// Unknown brace tokens (e.g. {myhost}) are an error — they must not become literal hostname text.
func ExpandPattern(pat, name string, index int) (string, error) {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return strings.TrimSpace(name), nil
	}
	if index < 1 {
		return "", fmt.Errorf("index must be >= 1")
	}
	var firstErr error
	out := patternToken.ReplaceAllStringFunc(pat, func(tok string) string {
		if firstErr != nil {
			return tok
		}
		inner := tok[1 : len(tok)-1]
		if inner == "name" {
			return name
		}
		s, err := expandIndexToken(inner, index)
		if err != nil {
			firstErr = err
			return tok
		}
		return s
	})
	if firstErr != nil {
		return "", firstErr
	}
	if leftover := anyBraceToken.FindString(out); leftover != "" {
		return "", fmt.Errorf("unknown pattern token %s (use {name}, {n}, {nn}, {nnn}, or {n+K})", leftover)
	}
	return out, nil
}

func expandIndexToken(token string, index int) (string, error) {
	base := token
	offset := 0
	if i := strings.IndexByte(token, '+'); i >= 0 {
		base = token[:i]
		n, err := strconv.Atoi(token[i+1:])
		if err != nil {
			return "", fmt.Errorf("bad offset in {%s}", token)
		}
		offset = n
	}
	for _, c := range base {
		if c != 'n' {
			return "", fmt.Errorf("unknown pattern token {%s}", token)
		}
	}
	if base == "" {
		return "", fmt.Errorf("empty pattern token")
	}
	v := index + offset
	if v < 0 {
		return "", fmt.Errorf("pattern {%s} yields negative index", token)
	}
	width := len(base)
	if width == 1 {
		return strconv.Itoa(v), nil
	}
	return fmt.Sprintf("%0*d", width, v), nil
}

// ExpandAddressCIDR expands index tokens in a CIDR pattern and validates the result.
func ExpandAddressCIDR(pat string, index int) (string, error) {
	out, err := ExpandPattern(pat, "", index)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("address CIDR pattern is empty")
	}
	ip, ipNet, err := net.ParseCIDR(out)
	if err != nil || ip == nil || ipNet == nil {
		return "", fmt.Errorf("invalid address CIDR %q after pattern expand", out)
	}
	ones, bits := ipNet.Mask.Size()
	if bits == 0 || ones < 0 {
		return "", fmt.Errorf("invalid address CIDR mask in %q", out)
	}
	return fmt.Sprintf("%s/%d", ip.String(), ones), nil
}

// HasIndexPattern reports whether pat contains an {n…} token (batch static IPs).
func HasIndexPattern(pat string) bool {
	for _, m := range patternToken.FindAllStringSubmatch(pat, -1) {
		if len(m) > 1 && m[1] != "name" {
			return true
		}
	}
	return false
}
