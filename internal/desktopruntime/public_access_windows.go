//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"
)

// Hooks keep lifecycle failures testable without a production environment flag
// that could skip authentication, public verification or ownership checks.
type publicAccessHooks struct {
	waitForPublic       bool
	findBinary          func(string) (string, error)
	client              func(string) tailscaleClient
	ensureCredentials   func(string) error
	coreRunning         func(context.Context, string) (bool, error)
	restartCore         func(context.Context, string) error
	stopCore            func(context.Context, string) error
	localHealthy        func(context.Context, string) bool
	cloudflareRunning   func(string) (bool, error)
	stopCloudflare      func(context.Context, tunnelRuntime) error
	startCloudflare     func(context.Context, tunnelRuntime) error
	startupEnabled      func(Manifest) (bool, error)
	setStartup          func(context.Context, string, bool) error
	configureCloudflare func(context.Context, TunnelConfigureRequest) error
	verifyOrigin        func(context.Context, tunnelRuntime, string) error
	saveState           func(string, *tailscaleFunnelState) error
	saveManifest        func(tunnelRuntime, string, string) error
}

func defaultPublicAccessHooks() publicAccessHooks {
	return publicAccessHooks{
		findBinary: findTailscaleBinary, client: newWindowsTailscaleClient,
		ensureCredentials: ensureDesktopCredentials,
		coreRunning: func(ctx context.Context, root string) (bool, error) {
			status, err := platformServiceStatus(ctx, root)
			return status.Running, err
		},
		restartCore:       restartTunnelCore,
		stopCore:          func(ctx context.Context, root string) error { return platformServiceAction(ctx, root, "stop") },
		localHealthy:      testHealth,
		cloudflareRunning: processRunningAtPath, stopCloudflare: stopCloudflareTunnel, startCloudflare: startCloudflareTunnel,
		startupEnabled: tunnelAutostartEnabled, setStartup: platformSetTunnelAutostart,
		configureCloudflare: configureCloudflareTunnel,
		verifyOrigin: func(ctx context.Context, runtime tunnelRuntime, origin string) error {
			if !testHealth(ctx, runtime.localOrigin()+"/healthz") {
				return tailscaleProblem("core_unhealthy", "AgentDock 本机健康检查失败")
			}
			token, err := readProtectedText(filepath.Join(runtime.root, "auth-token.dpapi"), "agentdock.startup.v1")
			if err != nil {
				return errors.New("无法读取 AgentDock Bearer Token，未执行公网认证验证")
			}
			return verifyTailscalePublicOrigin(ctx, origin, token, newTailscaleHTTPClient())
		},
		saveState:    saveTailscaleState,
		saveManifest: func(runtime tunnelRuntime, mode, origin string) error { return runtime.updateManifest(mode, origin) },
	}
}

