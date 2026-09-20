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

	"github.com/uvwt/agentdock/internal/auth"
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
			return errors.Join(tailscaleProblem("public_unreachable", "Tailscale 公网地址未能在验证期限内就绪，配置将回滚"), err, verification.Err())
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
	metadataURL, challengeErr := auth.BearerResourceMetadata(unauthorized.header.Values("WWW-Authenticate"))
	if challengeErr != nil || metadataURL != origin+"/.well-known/oauth-protected-resource/mcp" {
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
		return tailscaleHTTPResponse{}, &tailscaleTransientProbe{message: fmt.Sprintf("公网网络 %s%s: %v (%s)", origin, path, err, time.Now().UTC().Format(time.RFC3339))}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusBadGateway || response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusGatewayTimeout {
		return tailscaleHTTPResponse{}, &tailscaleTransientProbe{message: fmt.Sprintf("Funnel 尚未连接本机服务: %s (HTTP %d)", path, response.StatusCode)}
	}
	if response.StatusCode != wantStatus {
		return tailscaleHTTPResponse{}, tailscaleProblem("public_protocol_failed", fmt.Sprintf("公网路径 %s 返回 %d，预期 %d", path, response.StatusCode, wantStatus))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, tailscaleJSONLimit+1))
	if err != nil {
		return tailscaleHTTPResponse{}, &tailscaleTransientProbe{message: fmt.Sprintf("公网响应读取失败: %s: %v", path, err)}
	}
	if len(body) > tailscaleJSONLimit {
		return tailscaleHTTPResponse{}, tailscaleProblem("response_limit", "公网协议响应超过上限: "+path)
	}
	return tailscaleHTTPResponse{body: body, header: response.Header.Clone()}, nil
}

func verifyTailscaleInitialize(ctx context.Context, client *http.Client, origin, token string) error {
	response, err := tailscaleMCPPost(ctx, client, origin, token, "", "", `{"jsonrpc":"2.0","id":771,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"agentdock-funnel-check","version":"1"}}}`)
	if err != nil {
		return err
	}
	result, err := tailscaleRPCResult(response, 771, "initialize")
	sessionID := response.Header.Get("Mcp-Session-Id")
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err == nil && (json.Unmarshal(result, &initialized) != nil || initialized.ServerInfo.Name == "") {
		err = tailscaleProblem("initialize_failed", "MCP initialize 缺少服务端信息")
	}
	// Close only the session created by this probe, including failed tools/list checks.
	if sessionID != "" {
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request, requestErr := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, origin+"/mcp", nil)
			if requestErr != nil {
				return
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Mcp-Session-Id", sessionID)
			if initialized.ProtocolVersion != "" {
				request.Header.Set("MCP-Protocol-Version", initialized.ProtocolVersion)
			}
			if response, err := client.Do(request); err == nil {
				response.Body.Close()
			}
		}()
	}
	if err != nil {
		return err
	}
	switch initialized.ProtocolVersion {
	case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
	default:
		return tailscaleProblem("initialize_failed", "MCP initialize 返回不支持的协议版本")
	}
	notification, err := tailscaleMCPPost(ctx, client, origin, token, sessionID, initialized.ProtocolVersion,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if err != nil {
		return err
	}
	notification.Body.Close()
	if notification.StatusCode != http.StatusAccepted && notification.StatusCode != http.StatusNoContent {
		return tailscaleProblem("initialized_failed", fmt.Sprintf("MCP notifications/initialized 返回 HTTP %d", notification.StatusCode))
	}
	discovery, err := tailscaleMCPPost(ctx, client, origin, token, sessionID, initialized.ProtocolVersion,
		`{"jsonrpc":"2.0","id":772,"method":"tools/list","params":{}}`)
	if err != nil {
		return err
	}
	tools, err := tailscaleRPCResult(discovery, 772, "tools/list")
	if err != nil {
		return err
	}
	var listing struct {
		Tools []struct {
			Name        string                     `json:"name"`
			InputSchema map[string]json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if json.Unmarshal(tools, &listing) != nil || len(listing.Tools) == 0 {
		return tailscaleProblem("tools_list_failed", "MCP tools/list 未返回工具定义")
	}
	for _, tool := range listing.Tools {
		if tool.Name == "" || tool.InputSchema == nil {
			return tailscaleProblem("tools_list_failed", "MCP tools/list 包含无效工具定义")
		}
	}
	return nil
}

func tailscaleMCPPost(ctx context.Context, client *http.Client, origin, token, session, protocol, body string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/mcp", bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		request.Header.Set("Mcp-Session-Id", session)
	}
	if protocol != "" {
		request.Header.Set("MCP-Protocol-Version", protocol)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, &tailscaleTransientProbe{message: fmt.Sprintf("MCP 会话 %s/mcp: %v", origin, err)}
	}
	return response, nil
}

func tailscaleRPCResult(response *http.Response, id int, stage string) (json.RawMessage, error) {
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, tailscaleProblem("mcp_protocol_failed", fmt.Sprintf("MCP %s 返回 HTTP %d，预期 200", stage, response.StatusCode))
	}
	var payload []byte
	reader := io.LimitReader(response.Body, tailscaleJSONLimit+1)
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), tailscaleJSONLimit)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" && len(payload) > 0 {
				break
			}
			if strings.HasPrefix(line, "data:") {
				payload = append(payload, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
				payload = append(payload, '\n')
				if len(payload) > tailscaleJSONLimit {
					return nil, tailscaleProblem("response_limit", "MCP 响应超过上限: "+stage)
				}
			}
		}
		if scanner.Err() != nil {
			return nil, tailscaleProblem("mcp_protocol_failed", "MCP SSE 响应读取失败: "+stage)
		}
	} else {
		var err error
		payload, err = io.ReadAll(reader)
		if err != nil {
			return nil, tailscaleProblem("mcp_protocol_failed", "MCP 响应读取失败: "+stage)
		}
	}
	if len(payload) > tailscaleJSONLimit {
		return nil, tailscaleProblem("response_limit", "MCP 响应超过上限: "+stage)
	}
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Error   json.RawMessage `json:"error"`
		Result  json.RawMessage `json:"result"`
	}
	if json.Unmarshal(payload, &reply) != nil || reply.JSONRPC != "2.0" || reply.ID != id ||
		(len(reply.Error) != 0 && string(reply.Error) != "null") || len(reply.Result) == 0 || string(reply.Result) == "null" {
		return nil, tailscaleProblem("mcp_protocol_failed", "MCP "+stage+" 未返回有效结果")
	}
	return reply.Result, nil
}
