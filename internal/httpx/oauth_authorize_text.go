package httpx

import (
	"fmt"
	"net/http"
	"strings"
)

// Reuse the status page's existing browser-language negotiation. No UI text
// controls authentication, redirects, password comparison, or request parsing.
type authorizePageText struct {
	Lang, Title, Heading, Intro, RequestLabel, ReturnPrefix               string
	PasswordLabel, Placeholder, Submit, Cancel, WarningTitle, WarningText string
	InvalidPassword, RegisteredApp, UnnamedApp                            string
}

var authorizePageEnglish = authorizePageText{
	Lang: "en", Title: "Connect AgentDock", Heading: "Connect to AgentDock",
	Intro:        "Confirm the application below, then enter the server password to connect.",
	RequestLabel: "Application requesting access", ReturnPrefix: "After verification, return to",
	PasswordLabel: "AgentDock server password", Placeholder: "Enter the server password",
	Submit: "Verify and connect", Cancel: "Deny and return",
	WarningTitle:    "Confirm that you initiated this connection.",
	WarningText:     "The password is submitted only to this AgentDock service.",
	InvalidPassword: "The password is incorrect. Try again.", RegisteredApp: "the registered application", UnnamedApp: "Unnamed application",
}
var authorizePageChinese = authorizePageText{
	Lang: "zh-CN", Title: "连接 AgentDock", Heading: "连接到 AgentDock",
	Intro:        "确认下方应用后，输入服务端密码完成连接。",
	RequestLabel: "请求连接的应用", ReturnPrefix: "验证后返回",
	PasswordLabel: "AgentDock 服务端密码", Placeholder: "请输入服务端密码",
	Submit: "验证并连接", Cancel: "拒绝并返回",
	WarningTitle: "请确认这是你刚刚发起的连接。", WarningText: "密码只会提交到当前 AgentDock 服务。",
	InvalidPassword: "密码不正确，请重试。", RegisteredApp: "已注册的应用", UnnamedApp: "未命名应用",
}
var authorizePageKorean = authorizePageText{
	Lang: "ko-KR", Title: "AgentDock 연결", Heading: "AgentDock에 연결",
	Intro:        "아래 앱을 확인한 뒤 서버 비밀번호를 입력하여 연결하세요.",
	RequestLabel: "연결을 요청한 앱", ReturnPrefix: "인증 후 돌아갈 주소:",
	PasswordLabel: "AgentDock 서버 비밀번호", Placeholder: "서버 비밀번호를 입력하세요",
	Submit: "확인 후 연결", Cancel: "거절 후 돌아가기",
	WarningTitle: "직접 시작한 연결 요청인지 확인하세요.", WarningText: "비밀번호는 현재 AgentDock 서비스에만 전송됩니다.",
	InvalidPassword: "비밀번호가 올바르지 않습니다. 다시 입력하세요.", RegisteredApp: "등록된 앱", UnnamedApp: "이름 없는 앱",
}

func preferredAuthorizePageText(header string) authorizePageText {
	// Preserve the original Chinese no-header form for existing callers.
	if strings.TrimSpace(header) == "" {
		return authorizePageChinese
	}
	switch preferredStatusPageText(header).Lang {
	case "ko-KR":
		return authorizePageKorean
	case "zh-CN":
		return authorizePageChinese
	default:
		return authorizePageEnglish
	}
}

var authorizeBrowserErrors = map[string][3]string{
	"too_many_attempts":  {"too many authorization attempts", "授权尝试过多，请稍后重试。", "인증 시도가 너무 많습니다. 잠시 후 다시 시도하세요."},
	"method_not_allowed": {"method not allowed", "不支持此请求方法。", "지원하지 않는 요청 방식입니다."},
	"content_type":       {"content-type must be application/x-www-form-urlencoded", "Content-Type 必须为 application/x-www-form-urlencoded。", "Content-Type은 application/x-www-form-urlencoded여야 합니다."},
	"password_in_body":   {"password must be supplied in the request body", "密码必须放在请求正文中，不得放入 URL。", "비밀번호는 URL이 아닌 요청 본문으로 전송해야 합니다."},
	"parameters_in_body": {"OAuth parameters must be supplied in the request body", "OAuth 参数必须放在请求正文中。", "OAuth 매개변수는 요청 본문으로 전송해야 합니다."},
	"bad_form":           {"bad form", "表单无效或超过大小限制。", "양식이 올바르지 않거나 크기 제한을 초과했습니다."},
	"password_repeated":  {"password must not be repeated", "不得重复提交 password 参数。", "password 매개변수를 중복 전송할 수 없습니다."},
	"parameter_repeated": {"OAuth parameter must not be repeated: %s", "不得重复提交 OAuth 参数：%s", "OAuth 매개변수를 중복 전송할 수 없습니다: %s"},
	"invalid_client":     {"invalid client_id or redirect_uri", "client_id 或 redirect_uri 无效。", "client_id 또는 redirect_uri가 올바르지 않습니다."},
}

func writeAuthorizeBrowserError(w http.ResponseWriter, r *http.Request, code string, status int, args ...any) {
	index := 0
	switch preferredStatusPageText(r.Header.Get("Accept-Language")).Lang {
	case "zh-CN":
		index = 1
	case "ko-KR":
		index = 2
	}
	message := authorizeBrowserErrors[code][index]
	if len(args) != 0 {
		message = fmt.Sprintf(message, args...)
	}
	w.Header().Set("Vary", "Accept-Language")
	http.Error(w, message, status)
}