func platformConfigureTunnel(ctx context.Context, request TunnelConfigureRequest) error {
	request, err := normalizeTunnelConfigureRequest(request)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(strings.TrimSpace(request.RuntimeRoot))
	if err != nil {
		return err
	}
	request.RuntimeRoot = root
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	ctx, finishAction, err := tunnelActionContext(ctx, root, false)
	if err != nil {
		return err
	}
	defer finishAction()
	release, err := acquireTunnelOperation(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	runtime, err := loadTunnelRuntime(root)
	if err != nil {
		return err
	}
	return configurePublicAccess(ctx, runtime, request, defaultPublicAccessHooks())
}

func configurePublicAccess(ctx context.Context, runtime tunnelRuntime, request TunnelConfigureRequest, hooks publicAccessHooks) error {
	state, err := loadTailscaleState(runtime.root)
	if err != nil {
		return err
	}
	if request.Provider != PublicAccessProviderTailscale && runtime.mode != "funnel" && state == nil {
		return hooks.configureCloudflare(ctx, request)
	}
	binary := request.TailscaleBinary
	if binary == "" {
		binary = runtime.manifest.TailscaleBinary
	}
	binary, err = hooks.findBinary(binary)
	if err != nil {
		return err
	}
	return withTailscaleMutationLock(ctx, binary, func() error {
		if request.Provider == PublicAccessProviderTailscale {
			hooks.waitForPublic = request.WaitForPublic
			return configureTailscaleAccess(ctx, runtime, binary, true, hooks)
		}
		return leaveTailscaleAccess(ctx, runtime, binary, request, hooks)
	})
}

func withTailscaleMutationLock(ctx context.Context, binary string, operation func() error) error {
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	// AgentDock runtime roots using the same installed client coordinate here.
	// This name is only a mutex identity; no file is created beside Tailscale.
	key := filepath.Join(filepath.Dir(binary), ".agentdock-funnel-443")
	release, err := acquireTunnelOperation(ctx, key)
	if err != nil {
		return err
	}
	defer release()
	return operation()
}

func startTunnel(ctx context.Context, runtime tunnelRuntime) error {
	if runtime.mode != "funnel" {
		return startCloudflareTunnel(ctx, runtime)
	}
	hooks := defaultPublicAccessHooks()
	binary, err := hooks.findBinary(runtime.manifest.TailscaleBinary)
	if err != nil {
		return err
	}
	return withTailscaleMutationLock(ctx, binary, func() error { return configureTailscaleAccess(ctx, runtime, binary, false, hooks) })
}

func stopTunnel(ctx context.Context, runtime tunnelRuntime) error {
	if runtime.mode != "funnel" {
		return stopCloudflareTunnel(ctx, runtime)
	}
	hooks := defaultPublicAccessHooks()
	binary, err := hooks.findBinary(runtime.manifest.TailscaleBinary)
	if err != nil {
		return err
	}
	return withTailscaleMutationLock(ctx, binary, func() error { return stopTailscaleAccess(ctx, runtime, binary, hooks) })
}

type publicAccessSnapshot struct {
	files             []fileSnapshot
	coreRunning       bool
	cloudflareRunning bool
	startupEnabled    bool
}

func capturePublicAccessSnapshot(ctx context.Context, runtime tunnelRuntime, hooks publicAccessHooks) (publicAccessSnapshot, error) {
	var snapshot publicAccessSnapshot
	paths := []string{runtime.files.manifest, runtime.files.mode, runtime.files.serverURL, runtime.files.namedServerURL, runtime.files.quickURL, runtime.files.token}
	for _, name := range []string{tailscaleStateFile, "control-panel-settings.json", "auth-token.dpapi", "oauth-password.dpapi", "oauth-token-secret.dpapi", credentialOwnerSIDFile} {
		paths = append(paths, filepath.Join(runtime.root, name))
	}
	for _, path := range paths {
		file, err := snapshotFile(path)
		if err != nil {
			return snapshot, fmt.Errorf("备份公网访问配置失败: %w", err)
		}
		snapshot.files = append(snapshot.files, file)
	}
	var err error
	snapshot.coreRunning, err = hooks.coreRunning(ctx, runtime.root)
	if err != nil {
		return snapshot, err
	}
	snapshot.cloudflareRunning, err = hooks.cloudflareRunning(runtime.manifest.CloudflaredBinary)
	if err != nil {
		return snapshot, err
	}
	snapshot.startupEnabled, err = hooks.startupEnabled(runtime.manifest)
	return snapshot, err
}

func rollbackPublicAccess(ctx context.Context, runtime tunnelRuntime, snapshot publicAccessSnapshot, change *tailscaleMappingChange, client tailscaleClient, hooks publicAccessHooks, coreTouched, cloudflareTouched bool, pending *tailscaleFunnelState) error {
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	var failures error
	if cloudflareTouched {
		failures = errors.Join(failures, hooks.stopCloudflare(recovery, runtime))
	}
	mappingErr := change.rollback(recovery, client)
	failures = errors.Join(failures, mappingErr)
	fileErr := restoreSnapshots(snapshot.files)
	failures = errors.Join(failures, fileErr)
	if mappingErr != nil && pending != nil {
		// Keep a non-ready ownership record if cleanup could not be confirmed.
		// Losing it would turn a partially installed route into an orphan.
		pending.Pending, pending.VerifiedAt = true, nil
		failures = errors.Join(failures, hooks.saveState(runtime.root, pending))
	}
	if fileErr != nil {
		return failures
	}
	oldRuntime, err := loadTunnelRuntime(runtime.root)
	if err != nil {
		return errors.Join(failures, err)
	}
	if cloudflareTouched {
		failures = errors.Join(failures, hooks.setStartup(recovery, runtime.root, snapshot.startupEnabled))
	}
	if coreTouched {
		if snapshot.coreRunning {
			failures = errors.Join(failures, hooks.restartCore(recovery, runtime.root))
		} else {
			failures = errors.Join(failures, hooks.stopCore(recovery, runtime.root))
		}
	}
	if cloudflareTouched && snapshot.cloudflareRunning && (oldRuntime.mode == "quick" || oldRuntime.mode == "named") {
		failures = errors.Join(failures, hooks.startCloudflare(recovery, oldRuntime))
	}
	return failures
}

func configureTailscaleAccess(ctx context.Context, runtime tunnelRuntime, binary string, allowAdoption bool, hooks publicAccessHooks) (resultErr error) {
	client := hooks.client(binary)
	node, config, err := client.observe(ctx)
	if err != nil {
		return err
	}
	previous, err := loadTailscaleState(runtime.root)
	if err != nil {
		return err
	}
	change, err := prepareTailscaleMapping(node, config, runtime.localOrigin(), previous, allowAdoption)
	if err != nil {
		return err
	}
	origin := "https://" + node.DNSName
	if len(change.mutations) == 0 && previous != nil && previous.Enabled && previous.LocalOrigin == runtime.localOrigin() && (!hooks.waitForPublic || !previous.Pending && previous.VerifiedAt != nil) {
		currentOrigin, readErr := readTrimmedText(runtime.files.serverURL)
		cfRunning, processErr := hooks.cloudflareRunning(runtime.manifest.CloudflaredBinary)
		startup, startupErr := hooks.startupEnabled(runtime.manifest)
		if readErr == nil && processErr == nil && startupErr == nil && !cfRunning && !startup && currentOrigin == origin && runtime.manifest.EffectivePublicAccess().URL == origin && hooks.localHealthy(ctx, runtime.localOrigin()+"/healthz") {
			return nil
		}
	}
	snapshot, err := capturePublicAccessSnapshot(ctx, runtime, hooks)
	if err != nil {
		return err
	}
	pending := newTailscaleFunnelState(node, runtime.localOrigin())
	for _, mutation := range change.mutations {
		if mutation.path == "/mcp" && mutation.before != nil {
			if mutation.before.Proxy != pending.LocalOrigin+"/mcp" {
				return tailscaleProblem("pending_migration", "请先完成当前端口的临时 MCP 映射迁移，再修改 AgentDock 端口")
			}
			pending.LegacyMCPProxy = mutation.before.Proxy
		}
	}
	coreTouched, cloudflareTouched := false, false
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, rollbackPublicAccess(ctx, runtime, snapshot, change, client, hooks, coreTouched, cloudflareTouched, pending))
		}
	}()
	if err := hooks.ensureCredentials(runtime.root); err != nil {
		return err
	}
	if err := preserveNamedServerURL(runtime); err != nil {
		return err
	}
	if err := hooks.saveState(runtime.root, pending); err != nil {
		return err
	}
	cloudflareTouched = true
	if err := hooks.stopCloudflare(ctx, runtime); err != nil {
		return err
	}
	if err := hooks.setStartup(ctx, runtime.root, false); err != nil {
		return err
	}
	if err := change.apply(ctx, client); err != nil {
		return err
	}
	if err := clearActivePublicURL(runtime.files); err != nil {
		return err
	}
	if err := writeRuntimeText(runtime.files.mode, "none"); err != nil {
		return err
	}
	if err := writeRuntimeText(runtime.files.serverURL, origin); err != nil {
		return err
	}
	coreTouched = true
	if err := hooks.restartCore(ctx, runtime.root); err != nil {
		return err
	}
	if !hooks.localHealthy(ctx, runtime.localOrigin()+"/healthz") {
		return tailscaleProblem("core_unhealthy", "本地配置未通过健康检查，尚未进入公网验证")
	}
	if hooks.waitForPublic {
		if err := hooks.verifyOrigin(ctx, runtime, origin); err != nil {
			return err
		}
	}
	verifiedNode, verifiedConfig, err := client.observe(ctx)
	if err != nil {
		return err
	}
	if err := change.checkIdentity(verifiedNode); err != nil {
		return err
	}
	pending.LegacyMCPProxy = ""
	if _, err := prepareTailscaleMapping(verifiedNode, verifiedConfig, runtime.localOrigin(), pending, false); err != nil {
		return err
	}
	hostPort := node.DNSName + ":" + tailscaleFunnelPort
	if !verifiedConfig.AllowFunnel[hostPort] || !verifiedConfig.handler(hostPort, "/").isProxy(runtime.localOrigin()) {
		return tailscaleProblem("verification_failed", "验证期间 AgentDock Funnel 根映射发生变化")
	}
	if !sameTailscaleServe(unownedTailscaleServe(change.before, hostPort, change.paths()), unownedTailscaleServe(verifiedConfig, hostPort, change.paths())) {
		return tailscaleProblem("configuration_changed", "验证期间其他 Tailscale 配置发生变化，已停止提交")
	}
	now := time.Now().UTC()
	if hooks.waitForPublic {
		pending.Pending, pending.VerifiedAt = false, &now
	}
	if err := hooks.saveState(runtime.root, pending); err != nil {
		return err
	}
	runtime.manifest.TailscaleBinary = binary
	// This commits local ownership and the configured URL. A pending record is
	// not public readiness; the separate verifier must establish that fact.
	if err := hooks.saveManifest(runtime, "funnel", origin); err != nil {
		return err
	}
	return nil
}

