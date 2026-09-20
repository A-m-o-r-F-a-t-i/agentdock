package desktopruntime

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const (
	tailscaleStateFile       = "tailscale-funnel-state.json"
	tailscaleQueryTimeout    = 10 * time.Second
	tailscaleMutationTimeout = 30 * time.Second
)

// This record grants authority over specific local mappings, never the tailnet
// or the Tailscale service. Pending records are not advertised as ready.
type tailscaleFunnelState struct {
	SchemaVersion  int        `json:"schema_version"`
	Managed        bool       `json:"managed"`
	Enabled        bool       `json:"enabled"`
	Pending        bool       `json:"pending,omitempty"`
	PublicOrigin   string     `json:"public_origin"`
	HTTPSPort      int        `json:"https_port"`
	LocalOrigin    string     `json:"local_origin"`
	DeviceID       string     `json:"device_id"`
	DNSName        string     `json:"dns_name"`
	ConfiguredAt   time.Time  `json:"configured_at"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
	LegacyMCPProxy string     `json:"legacy_mcp_proxy,omitempty"`
}

func (state *tailscaleFunnelState) validate() error {
	if state == nil || state.SchemaVersion != 1 || !state.Managed || state.HTTPSPort != 443 || state.DeviceID == "" {
		return tailscaleProblem("invalid_ownership", "AgentDock Funnel 所有权记录无效，拒绝修改映射")
	}
	dns, err := normalizeTailscaleDNSName(state.DNSName)
	if err != nil || dns != state.DNSName || state.PublicOrigin != "https://"+dns {
		return tailscaleProblem("invalid_ownership", "AgentDock Funnel 所有权记录的设备域名不一致")
	}
	if !validTailscaleLocalOrigin(state.LocalOrigin) {
		return tailscaleProblem("invalid_ownership", "AgentDock Funnel 所有权记录包含无效本机目标")
	}
	if state.LegacyMCPProxy != "" && state.LegacyMCPProxy != state.LocalOrigin+"/mcp" {
		return tailscaleProblem("invalid_ownership", "AgentDock Funnel 临时 /mcp 所有权记录无效")
	}
	return nil
}

func validTailscaleLocalOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port >= 1 && port <= 65535 && localTailscaleOrigin(port) == origin
}

func (state *tailscaleFunnelState) matchesNode(node tailscaleNode) bool {
	return state != nil && state.DeviceID == node.ID && state.DNSName == node.DNSName && state.PublicOrigin == "https://"+node.DNSName
}

func newTailscaleFunnelState(node tailscaleNode, target string) *tailscaleFunnelState {
	return &tailscaleFunnelState{
		SchemaVersion: 1, Managed: true, Enabled: true, Pending: true,
		PublicOrigin: "https://" + node.DNSName, HTTPSPort: 443, LocalOrigin: target,
		DeviceID: node.ID, DNSName: node.DNSName, ConfiguredAt: time.Now().UTC(),
	}
}

type tailscaleCommand func(context.Context, time.Duration, ...string) ([]byte, error)
type tailscaleClient struct{ run tailscaleCommand }

func (client tailscaleClient) readNode(ctx context.Context) (tailscaleNode, error) {
	data, err := client.run(ctx, tailscaleQueryTimeout, "status", "--json", "--peers=false")
	if err != nil {
		return tailscaleNode{}, err
	}
	return decodeTailscaleNode(data)
}

func (client tailscaleClient) readServe(ctx context.Context) (*tailscaleServeConfig, error) {
	data, err := client.run(ctx, tailscaleQueryTimeout, "funnel", "status", "--json")
	if err != nil {
		return nil, err
	}
	return decodeTailscaleServe(data)
}

func (client tailscaleClient) observe(ctx context.Context) (tailscaleNode, *tailscaleServeConfig, error) {
	node, err := client.readNode(ctx)
	if err != nil {
		return node, nil, err
	}
	config, err := client.readServe(ctx)
	return node, config, err
}

type tailscalePathMutation struct {
	path         string
	before       *tailscaleHTTPHandler
	after        *tailscaleHTTPHandler
	beforePublic bool
	afterPublic  bool
}

type tailscaleMappingChange struct {
	node      tailscaleNode
	before    *tailscaleServeConfig
	mutations []tailscalePathMutation
	attempted int
}

func prepareTailscaleMapping(node tailscaleNode, config *tailscaleServeConfig, target string, state *tailscaleFunnelState, allowAdoption bool) (*tailscaleMappingChange, error) {
	if err := node.checkFunnelReady(time.Now()); err != nil {
		return nil, err
	}
	if !validTailscaleLocalOrigin(target) {
		return nil, errors.New("Funnel target must be a canonical loopback HTTP origin")
	}
	if state != nil {
		if err := state.validate(); err != nil {
			return nil, err
		}
		if !state.matchesNode(node) {
			return nil, tailscaleProblem("device_changed", "Tailscale 设备或域名已变化，保留原映射并拒绝自动接管")
		}
	} else if !allowAdoption {
		return nil, tailscaleProblem("ownership_required", "尚未配置 AgentDock Funnel，请先应用 Tailscale 访问模式")
	}
	hostPort := node.DNSName + ":" + tailscaleFunnelPort
	if err := config.checkHTTPSPort(hostPort); err != nil {
		return nil, err
	}
	root := config.handler(hostPort, "/")
	if root != nil {
		owned := state != nil && root.isProxy(state.LocalOrigin)
		if !owned && !(allowAdoption && root.isProxy(target)) {
			return nil, tailscaleProblem("mapping_conflict", "HTTPS 443 根路径已指向其他目标，拒绝覆盖: "+safeTailscaleTarget(root))
		}
	}
	legacyTarget := ""
	if web := config.Web[hostPort]; web != nil {
		for path, handler := range web.Handlers {
			if path == "/" {
				continue
			}
			legacyOwned := path == "/mcp" && state != nil && state.LegacyMCPProxy != "" && handler.isProxy(state.LegacyMCPProxy)
			legacyAdoptable := allowAdoption && path == "/mcp" && handler.isProxy(target+"/mcp")
			if legacyOwned || legacyAdoptable {
				legacyTarget = handler.Proxy
				continue
			}
			if reservedAgentDockPath(path) {
				return nil, tailscaleProblem("mapping_conflict", "已有路径覆盖 AgentDock 协议接口，拒绝接管: "+path)
			}
			if !config.AllowFunnel[hostPort] {
				return nil, tailscaleProblem("private_serve_conflict", "HTTPS 443 存在其他私有 Serve 路径，启用 Funnel 会公开它们，已拒绝修改")
			}
		}
	}
	change := &tailscaleMappingChange{node: node, before: cloneTailscaleServe(config)}
	public := config.AllowFunnel[hostPort]
	if !root.isProxy(target) || !public {
		change.mutations = append(change.mutations, tailscalePathMutation{
			path: "/", before: cloneTailscaleHandler(root), after: &tailscaleHTTPHandler{Proxy: target}, beforePublic: public, afterPublic: true,
		})
	}
	if legacyTarget != "" {
		change.mutations = append(change.mutations, tailscalePathMutation{
			path: "/mcp", before: &tailscaleHTTPHandler{Proxy: legacyTarget}, beforePublic: true, afterPublic: true,
		})
	}
	return change, nil
}

// Targets included in diagnostics cannot contain credentials or query strings.
func safeTailscaleTarget(handler *tailscaleHTTPHandler) string {
	if handler == nil {
		return "<empty>"
	}
	parsed, err := url.Parse(handler.Proxy)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<non-proxy handler>"
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
}

func prepareTailscaleStop(node tailscaleNode, config *tailscaleServeConfig, state *tailscaleFunnelState) (*tailscaleMappingChange, error) {
	if err := state.validate(); err != nil {
		return nil, err
	}
	if err := node.checkIdentity(); err != nil {
		return nil, err
	}
	if !state.matchesNode(node) {
		return nil, tailscaleProblem("device_changed", "设备身份已变化，未删除任何 Funnel 映射")
	}
	hostPort := node.DNSName + ":" + tailscaleFunnelPort
	if err := config.checkHTTPSPort(hostPort); err != nil {
		return nil, err
	}
	change := &tailscaleMappingChange{node: node, before: cloneTailscaleServe(config)}
	paths := []struct{ path, target string }{{"/", state.LocalOrigin}}
	if state.LegacyMCPProxy != "" {
		paths = append(paths, struct{ path, target string }{"/mcp", state.LegacyMCPProxy})
	}
	for _, item := range paths {
		handler := config.handler(hostPort, item.path)
		if handler == nil {
			continue
		}
		if !handler.isProxy(item.target) {
			return nil, tailscaleProblem("ownership_conflict", "AgentDock 映射已被修改，保留当前配置: "+item.path)
		}
		// Serve and Funnel share the same HTTP mapping. A changed private/public
		// flag does not expand our ownership beyond this exact handler.
		change.mutations = append(change.mutations, tailscalePathMutation{
			path: item.path, before: cloneTailscaleHandler(handler), beforePublic: config.AllowFunnel[hostPort], afterPublic: config.AllowFunnel[hostPort],
		})
	}
	return change, nil
}

func cloneTailscaleHandler(handler *tailscaleHTTPHandler) *tailscaleHTTPHandler {
	if handler == nil {
		return nil
	}
	clone := *handler
	clone.AcceptAppCaps = append([]string(nil), handler.AcceptAppCaps...)
	return &clone
}

func sameTailscaleHandler(left, right *tailscaleHTTPHandler) bool {
	return reflect.DeepEqual(left, right)
}

func (change *tailscaleMappingChange) apply(ctx context.Context, client tailscaleClient) error {
	node, current, err := client.observe(ctx)
	if err != nil {
		return err
	}
	if err := change.checkIdentity(node); err != nil {
		return err
	}
	if !sameTailscaleServe(change.before, current) {
		return tailscaleProblem("configuration_changed", "Tailscale 配置在检查后已变化，未执行写入，请重新检测")
	}
	for index, mutation := range change.mutations {
		change.attempted = index + 1
		after, err := mutateTailscalePath(ctx, client, change.node, current, mutation.path, mutation.after, mutation.afterPublic)
		if err != nil {
			return err
		}
		current = after
	}
	return nil
}

func (change *tailscaleMappingChange) checkIdentity(node tailscaleNode) error {
	if err := node.checkIdentity(); err != nil {
		return err
	}
	if node.ID != change.node.ID || node.DNSName != change.node.DNSName {
		return tailscaleProblem("device_changed", "Tailscale 设备身份在操作期间变化，已停止修改")
	}
	return nil
}

func (change *tailscaleMappingChange) rollback(ctx context.Context, client tailscaleClient) error {
	if change == nil {
		return nil
	}
	var failures error
	hostPort := change.node.DNSName + ":" + tailscaleFunnelPort
	for index := change.attempted - 1; index >= 0; index-- {
		mutation := change.mutations[index]
		node, current, err := client.observe(ctx)
		if err != nil {
			return errors.Join(failures, fmt.Errorf("读取 Funnel 回滚状态失败: %w", err))
		}
		if err := change.checkIdentity(node); err != nil {
			return errors.Join(failures, err)
		}
		if err := current.checkHTTPSPort(hostPort); err != nil {
			return errors.Join(failures, err)
		}
		live := current.handler(hostPort, mutation.path)
		if sameTailscaleHandler(live, mutation.before) && (live == nil || mutation.after == nil || current.AllowFunnel[hostPort] == mutation.beforePublic) {
			continue
		}
		if !sameTailscaleHandler(live, mutation.after) {
			failures = errors.Join(failures, tailscaleProblem("rollback_conflict", "回滚路径已被其他操作修改，已保留: "+mutation.path))
			continue
		}
		if mutation.before != nil && !mutation.beforePublic && current.AllowFunnel[hostPort] {
			// Making the port private would change any new, foreign public routes.
			// Remove our newly exposed route instead and report the incomplete restore.
			beforeUnowned := unownedTailscaleServe(change.before, hostPort, change.paths())
			currentUnowned := unownedTailscaleServe(current, hostPort, change.paths())
			if !sameTailscaleServe(beforeUnowned, currentUnowned) {
				_, removeErr := mutateTailscalePath(ctx, client, change.node, current, mutation.path, nil, true)
				failures = errors.Join(failures, removeErr, tailscaleProblem("rollback_conflict", "已关闭本次公开的 AgentDock 路径，其他公网路径发生变化，无法安全恢复原私有 Serve 映射"))
				continue
			}
		}
		_, err = mutateTailscalePath(ctx, client, change.node, current, mutation.path, mutation.before, mutation.beforePublic)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("恢复 Funnel 路径 %s 失败: %w", mutation.path, err))
		}
	}
	if failures == nil {
		return change.restorePrivateVisibility(ctx, client)
	}
	return failures
}

// Adding a public root also publishes an existing private /mcp mapping. Removing
// the root again does not undo that port-wide flag; restore it explicitly only
// when the remaining mappings still belong to the original transaction.
func (change *tailscaleMappingChange) restorePrivateVisibility(ctx context.Context, client tailscaleClient) error {
	hostPort := change.node.DNSName + ":" + tailscaleFunnelPort
	if change.attempted == 0 || change.before.AllowFunnel[hostPort] {
		return nil
	}
	node, current, err := client.observe(ctx)
	if err != nil {
		return err
	}
	if err := change.checkIdentity(node); err != nil {
		return err
	}
	if !current.AllowFunnel[hostPort] {
		return nil
	}
	foreignChanged := !sameTailscaleServe(
		unownedTailscaleServe(change.before, hostPort, change.paths()),
		unownedTailscaleServe(current, hostPort, change.paths()))
	for _, path := range change.paths() {
		original := change.before.handler(hostPort, path)
		if original == nil || !sameTailscaleHandler(original, current.handler(hostPort, path)) {
			continue
		}
		if foreignChanged {
			_, removeErr := mutateTailscalePath(ctx, client, change.node, current, path, nil, true)
			return errors.Join(removeErr, tailscaleProblem("rollback_conflict", "已关闭本次公开的 AgentDock 路径，其他映射已变化，原私有 Serve 需要重新配置"))
		}
		_, err := mutateTailscalePath(ctx, client, change.node, current, path, original, false)
		return err
	}
	return nil
}

func (change *tailscaleMappingChange) paths() []string {
	paths := make([]string, 0, len(change.mutations))
	for _, mutation := range change.mutations {
		paths = append(paths, mutation.path)
	}
	return paths
}

func expectedTailscalePath(config *tailscaleServeConfig, hostPort, path string, handler *tailscaleHTTPHandler, public bool) *tailscaleServeConfig {
	expected := cloneTailscaleServe(config)
	if handler == nil {
		if web := expected.Web[hostPort]; web != nil {
			delete(web.Handlers, path)
			if len(web.Handlers) == 0 {
				delete(expected.Web, hostPort)
				delete(expected.TCP, tailscaleFunnelPort)
				delete(expected.AllowFunnel, hostPort)
			}
		}
		return expected
	}
	if expected.TCP == nil {
		expected.TCP = map[string]*tailscaleTCPHandler{}
	}
	if expected.Web == nil {
		expected.Web = map[string]*tailscaleWebConfig{}
	}
	if expected.AllowFunnel == nil {
		expected.AllowFunnel = map[string]bool{}
	}
	if expected.Web[hostPort] == nil {
		expected.Web[hostPort] = &tailscaleWebConfig{Handlers: map[string]*tailscaleHTTPHandler{}}
	}
	expected.TCP[tailscaleFunnelPort] = &tailscaleTCPHandler{HTTPS: true}
	expected.Web[hostPort].Handlers[path] = cloneTailscaleHandler(handler)
	if public {
		expected.AllowFunnel[hostPort] = true
	} else {
		delete(expected.AllowFunnel, hostPort)
	}
	return expected
}

func mutateTailscalePath(ctx context.Context, client tailscaleClient, identity tailscaleNode, before *tailscaleServeConfig, path string, handler *tailscaleHTTPHandler, public bool) (*tailscaleServeConfig, error) {
	if path != "/" && path != "/mcp" {
		return nil, errors.New("refusing to mutate an unowned Tailscale path")
	}
	if handler != nil && (!handler.isProxy(handler.Proxy) || !(validTailscaleLocalOrigin(handler.Proxy) || path == "/mcp" && validTailscaleLocalOrigin(strings.TrimSuffix(handler.Proxy, "/mcp")))) {
		return nil, errors.New("refusing to configure an unowned Tailscale target")
	}
	node, current, err := client.observe(ctx)
	if err != nil {
		return nil, err
	}
	if node.ID != identity.ID || node.DNSName != identity.DNSName || node.checkIdentity() != nil {
		return nil, tailscaleProblem("device_changed", "写入前的 Tailscale 设备身份检查失败，未修改配置")
	}
	if !sameTailscaleServe(before, current) {
		return nil, tailscaleProblem("configuration_changed", "写入前 Tailscale 配置发生变化，未重放命令")
	}
	hostPort := identity.DNSName + ":" + tailscaleFunnelPort
	if err := current.checkHTTPSPort(hostPort); err != nil {
		return nil, err
	}
	command := "funnel"
	if !public {
		command = "serve"
	}
	arguments := []string{command, "--bg", "--yes", "--https=" + tailscaleFunnelPort, "--set-path=" + path}
	if handler == nil {
		arguments = append(arguments, "off")
	} else {
		arguments = append(arguments, handler.Proxy)
	}
	expected := expectedTailscalePath(current, hostPort, path, handler, public)
	_, commandErr := client.run(ctx, tailscaleMutationTimeout, arguments...)
	// An interrupted process may have already changed tailscaled. Always observe
	// it with a separate bounded context; never repeat the write on uncertainty.
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*tailscaleQueryTimeout)
	defer cancel()
	afterNode, after, readErr := client.observe(readCtx)
	if readErr != nil {
		return nil, errors.Join(commandErr, tailscaleProblem("write_state_unknown", "Funnel 写入后的状态无法确认，未重复执行命令"), readErr)
	}
	if afterNode.ID != identity.ID || afterNode.DNSName != identity.DNSName {
		return after, errors.Join(commandErr, tailscaleProblem("device_changed", "Funnel 写入后设备身份变化，需要保留未判定状态"))
	}
	if !sameTailscaleServe(expected, after) {
		return after, errors.Join(commandErr, tailscaleProblem("verification_failed", "Funnel 写入后状态与预期不符，停止后续写入"))
	}
	if commandErr != nil {
		return after, commandErr
	}
	return after, nil
}
