package auth

import "testing"

func TestBearerResourceMetadataFormatting(t *testing.T) {
	const resource = "https://device.example.ts.net/.well-known/oauth-protected-resource/mcp"
	for name, headers := range map[string][]string{
		"canonical":           {`Bearer resource_metadata="` + resource + `"`},
		"order and additions": {`Bearer scope="mcp", resource_metadata="` + resource + `", error="invalid_token"`},
		"case and whitespace": {"bEaReR\tRESOURCE_METADATA = \"" + resource + "\" , scope = \"mcp\""},
		"quoted comma":        {`Bearer error_description="login, then retry", resource_metadata="` + resource + `"`},
		"other scheme":        {`Basic realm="local", Bearer resource_metadata="` + resource + `"`},
		"multiple fields":     {`Basic realm="local"`, `Bearer resource_metadata="` + resource + `"`},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := BearerResourceMetadata(headers)
			if err != nil || got != resource {
				t.Fatalf("got %q, err=%v", got, err)
			}
		})
	}
	for _, header := range []string{
		`Basic resource_metadata="` + resource + `"`,
		`Bearer resource_metadata="unterminated`,
		`Bearer resource_metadata="` + resource + `", resource_metadata="` + resource + `"`,
		`Bearer resource_metadata="` + resource + `", Bearer resource_metadata="` + resource + `"`,
		`Bearer scope="mcp"`, `Bearer resource_metadata=""`,
		`Bearer resource_metadata="` + resource + `" extra`,
		"Bearer resource_metadata=\"" + resource + "\"\r\nInjected: value",
	} {
		if _, err := BearerResourceMetadata([]string{header}); err == nil {
			t.Fatalf("accepted malformed challenge %q", header)
		}
	}
}
