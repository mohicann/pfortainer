package pf

import (
	"net/url"
	"strings"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// urlenc: urlencoding::encode(RFC 3986 unreserved만 남김).
func urlenc(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(string("0123456789abcdef"[c>>4])+string("0123456789abcdef"[c&15])))
		}
	}
	return b.String()
}

var _ = url.QueryEscape
