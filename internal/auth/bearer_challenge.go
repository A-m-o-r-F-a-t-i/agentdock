package auth

import (
	"errors"
	"strings"
)

// BearerResourceMetadata parses the Bearer challenge instead of comparing its
// serialized header. Parameter order, case, whitespace, quoted commas and other
// authentication schemes are allowed. Ambiguous or malformed challenges fail closed.
func BearerResourceMetadata(headers []string) (string, error) {
	invalid := errors.New("invalid or ambiguous Bearer resource metadata challenge")
	params := map[string]string{}
	bearer, count := false, 0
	for _, header := range headers {
		if len(header) > 8192 {
			return "", invalid
		}
		parts, ok := challengeParts(header)
		if !ok {
			return "", invalid
		}
		for _, part := range parts {
			part = strings.Trim(part, " \t")
			if part == "" {
				continue
			}
			end := 0
			for end < len(part) && challengeToken(part[end]) {
				end++
			}
			if end == 0 {
				return "", invalid
			}
			rest := strings.TrimLeft(part[end:], " \t")
			if !strings.HasPrefix(rest, "=") {
				bearer = strings.EqualFold(part[:end], "Bearer")
				if !bearer {
					continue
				}
				count++
				if count != 1 || rest == "" || end == len(part) || (part[end] != ' ' && part[end] != '\t') {
					return "", invalid
				}
				part = rest
			}
			if !bearer {
				continue
			}
			key, value, ok := challengeParameter(part)
			if !ok {
				return "", invalid
			}
			if _, exists := params[key]; exists {
				return "", invalid
			}
			params[key] = value
		}
		// Separate field lines cannot accidentally append parameters to a different challenge.
		bearer = false
	}
	if count != 1 || params["resource_metadata"] == "" {
		return "", invalid
	}
	return params["resource_metadata"], nil
}

func challengeToken(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b))
}

func challengeParts(value string) ([]string, bool) {
	parts := []string{}
	start, quoted, escaped := 0, false, false
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < 32 && b != '\t' || b == 127 {
			return nil, false
		}
		if escaped {
			escaped = false
			continue
		}
		if quoted && b == '\\' {
			escaped = true
			continue
		}
		if b == '"' {
			quoted = !quoted
		}
		if b == ',' && !quoted {
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil, false
	}
	return append(parts, value[start:]), true
}

func challengeParameter(part string) (string, string, bool) {
	end := 0
	for end < len(part) && challengeToken(part[end]) {
		end++
	}
	if end == 0 {
		return "", "", false
	}
	key := strings.ToLower(part[:end])
	rest := strings.TrimLeft(part[end:], " \t")
	if !strings.HasPrefix(rest, "=") {
		return "", "", false
	}
	rest = strings.TrimSpace(rest[1:])
	if rest == "" {
		return "", "", false
	}
	if rest[0] != '"' {
		for i := range len(rest) {
			if !challengeToken(rest[i]) {
				return "", "", false
			}
		}
		return key, rest, true
	}
	var value strings.Builder
	for i := 1; i < len(rest); i++ {
		if rest[i] == '"' {
			return key, value.String(), strings.TrimSpace(rest[i+1:]) == ""
		}
		if rest[i] == '\\' {
			i++
			if i >= len(rest) {
				return "", "", false
			}
		}
		value.WriteByte(rest[i])
	}
	return "", "", false
}
