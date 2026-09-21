package app

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type observedClient struct {
	Active                   int
	AuthorizedAt, RejectedAt time.Time
}
type clientConnections struct {
	mu      sync.Mutex
	clients map[string]*observedClient
}

// ObserveClientRequest is called after HTTP authentication. The key is a
// one-way credential digest, never an access token or user-supplied title.
// Anonymous discovery and unrecognized invalid tokens cannot downgrade a
// previously authenticated client. No credentials are persisted by this tracker.
func (r *Runtime) ObserveClientRequest(key string, authorized bool) func() {
	if key == "" || len(key) > 128 {
		return func() {}
	}
	state := &r.connections
	state.mu.Lock()
	if state.clients == nil {
		state.clients = map[string]*observedClient{}
	}
	current := state.clients[key]
	if !authorized {
		if current != nil {
			current.RejectedAt = time.Now().UTC()
		}
		state.mu.Unlock()
		return func() {}
	}
	if current == nil {
		if len(state.clients) >= 128 {
			oldestKey := ""
			var oldest time.Time
			for candidate, value := range state.clients {
				if value.Active == 0 && (oldestKey == "" || value.AuthorizedAt.Before(oldest)) {
					oldestKey, oldest = candidate, value.AuthorizedAt
				}
			}
			if oldestKey == "" {
				state.mu.Unlock()
				return func() {}
			}
			delete(state.clients, oldestKey)
		}
		current = &observedClient{}
		state.clients[key] = current
	}
	current.Active++
	current.AuthorizedAt = time.Now().UTC()
	state.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { state.mu.Lock(); current.Active--; state.mu.Unlock() }) }
}

func (r *Runtime) RuntimeClientConnection(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.connections.mu.Lock()
	defer r.connections.mu.Unlock()
	active, valid, invalid := 0, 0, 0
	var latest time.Time
	for _, client := range r.connections.clients {
		active += client.Active
		if client.AuthorizedAt.After(latest) {
			latest = client.AuthorizedAt
		}
		if client.RejectedAt.After(client.AuthorizedAt) {
			invalid++
		} else {
			valid++
		}
	}
	state, summary, detail := "unobserved", "尚无本次启动后的客户端认证记录", "匿名公网检查不代表客户端需要重新授权。"
	if active > 0 {
		state, summary = "client_connected", "客户端已连接"
		detail = fmt.Sprintf("当前有 %d 个已认证的 MCP 请求或传输连接。", active)
	} else if valid > 0 && time.Since(latest) <= 2*time.Minute {
		state, summary = "recently_connected", "客户端最近已连接"
		detail = "当前没有持续请求，最近一次客户端请求已通过认证。"
	} else if valid > 0 {
		state, summary = "authorized", "客户端已授权"
		detail = "该状态来自最近一次成功的客户端认证。当前没有持续请求。"
	} else if invalid > 0 {
		state, summary = "reauthorization_required", "客户端需要重新授权"
		detail = "此前成功使用的凭据已在后续请求中被认证服务拒绝。"
	}
	result := Result{"state": state, "summary": summary, "detail": detail, "active_requests": active, "observed_clients": len(r.connections.clients), "public_reachability": "not_checked", "evidence": "authenticated_mcp_http_requests"}
	if !latest.IsZero() {
		result["last_authorized_at"] = latest
		result["detail"] = detail + " 最近验证：" + latest.Local().Format("2006-01-02 15:04:05")
	}
	return result, nil
}
