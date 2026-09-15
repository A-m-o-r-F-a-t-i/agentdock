package client

import (
	"errors"
	"net/http"
	"net/url"
	"runtime"
	"strings"
)

// The literal package headers and host credential headers must never travel to
// another origin, including redirects whose request headers were already copied.
func newOriginBoundHTTPClient(endpoint string, headers http.Header) *http.Client {
	return &http.Client{
		Transport: headerRoundTripper{headers: headers, origin: endpoint},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("MCP redirect limit exceeded")
			}
			if !sameHTTPOrigin(endpoint, request.URL.String()) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func sameHTTPOrigin(left, right string) bool {
	a, err := url.Parse(left)
	if err != nil {
		return false
	}
	b, err := url.Parse(right)
	if err != nil {
		return false
	}
	port := func(value *url.URL) string {
		if value.Port() != "" {
			return value.Port()
		}
		if strings.EqualFold(value.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func setProcessEnvironmentValue(values map[string]string, key, value string) {
	if runtime.GOOS == "windows" {
		for existing := range values {
			if strings.EqualFold(existing, key) {
				delete(values, existing)
			}
		}
	}
	values[key] = value
}
