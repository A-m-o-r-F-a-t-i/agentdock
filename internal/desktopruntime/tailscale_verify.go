package desktopruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type tailscaleTransientProbe struct{ message string }

func (err *tailscaleTransientProbe) Error() string { return err.message }

func newTailscaleHTTPClient() *http.Client {
	return &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

// This verifier only receives an origin obtained from the authenticated local
// Tailscale client. It never sends the AgentDock credential through redirects.
func waitTailscalePublicOrigin(ctx context.Context, origin, token string, client *http.Client) error {
	canonical, err := normalizeTailscaleOrigin(origin)
	if err != nil || canonical != origin {
		return errors.New("public verification requires the canonical Tailscale device origin")
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("AgentDock Bearer Token is missing; public access was not verified")
	}
	verification, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		err = verifyTailscalePublicOrigin(verification, origin, token, client)
		if err == nil {
			return nil
		}
		var transient *tailscaleTransientProbe
		if !errors.As(err, &transient) {
			return err
		}
		select {
		case <-verification.Done():
			return errors.Join(tailscaleProblem("public_unreachable", "Tailscale 公网地址未能在验证期限内就绪，配置将回滚"), verification.Err())
		case <-time.After(time.Second):
		}
	}
}

func verifyTailscalePublicOrigin(ctx context.Context, origin, token string, client *http.Client) error {
	if _, err := tailscaleGet(ctx, client, origin, "/healthz", http.StatusOK); err != nil {
		return err
	}
	unauthorized, err := tailscaleGet(ctx, client, origin, "/mcp", http.StatusUnauthorized)
	if err != nil {
		return err
	}
	challenge := unauthorized.header.Get("WWW-Authenticate")
	wantChallenge := `Bearer resource_metadata="` + origin + `/.well-known/oauth-protected-resource/mcp"`
	if challenge != wantChallenge {
		return tailscaleProblem("oauth_origin_mismatch", "MCP 401 challenge 未使用当前 Tailscale Origin")
	}
	if _, err := tailscaleGet(ctx, client, origin, "/internal/runtime/status", http.StatusUnauthorized); err != nil {
		return err
	}
	authorization, err := tailscaleGet(ctx, client, origin, "/.well-known/oauth-authorization-server", http.StatusOK)
	if err != nil {
		return err
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(authorization.body, &metadata) != nil || metadata == nil {
		return tailscaleProblem("invalid_oauth_metadata", "OAuth Metadata 不是有效 JSON 对象")
	}
	for field, suffix := range map[string]string{"issuer": "", "authorization_endpoint": "/oauth/authorize", "token_endpoint": "/oauth/token", "registration_endpoint": "/register"} {
		var value string
		if json.Unmarshal(metadata[field], &value) != nil || value != origin+suffix {
			return tailscaleProblem("oauth_origin_mismatch", "OAuth Metadata 的 "+field+" 未使用当前 Tailscale Origin")
		}
	}
	resource, err := tailscaleGet(ctx, client, origin, "/.well-known/oauth-protected-resource/mcp", http.StatusOK)
	if err != nil {
		return err
	}
	var protected struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if json.Unmarshal(resource.body, &protected) != nil || protected.Resource != origin+"/mcp" || len(protected.AuthorizationServers) != 1 || protected.AuthorizationServers[0] != origin {
		return tailscaleProblem("oauth_origin_mismatch", "Protected Resource Metadata 未使用当前 Tailscale Origin")
	}
	for _, path := range []string{"/.well-known/mcp.json", "/.well-known/mcp/server-card.json"} {
		response, err := tailscaleGet(ctx, client, origin, path, http.StatusOK)
		if err != nil {
			return err
		}
		var document map[string]json.RawMessage
		if json.Unmarshal(response.body, &document) != nil || document == nil {
			return tailscaleProblem("invalid_server_metadata", "MCP 描述路径未返回有效 JSON: "+path)
		}
	}
	for path, status := range map[string]int{"/register": http.StatusMethodNotAllowed, "/oauth/authorize": http.StatusBadRequest, "/oauth/token": http.StatusMethodNotAllowed} {
		if _, err := tailscaleGet(ctx, client, origin, path, status); err != nil {
			return err
		}
	}
	return verifyTailscaleInitialize(ctx, client, origin, token)
}

type tailscaleHTTPResponse struct {
	body   []byte
	header http.Header
}

func tailscaleGet(ctx context.Context, client *http.Client, origin, path string, wantStatus int) (tailscaleHTTPResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+path, nil)
	if err != nil {
		return tailscaleHTTPResponse{}, err
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return tailscaleHTTPResponse{}, &tailscaleTransientProbe{message: "公网请求暂未成功: " + path}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusBadGateway || response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusGatewayTimeout {
		return tailscaleHTTPResponse{}, &tailscaleTransientProbe{message: "Funnel 尚未连接本机服务: " + path}
	}
	if response.StatusCode != wantStatus {
		return tailscaleHTTPResponse{}, tailscaleProblem("public_protocol_failed", fmt.Sprintf("公网路径 %s 返回 %d，预期 %d", path, response.StatusCode, wantStatus))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, tailscaleJSONLimit+1))
	if err != nil {
		return tailscaleHTTPResponse{}, &tailscaleTransientProbe{message: "公网响应读取失败: " + path}
	}
	if len(body) > tailscaleJSONLimit {
		return tailscaleHTTPResponse{}, tailscaleProblem("response_limit", "公网协议响应超过上限: "+path)
	}
	return tailscaleHTTPResponse{body: body, header: response.Header.Clone()}, nil
}

