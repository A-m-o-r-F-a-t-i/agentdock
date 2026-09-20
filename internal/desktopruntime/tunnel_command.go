package desktopruntime

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/desktopcontrol"
)

// TunnelStatus 是桌面端和 CLI 共享的结构化 Tunnel 状态。
type TunnelStatus struct {
	Provider         string     `json:"provider,omitempty"`
	Mode             string     `json:"mode"`
	Running          bool       `json:"running"`
	Ready            bool       `json:"ready"`
	StartupEnabled   bool       `json:"startup_enabled"`
	PublicURL        string     `json:"public_url,omitempty"`
	Installed        bool       `json:"installed,omitempty"`
	Configured       bool       `json:"configured,omitempty"`
	FunnelEnabled    bool       `json:"funnel_enabled,omitempty"`
	BackendState     string     `json:"backend_state,omitempty"`
	DeviceName       string     `json:"device_name,omitempty"`
	DNSName          string     `json:"dns_name,omitempty"`
	KeyExpiry        *time.Time `json:"key_expiry,omitempty"`
	LocalOrigin      string     `json:"local_origin,omitempty"`
	BinaryPath       string     `json:"binary_path,omitempty"`
	Diagnostic       string     `json:"diagnostic,omitempty"`
	DiagnosticCode   string     `json:"diagnostic_code,omitempty"`
	AuthorizationURL string     `json:"authorization_url,omitempty"`
}

type TunnelConfigureRequest struct {
	Provider        string
	TailscaleBinary string
	RuntimeRoot     string
	Mode            string
	ServerURL       string
	TokenFile       string
}

func RunTunnelCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return tunnelCommandUsageError()
	}

	switch args[0] {
	case "launch":
		runtimeRoot, err := parseLaunchRuntimeRoot("agentdock tunnel launch", args[1:], stderr)
		if err != nil {
			return err
		}
		return platformLaunchTunnel(ctx, runtimeRoot)
	case "status":
		flags := flag.NewFlagSet("agentdock tunnel status", flag.ContinueOnError)
		flags.SetOutput(stderr)
		root := flags.String("runtime-root", "", "AgentDock 桌面运行目录")
		provider := flags.String("provider", "", "可选 tailscale：检测客户端而不改变当前访问模式")
		binary := flags.String("tailscale-binary", "", "Tailscale 客户端绝对路径")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		runtimeRoot := strings.TrimSpace(*root)
		if flags.NArg() != 0 || runtimeRoot == "" {
			return errors.New("用法：agentdock tunnel status --runtime-root <目录> [--provider tailscale] [--tailscale-binary <文件>]")
		}
		selected := strings.ToLower(strings.TrimSpace(*provider))
		if selected != "" && selected != PublicAccessProviderTailscale {
			return errors.New("status --provider 仅接受 tailscale，省略时读取当前访问方式")
		}
		if *binary != "" && selected != PublicAccessProviderTailscale {
			return errors.New("--tailscale-binary requires --provider tailscale")
		}
		var status TunnelStatus
		var err error
		if selected == PublicAccessProviderTailscale {
			status, err = platformTailscaleStatus(ctx, runtimeRoot, strings.TrimSpace(*binary))
		} else if manifest, loadErr := Load(filepath.Join(runtimeRoot, "runtime.json")); loadErr == nil && manifest.EffectivePublicAccess().Provider == PublicAccessProviderTailscale {
			// An older Core can still be running during an upgrade. Its IPC view
			// only understands the legacy none projection, so read native state.
			status, err = platformTunnelStatus(ctx, runtimeRoot)
		} else {
			err = desktopcontrol.Call(ctx, runtimeRoot, "tunnel.status", controlActionParams{RuntimeRoot: runtimeRoot}, &status)
			if err != nil {
				status, err = platformTunnelStatus(ctx, runtimeRoot)
			}
		}
		if err != nil {
			return err
		}
		if status.Provider == "" {
			status.Provider = PublicAccessProviderNone
			if status.Mode == "quick" || status.Mode == "named" {
				status.Provider = PublicAccessProviderCloudflare
			}
		}
		return json.NewEncoder(stdout).Encode(status)
	case "start", "stop", "restart", "regenerate":
		action := args[0]
		runtimeRoot, err := parseRuntimeRoot("agentdock tunnel "+action, args[1:], stderr)
		if err != nil {
			return err
		}
		// 写操作可能重启当前核心，必须由独立控制进程直接调用系统适配器；
		// IPC 仅用于不会改变服务生命周期的状态读取。
		if err := platformTunnelAction(ctx, runtimeRoot, action); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(serviceCommandResult{Action: action, Completed: true})
	case "configure":
		flags := flag.NewFlagSet("agentdock tunnel configure", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runtimeRoot := flags.String("runtime-root", "", "AgentDock 桌面运行目录")
		mode := flags.String("mode", "", "访问模式：none、local、quick、named 或 funnel")
		provider := flags.String("provider", "", "公网提供方：none、cloudflare 或 tailscale")
		binary := flags.String("tailscale-binary", "", "Tailscale 客户端绝对路径")
		serverURL := flags.String("server-url", "", "Named Tunnel HTTPS Origin")
		tokenFile := flags.String("token-file", "", "临时 Tunnel Token 文件")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*runtimeRoot) == "" {
			return errors.New("用法：agentdock tunnel configure --runtime-root <目录> [--provider <none|cloudflare|tailscale>] --mode <none|local|quick|named|funnel> [--server-url <HTTPS Origin>] [--token-file <文件>] [--tailscale-binary <文件>]")
		}
		request, err := normalizeTunnelConfigureRequest(TunnelConfigureRequest{
			Provider:        *provider,
			TailscaleBinary: *binary,
			RuntimeRoot:     *runtimeRoot,
			Mode:            *mode,
			ServerURL:       strings.TrimSpace(*serverURL),
			TokenFile:       strings.TrimSpace(*tokenFile),
		})
		if err != nil {
			return err
		}
		if request.Provider == PublicAccessProviderTailscale && runtime.GOOS != "windows" {
			return errors.New("当前平台不支持原生 Tailscale Funnel 管理；首期仅支持 Windows Desktop")
		}
		if err := platformConfigureTunnel(ctx, request); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(serviceCommandResult{Action: "configure", Completed: true})
	case "autostart":
		flags := flag.NewFlagSet("agentdock tunnel autostart", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runtimeRoot := flags.String("runtime-root", "", "AgentDock 桌面运行目录")
		enabled := flags.String("enabled", "", "是否启用：true 或 false")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*runtimeRoot) == "" {
			return errors.New("用法：agentdock tunnel autostart --runtime-root <目录> --enabled <true|false>")
		}
		shouldEnable, err := parseCommandBoolean("tunnel autostart", *enabled)
		if err != nil {
			return err
		}
		if err := platformSetTunnelAutostart(ctx, *runtimeRoot, shouldEnable); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(serviceCommandResult{Action: "autostart", Completed: true})
	default:
		return tunnelCommandUsageError()
	}
}

func parseLaunchRuntimeRoot(name string, args []string, stderr io.Writer) (string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	runtimeRoot := flags.String("runtime-root", DefaultRuntimeRoot(), "AgentDock 桌面运行目录")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*runtimeRoot) == "" {
		return "", errors.New("用法：" + name + " --runtime-root <目录>")
	}
	return strings.TrimSpace(*runtimeRoot), nil
}

func parseCommandBoolean(command, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New(command + " 的 enabled 必须是 true 或 false")
	}
}

func tunnelCommandUsageError() error {
	return errors.New("用法：agentdock tunnel <launch|status|start|stop|restart|regenerate|configure|autostart> --runtime-root <目录>")
}
