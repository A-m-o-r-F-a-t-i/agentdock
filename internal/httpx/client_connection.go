package httpx

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
)

// This digest is used only as an in-memory observation key. Access credentials
// are never returned by the management endpoint or written to a log/file.
func clientCredentialDigest(r *http.Request) string {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || len(fields[1]) > 16384 {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fields[1])))
}
