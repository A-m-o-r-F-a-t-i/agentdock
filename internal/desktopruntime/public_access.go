package desktopruntime

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

const (
	PublicAccessProviderNone       = "none"
	PublicAccessProviderCloudflare = "cloudflare"
	PublicAccessProviderTailscale  = "tailscale"
)

// PublicAccess is the provider-neutral projection used by desktop consumers.
// Legacy tunnel_mode remains restricted to none/quick/named for downgrade safety.
type PublicAccess struct {
	Provider string `json:"provider"`
	Mode     string `json:"mode"`
	URL      string `json:"url,omitempty"`
}

func (manifest Manifest) EffectivePublicAccess() PublicAccess {
	if manifest.PublicAccessProvider != "" {
		return PublicAccess{manifest.PublicAccessProvider, manifest.PublicAccessMode, manifest.PublicAccessURL}
	}
	if manifest.TunnelMode == "quick" || manifest.TunnelMode == "named" {
		return PublicAccess{PublicAccessProviderCloudflare, manifest.TunnelMode, manifest.PublicURL}
	}
	return PublicAccess{Provider: PublicAccessProviderNone, Mode: "local"}
}

func (manifest *Manifest) setPublicAccess(provider, mode, origin string) {
	manifest.PublicAccessProvider = provider
	manifest.PublicAccessMode = mode
	manifest.PublicAccessURL = origin
	manifest.TunnelMode = "none"
	manifest.PublicURL = ""
	if provider == PublicAccessProviderCloudflare {
		manifest.TunnelMode = mode
		manifest.PublicURL = origin
	}
}

func (manifest Manifest) validatePublicAccess() error {
	if manifest.TailscaleBinary != "" && !filepath.IsAbs(manifest.TailscaleBinary) {
		return errors.New("tailscale_binary must be an absolute path")
	}
	switch manifest.PublicAccessProvider {
	case "":
		if manifest.PublicAccessMode != "" || manifest.PublicAccessURL != "" {
			return errors.New("public_access_mode and public_access_url require public_access_provider")
		}
		return nil
	case PublicAccessProviderNone:
		if manifest.PublicAccessMode != "local" || manifest.PublicAccessURL != "" || manifest.TunnelMode != "none" {
			return errors.New("none provider requires local mode and an empty public origin")
		}
	case PublicAccessProviderCloudflare:
		if manifest.PublicAccessMode != "quick" && manifest.PublicAccessMode != "named" {
			return errors.New("cloudflare provider requires quick or named mode")
		}
		if manifest.TunnelMode != manifest.PublicAccessMode || manifest.PublicURL != manifest.PublicAccessURL {
			return errors.New("Cloudflare public access must match its legacy tunnel projection")
		}
	case PublicAccessProviderTailscale:
		if manifest.PublicAccessMode != "funnel" || manifest.TunnelMode != "none" || manifest.PublicURL != "" {
			return errors.New("Tailscale Funnel requires legacy tunnel_mode=none and an empty legacy public_url")
		}
		if _, err := normalizeTailscaleOrigin(manifest.PublicAccessURL); err != nil {
			return fmt.Errorf("invalid Tailscale public_access_url: %w", err)
		}
	default:
		return fmt.Errorf("unsupported public access provider: %s", manifest.PublicAccessProvider)
	}
	return nil
}

func normalizeTunnelConfigureRequest(request TunnelConfigureRequest) (TunnelConfigureRequest, error) {
	request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
	request.Mode = strings.ToLower(strings.TrimSpace(request.Mode))
	request.ServerURL = strings.TrimSpace(request.ServerURL)
	request.TokenFile = strings.TrimSpace(request.TokenFile)
	request.TailscaleBinary = strings.TrimSpace(request.TailscaleBinary)
	if request.Provider == "" {
		switch request.Mode {
		case "none":
			request.Provider = PublicAccessProviderNone
		case "quick", "named":
			request.Provider = PublicAccessProviderCloudflare
		default:
			return request, errors.New("legacy mode must be none, quick or named; Funnel requires --provider tailscale --mode funnel")
		}
	}
	switch request.Provider {
	case PublicAccessProviderNone:
		if request.Mode != "local" && request.Mode != "none" {
			return request, errors.New("none provider requires mode local")
		}
		request.Mode = "none"
	case PublicAccessProviderCloudflare:
		if request.Mode != "quick" && request.Mode != "named" {
			return request, errors.New("cloudflare provider requires mode quick or named")
		}
	case PublicAccessProviderTailscale:
		if request.Mode != "funnel" {
			return request, errors.New("tailscale provider requires mode funnel")
		}
		if request.ServerURL != "" {
			return request, errors.New("Tailscale 域名从当前设备自动读取，不能传入 --server-url")
		}
	default:
		return request, fmt.Errorf("unsupported public access provider: %s", request.Provider)
	}
	if request.TokenFile != "" && (request.Provider != PublicAccessProviderCloudflare || request.Mode != "named") {
		return request, errors.New("--token-file is only supported by Cloudflare named mode; Tailscale does not use a Tunnel Token")
	}
	if request.TailscaleBinary != "" && request.Provider != PublicAccessProviderTailscale {
		return request, errors.New("--tailscale-binary requires provider tailscale")
	}
	return request, nil
}

func normalizeTailscaleDNSName(value string) (string, error) {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	labels := strings.Split(name, ".")
	if len(name) > 253 || len(labels) < 4 || !strings.HasSuffix(name, ".ts.net") {
		return "", errors.New("Tailscale DNS name must be a device name under .ts.net")
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid Tailscale DNS label")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
				return "", errors.New("invalid character in Tailscale DNS name")
			}
		}
	}
	return name, nil
}

func normalizeTailscaleOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("Tailscale public origin must be an HTTPS device origin without path, port or credentials")
	}
	name, err := normalizeTailscaleDNSName(parsed.Host)
	if err != nil {
		return "", err
	}
	return "https://" + name, nil
}
