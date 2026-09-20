package desktopruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const tailscaleJSONLimit = 1 << 20
const tailscaleFunnelPort = "443"
const tailscaleAuthorizationURL = "https://login.tailscale.com/admin/acls"

type tailscaleDiagnostic struct{ Code, Message string }

func (diagnostic *tailscaleDiagnostic) Error() string { return diagnostic.Message }
func tailscaleProblem(code, message string) error     { return &tailscaleDiagnostic{code, message} }

type tailscaleNode struct {
	BackendState string
	ID           string
	HostName     string
	DNSName      string
	Online       bool
	KeyExpiry    *time.Time
	capabilities map[string]bool
}

// Only self-node data is retained. Peer identities, login URLs and credentials
// are neither returned by the desktop API nor persisted in ownership records.
func decodeTailscaleNode(data []byte) (tailscaleNode, error) {
	var wire struct {
		BackendState string
		Self         *struct {
			ID           string
			HostName     string
			DNSName      string
			Online       bool
			KeyExpiry    *time.Time
			Capabilities []string
			CapMap       map[string]json.RawMessage
		}
		CurrentTailnet *struct{ MagicDNSSuffix string }
	}
	if len(data) > tailscaleJSONLimit || json.Unmarshal(data, &wire) != nil || wire.BackendState == "" {
		return tailscaleNode{}, tailscaleProblem("invalid_status_json", "Tailscale 状态 JSON 不兼容，未修改配置")
	}
	node := tailscaleNode{BackendState: wire.BackendState, capabilities: map[string]bool{}}
	if wire.Self == nil {
		if wire.BackendState == "Running" {
			return node, tailscaleProblem("invalid_status_json", "Tailscale Running 状态缺少 Self 节点信息")
		}
		return node, nil
	}
	node.ID, node.HostName, node.Online, node.KeyExpiry = wire.Self.ID, wire.Self.HostName, wire.Self.Online, wire.Self.KeyExpiry
	if wire.Self.DNSName != "" {
		dns, err := normalizeTailscaleDNSName(wire.Self.DNSName)
		if err != nil {
			return node, tailscaleProblem("invalid_dns", "Tailscale 返回的设备 DNS 名无效，未修改配置")
		}
		node.DNSName = dns
		if wire.CurrentTailnet != nil && wire.CurrentTailnet.MagicDNSSuffix != "" {
			suffix := strings.ToLower(strings.TrimSuffix(wire.CurrentTailnet.MagicDNSSuffix, "."))
			if !strings.HasSuffix(dns, "."+suffix) {
				return node, tailscaleProblem("invalid_dns", "设备 DNS 名不属于当前 tailnet，未修改配置")
			}
		}
	}
	for _, capability := range wire.Self.Capabilities {
		node.capabilities[capability] = true
	}
	// Capability values can be null. Authorization is encoded by key presence.
	for capability := range wire.Self.CapMap {
		node.capabilities[capability] = true
	}
	return node, nil
}

func (node tailscaleNode) checkIdentity() error {
	if node.BackendState != "Running" {
		if node.BackendState == "NeedsLogin" || node.BackendState == "NeedsMachineAuth" {
			return tailscaleProblem("needs_login", "请在 Tailscale 官方客户端完成登录或设备授权")
		}
		return tailscaleProblem("not_running", "Tailscale 未处于 Running 状态，请检查官方客户端")
	}
	if !node.Online {
		return tailscaleProblem("offline", "当前 Tailscale 设备离线，未修改配置")
	}
	if node.ID == "" || node.DNSName == "" {
		return tailscaleProblem("missing_identity", "Tailscale 缺少设备 ID 或 DNS 名，未修改配置")
	}
	return nil
}

