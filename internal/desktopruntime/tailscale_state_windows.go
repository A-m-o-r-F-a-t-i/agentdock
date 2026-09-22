//go:build windows

package desktopruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

func loadTailscaleState(root string) (*tailscaleFunnelState, error) {
	file, err := os.Open(filepath.Join(root, tailscaleStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16*1024 {
		return nil, tailscaleProblem("invalid_ownership", "AgentDock Funnel 所有权文件无效或过大")
	}
	data, err := io.ReadAll(io.LimitReader(file, 16*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16*1024 {
		return nil, tailscaleProblem("invalid_ownership", "AgentDock Funnel 所有权文件超过上限")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state tailscaleFunnelState
	if err := decoder.Decode(&state); err != nil {
		return nil, tailscaleProblem("invalid_ownership", "无法解析 AgentDock Funnel 所有权记录，未修改映射")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, tailscaleProblem("invalid_ownership", "AgentDock Funnel 所有权记录包含多余数据")
	}
	if err := state.validate(); err != nil {
		return nil, err
	}
	return &state, nil
}

func saveTailscaleState(root string, state *tailscaleFunnelState) error {
	if err := state.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(root, tailscaleStateFile), append(data, '\n'), 0o600)
}

func tailscaleStatusError(status TunnelStatus, err error) TunnelStatus {
	status.Ready = false
	status.DiagnosticCode = tailscaleDiagnosticCode(err)
	status.Diagnostic = err.Error()
	status.Phase = "Failed"
	if status.DiagnosticCode == "not_configured" || status.DiagnosticCode == "disabled" {
		status.Phase = "Idle"
	}
	if status.DiagnosticCode == "verification_pending" {
		status.Phase = "VerifyingPublic"
	}
	if status.DiagnosticCode == "public_unreachable" {
		status.Phase = "Degraded"
	}
	if status.DiagnosticCode == "funnel_permission_required" {
		status.Phase = "NeedsApproval"
		status.AuthorizationURL = tailscaleAuthorizationURL
	}
	return status
}

func platformTailscaleStatus(ctx context.Context, root, binary string) (TunnelStatus, error) {
	runtime, err := loadTunnelRuntime(root)
	if err != nil {
		return TunnelStatus{}, err
	}
	status := TunnelStatus{Provider: PublicAccessProviderTailscale, Mode: "funnel", LocalOrigin: runtime.localOrigin()}
	if binary == "" {
		binary = runtime.manifest.TailscaleBinary
	}
	binary, err = findTailscaleBinary(binary)
	if err != nil {
		return tailscaleStatusError(status, err), nil
	}
	return inspectTailscaleRuntime(ctx, runtime, binary, newWindowsTailscaleClient(binary)), nil
}

func inspectTailscaleRuntime(ctx context.Context, runtime tunnelRuntime, binary string, client tailscaleClient) TunnelStatus {
	status := TunnelStatus{Provider: PublicAccessProviderTailscale, Mode: "funnel", Installed: true, BinaryPath: binary, LocalOrigin: runtime.localOrigin()}
	node, err := client.readNode(ctx)
	if err != nil {
		return tailscaleStatusError(status, err)
	}
	status.BackendState, status.DeviceName, status.DNSName, status.KeyExpiry = node.BackendState, node.HostName, node.DNSName, node.KeyExpiry
	if node.DNSName != "" {
		status.PublicURL = "https://" + node.DNSName
	}
	if err := node.checkFunnelReady(time.Now()); err != nil {
		return tailscaleStatusError(status, err)
	}
	config, err := client.readServe(ctx)
	if err != nil {
		return tailscaleStatusError(status, err)
	}
	state, err := loadTailscaleState(runtime.root)
	if err != nil {
		return tailscaleStatusError(status, err)
	}
	hostPort := node.DNSName + ":" + tailscaleFunnelPort
	status.FunnelEnabled = config.AllowFunnel[hostPort]
	status.Running = status.FunnelEnabled && config.handler(hostPort, "/").isProxy(runtime.localOrigin())
	status.Configured = state != nil
	if _, err := prepareTailscaleMapping(node, config, runtime.localOrigin(), state, true); err != nil {
		return tailscaleStatusError(status, err)
	}
	if state == nil {
		code, message := "not_configured", "客户端已就绪，应用 Tailscale 模式后由 AgentDock 管理根路径映射"
		if config.handler(hostPort, "/mcp").isProxy(runtime.localOrigin() + "/mcp") {
			code, message = "migration_required", "检测到临时 /mcp 映射，应用后将迁移为完整 Origin 转发"
		}
		return tailscaleStatusError(status, tailscaleProblem(code, message))
	}
	status.StartupEnabled = state.Enabled && status.Running
	if !state.Enabled {
		return tailscaleStatusError(status, tailscaleProblem("disabled", "AgentDock Funnel 已停用"))
	}
	if !status.Running {
		return tailscaleStatusError(status, tailscaleProblem("mapping_missing", "AgentDock Funnel 根映射未运行或本机目标已变化"))
	}
	origin, err := readTrimmedText(runtime.files.serverURL)
	if err != nil {
		return tailscaleStatusError(status, err)
	}
	access := runtime.manifest.EffectivePublicAccess()
	if access.Provider != PublicAccessProviderTailscale || access.URL != status.PublicURL || origin != status.PublicURL || state.LocalOrigin != runtime.localOrigin() {
		return tailscaleStatusError(status, tailscaleProblem("origin_mismatch", "Funnel 映射与 AgentDock Origin 配置不一致，请重新应用访问模式"))
	}
	if !testHealth(ctx, runtime.localOrigin()+"/healthz") {
		return tailscaleStatusError(status, tailscaleProblem("core_unhealthy", "Funnel 已配置，但 AgentDock 本机健康检查未通过"))
	}
	status.LocalReady = true
	status.VerifiedAt = state.VerifiedAt
	if state.Pending || state.VerifiedAt == nil {
		return tailscaleStatusError(status, tailscaleProblem("verification_pending", "本地配置已完成，公网验证中。无需重新应用或重建映射。"))
	}
	status.Ready = true
	status.Phase = "Ready"
	if node.KeyExpiry != nil && !node.KeyExpiry.IsZero() && time.Until(*node.KeyExpiry) < 14*24*time.Hour {
		status.DiagnosticCode, status.Diagnostic = "key_expiring", "Tailscale 设备密钥将在 14 天内到期，请在官方客户端安排重新认证"
	}
	return status
}