func stopTailscaleAccess(ctx context.Context, runtime tunnelRuntime, binary string, hooks publicAccessHooks) (resultErr error) {
	state, err := loadTailscaleState(runtime.root)
	if err != nil {
		return err
	}
	if state == nil {
		return tailscaleProblem("ownership_required", "缺少 AgentDock Funnel 所有权记录，未删除任何映射")
	}
	client := hooks.client(binary)
	node, config, err := client.observe(ctx)
	if err != nil {
		return err
	}
	change, err := prepareTailscaleStop(node, config, state)
	if err != nil {
		return err
	}
	snapshot, err := snapshotFile(filepath.Join(runtime.root, tailscaleStateFile))
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			defer cancel()
			mappingErr := change.rollback(recovery, client)
			resultErr = errors.Join(resultErr, mappingErr, restoreSnapshots([]fileSnapshot{snapshot}))
			if mappingErr != nil {
				state.Pending, state.VerifiedAt = true, nil
				resultErr = errors.Join(resultErr, hooks.saveState(runtime.root, state))
			}
		}
	}()
	if err := change.apply(ctx, client); err != nil {
		return err
	}
	state.Enabled, state.Pending = false, false
	return hooks.saveState(runtime.root, state)
}

func leaveTailscaleAccess(ctx context.Context, runtime tunnelRuntime, binary string, request TunnelConfigureRequest, hooks publicAccessHooks) (resultErr error) {
	state, err := loadTailscaleState(runtime.root)
	if err != nil {
		return err
	}
	if state == nil {
		return tailscaleProblem("ownership_required", "缺少 AgentDock Funnel 所有权记录，无法安全切换公网入口")
	}
	client := hooks.client(binary)
	node, config, err := client.observe(ctx)
	if err != nil {
		return err
	}
	change, err := prepareTailscaleStop(node, config, state)
	if err != nil {
		return err
	}
	snapshot, err := capturePublicAccessSnapshot(ctx, runtime, hooks)
	if err != nil {
		return err
	}
	coreTouched, cloudflareTouched := false, false
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, rollbackPublicAccess(ctx, runtime, snapshot, change, client, hooks, coreTouched, cloudflareTouched, state))
		}
	}()
	if err := change.apply(ctx, client); err != nil {
		return err
	}
	state.Enabled, state.Pending = false, false
	if err := hooks.saveState(runtime.root, state); err != nil {
		return err
	}
	coreTouched, cloudflareTouched = true, true
	if err := hooks.configureCloudflare(ctx, request); err != nil {
		return err
	}
	// The old provider is gone and the new one is verified by its own adapter.
	if err := os.Remove(filepath.Join(runtime.root, tailscaleStateFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
