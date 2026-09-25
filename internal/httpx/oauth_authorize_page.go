package httpx

import (
	_ "embed"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/uvwt/agentdock/internal/auth"
)

// 授权页必须保持完全自包含，避免 OAuth 流程依赖第三方静态资源或前端构建链路。
//
//go:embed oauth_authorize_page.html
var authorizePageHTML string

var authorizePageTemplate = template.Must(template.New("oauth-authorize").Parse(authorizePageHTML))

type authorizePageData struct {
	Text                authorizePageText
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	State               string
	Error               string
	ClientName          string
	ClientInitial       string
	RedirectHost        string
	CancelURL           string
}

// 不设置 form-action。Chromium 会把表单提交后的整条 HTTP 重定向链都纳入该指令校验，
// 而 OAuth 客户端的已注册 callback 仍可能继续跳转到另一个来源。AgentDock 无法也不应枚举
// 客户端 callback 后续的导航目标；真正的 redirect_uri 安全边界由服务端注册信息校验负责。
// 授权页本身没有脚本，表单 action 固定为同源 /oauth/authorize，其余 CSP 继续禁止外部资源、
// base URL 改写和第三方嵌入。
const authorizationPageCSP = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'"

func writeAuthorizeForm(w http.ResponseWriter, values url.Values, errorText, clientName string, language ...string) {
	header := ""
	if len(language) > 0 {
		header = language[0]
	}
	text := preferredAuthorizePageText(header)
	w.Header().Set("Vary", "Accept-Language")
	w.Header().Set("Content-Language", text.Lang)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Content-Security-Policy", authorizationPageCSP)

	message := ""
	if errorText != "" {
		message = text.InvalidPassword
	}
	redirectHost := text.RegisteredApp
	if parsed, err := url.Parse(values.Get("redirect_uri")); err == nil && parsed.Host != "" {
		redirectHost = parsed.Host
	}
	clientName = strings.TrimSpace(clientName)
	if clientName == "" {
		clientName = text.UnnamedApp
	}
	clientInitial := "?"
	if runes := []rune(clientName); len(runes) > 0 {
		clientInitial = string(runes[0])
	}
	cancelValues := url.Values{"error": {"access_denied"}}
	if state := values.Get("state"); state != "" {
		cancelValues.Set("state", state)
	}
	data := authorizePageData{
		Text:                text,
		ResponseType:        values.Get("response_type"),
		ClientID:            values.Get("client_id"),
		RedirectURI:         values.Get("redirect_uri"),
		CodeChallenge:       values.Get("code_challenge"),
		CodeChallengeMethod: values.Get("code_challenge_method"),
		Resource:            values.Get("resource"),
		State:               values.Get("state"),
		Error:               message,
		ClientName:          clientName,
		ClientInitial:       clientInitial,
		RedirectHost:        redirectHost,
		CancelURL:           auth.AppendQuery(values.Get("redirect_uri"), cancelValues),
	}
	if err := authorizePageTemplate.Execute(w, data); err != nil {
		slog.Warn("write OAuth authorization form failed", "error", err)
	}
}