func verifyTailscaleInitialize(ctx context.Context, client *http.Client, origin, token string) error {
	body := []byte(`{"jsonrpc":"2.0","id":771,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"agentdock-funnel-check","version":"1"}}}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/mcp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return &tailscaleTransientProbe{message: "认证 MCP initialize 请求暂未成功"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return tailscaleProblem("initialize_failed", fmt.Sprintf("认证 MCP initialize 返回 %d，预期 200", response.StatusCode))
	}
	var payload []byte
	reader := io.LimitReader(response.Body, tailscaleJSONLimit+1)
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), tailscaleJSONLimit)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				payload = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				break
			}
		}
		if scanner.Err() != nil {
			return tailscaleProblem("initialize_failed", "MCP initialize SSE 响应读取失败")
		}
	} else {
		payload, err = io.ReadAll(reader)
		if err != nil {
			return tailscaleProblem("initialize_failed", "MCP initialize 响应读取失败")
		}
	}
	if len(payload) > tailscaleJSONLimit {
		return tailscaleProblem("response_limit", "MCP initialize 响应超过上限")
	}
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Error   json.RawMessage `json:"error"`
		Result  *struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if json.Unmarshal(payload, &reply) != nil || reply.JSONRPC != "2.0" || reply.ID != 771 || (len(reply.Error) != 0 && string(reply.Error) != "null") || reply.Result == nil || reply.Result.ServerInfo.Name == "" || reply.Result.ProtocolVersion == "" {
		return tailscaleProblem("initialize_failed", "认证 MCP initialize 未返回有效初始化结果")
	}
	// Dispose only the session created by this verification request, if the
	// server uses sessions. No user session is enumerated or modified.
	if sessionID := response.Header.Get("Mcp-Session-Id"); sessionID != "" {
		_ = response.Body.Close()
		cleanup, cleanupErr := http.NewRequestWithContext(ctx, http.MethodDelete, origin+"/mcp", nil)
		if cleanupErr == nil {
			cleanup.Header.Set("Authorization", "Bearer "+token)
			cleanup.Header.Set("Mcp-Session-Id", sessionID)
			cleanup.Header.Set("MCP-Protocol-Version", reply.Result.ProtocolVersion)
			if result, err := client.Do(cleanup); err == nil {
				_ = result.Body.Close()
			}
		}
	}
	return nil
}