func (node tailscaleNode) checkFunnelReady(now time.Time) error {
	if err := node.checkIdentity(); err != nil {
		return err
	}
	if node.KeyExpiry != nil && !node.KeyExpiry.IsZero() && !node.KeyExpiry.After(now) {
		return tailscaleProblem("key_expired", "Tailscale 设备密钥已到期，请在官方客户端重新认证")
	}
	if !node.capabilities["funnel"] || !node.capabilities["https"] || !node.funnelPortAllowed(443) {
		return tailscaleProblem("funnel_permission_required", "当前设备未获得 HTTPS 443 Funnel 权限，请在 Tailscale 管理页授权后重新检测")
	}
	return nil
}

func (node tailscaleNode) funnelPortAllowed(port int) bool {
	for capability := range node.capabilities {
		parsed, err := url.Parse(capability)
		if err != nil || parsed.Scheme != "https" || parsed.Host != "tailscale.com" || parsed.Path != "/cap/funnel-ports" {
			continue
		}
		for _, value := range strings.Split(parsed.Query().Get("ports"), ",") {
			low, high, hasRange := strings.Cut(value, "-")
			first, err := strconv.Atoi(low)
			if err != nil {
				continue
			}
			if !hasRange && first == port {
				return true
			}
			last, err := strconv.Atoi(high)
			if hasRange && err == nil && first <= port && port <= last {
				return true
			}
		}
	}
	return false
}

// The mutation boundary accepts only the known Serve schema. Services are opaque
// and preserved; unknown top-level/handler fields fail closed before any write.
type tailscaleServeConfig struct {
	TCP         map[string]*tailscaleTCPHandler  `json:",omitempty"`
	Web         map[string]*tailscaleWebConfig   `json:",omitempty"`
	AllowFunnel map[string]bool                  `json:",omitempty"`
	Foreground  map[string]*tailscaleServeConfig `json:",omitempty"`
	Services    map[string]json.RawMessage       `json:",omitempty"`
}
type tailscaleTCPHandler struct {
	HTTPS         bool   `json:",omitempty"`
	HTTP          bool   `json:",omitempty"`
	TCPForward    string `json:",omitempty"`
	TerminateTLS  string `json:",omitempty"`
	ProxyProtocol int    `json:",omitempty"`
}
type tailscaleWebConfig struct {
	Handlers map[string]*tailscaleHTTPHandler
}
type tailscaleHTTPHandler struct {
	Path          string   `json:",omitempty"`
	Proxy         string   `json:",omitempty"`
	Text          string   `json:",omitempty"`
	Redirect      string   `json:",omitempty"`
	AcceptAppCaps []string `json:",omitempty"`
}

func decodeTailscaleServe(data []byte) (*tailscaleServeConfig, error) {
	if len(data) > tailscaleJSONLimit {
		return nil, tailscaleProblem("output_limit", "Tailscale 状态超过输出上限，未修改配置")
	}
	var config *tailscaleServeConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, tailscaleProblem("invalid_funnel_json", "Tailscale Serve/Funnel JSON 不兼容，未修改配置")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, tailscaleProblem("invalid_funnel_json", "Tailscale Serve/Funnel 返回了多余 JSON 数据")
	}
	if config == nil {
		config = &tailscaleServeConfig{}
	}
	if err := config.validateShape(0); err != nil {
		return nil, err
	}
	return config, nil
}

func (config *tailscaleServeConfig) validateShape(depth int) error {
	invalid := func() error {
		return tailscaleProblem("invalid_funnel_json", "Tailscale Serve/Funnel 配置结构无效，未修改配置")
	}
	if config == nil || depth > 1 {
		return invalid()
	}
	for port, handler := range config.TCP {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 || handler == nil {
			return invalid()
		}
	}
	for hostPort, web := range config.Web {
		if _, _, err := net.SplitHostPort(hostPort); err != nil || web == nil || web.Handlers == nil {
			return invalid()
		}
		for path, handler := range web.Handlers {
			if !strings.HasPrefix(path, "/") || handler == nil {
				return invalid()
			}
		}
	}
	for _, child := range config.Foreground {
		if err := child.validateShape(depth + 1); err != nil {
			return err
		}
	}
	return nil
}

func (config *tailscaleServeConfig) handler(hostPort, path string) *tailscaleHTTPHandler {
	if config.Web[hostPort] == nil {
		return nil
	}
	return config.Web[hostPort].Handlers[path]
}

func (handler *tailscaleHTTPHandler) isProxy(target string) bool {
	return handler != nil && handler.Proxy == target && handler.Path == "" && handler.Text == "" && handler.Redirect == "" && len(handler.AcceptAppCaps) == 0
}

func (config *tailscaleServeConfig) checkHTTPSPort(hostPort string) error {
	conflict := func(message string) error { return tailscaleProblem("mapping_conflict", message) }
	if tcp := config.TCP[tailscaleFunnelPort]; tcp != nil && (!tcp.HTTPS || tcp.HTTP || tcp.TCPForward != "" || tcp.TerminateTLS != "" || tcp.ProxyProtocol != 0) {
		return conflict("HTTPS 443 已被其他 TCP 服务占用，拒绝覆盖")
	}
	for other := range config.Web {
		_, port, err := net.SplitHostPort(other)
		if err != nil {
			return err
		}
		if port == tailscaleFunnelPort && other != hostPort {
			return conflict("HTTPS 443 存在其他设备域名映射，拒绝覆盖")
		}
	}
	for _, child := range config.Foreground {
		if child.TCP[tailscaleFunnelPort] != nil {
			return conflict("HTTPS 443 正被前台 Serve/Funnel 使用，拒绝接管")
		}
		for other := range child.Web {
			_, port, _ := net.SplitHostPort(other)
			if port == tailscaleFunnelPort {
				return conflict("HTTPS 443 正被前台 Serve/Funnel 使用，拒绝接管")
			}
		}
	}
	handlers := 0
	if web := config.Web[hostPort]; web != nil {
		handlers = len(web.Handlers)
	}
	if config.TCP[tailscaleFunnelPort] != nil && handlers == 0 {
		return conflict("HTTPS 443 已配置但缺少可识别的 HTTP 映射，拒绝覆盖")
	}
	if handlers > 0 && config.TCP[tailscaleFunnelPort] == nil {
		return tailscaleProblem("invalid_funnel_json", "HTTP 映射缺少对应 HTTPS 端口，未修改配置")
	}
	return nil
}

func reservedAgentDockPath(path string) bool {
	path = strings.TrimSuffix(path, "/")
	for _, reserved := range []string{"/mcp", "/context", "/.well-known", "/register", "/oauth", "/artifacts", "/internal", "/healthz"} {
		if path == reserved || strings.HasPrefix(path, reserved+"/") || strings.HasPrefix(reserved, path+"/") {
			return true
		}
	}
	return false
}

func cloneTailscaleServe(config *tailscaleServeConfig) *tailscaleServeConfig {
	data, _ := json.Marshal(config)
	var clone tailscaleServeConfig
	_ = json.Unmarshal(data, &clone)
	return &clone
}

func sameTailscaleServe(left, right *tailscaleServeConfig) bool {
	// Normalize omitted empty maps and RawMessage whitespace/key order.
	var l, r any
	ld, _ := json.Marshal(left)
	rd, _ := json.Marshal(right)
	_ = json.Unmarshal(ld, &l)
	_ = json.Unmarshal(rd, &r)
	return reflect.DeepEqual(l, r)
}

func unownedTailscaleServe(config *tailscaleServeConfig, hostPort string, paths []string) *tailscaleServeConfig {
	copy := cloneTailscaleServe(config)
	if web := copy.Web[hostPort]; web != nil {
		for _, path := range paths {
			delete(web.Handlers, path)
		}
		if len(web.Handlers) == 0 {
			delete(copy.Web, hostPort)
			delete(copy.TCP, tailscaleFunnelPort)
			delete(copy.AllowFunnel, hostPort)
		}
	}
	return copy
}

func tailscaleDiagnosticCode(err error) string {
	var diagnostic *tailscaleDiagnostic
	if errors.As(err, &diagnostic) {
		return diagnostic.Code
	}
	if err != nil {
		return "tailscale_error"
	}
	return ""
}

func localTailscaleOrigin(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }
