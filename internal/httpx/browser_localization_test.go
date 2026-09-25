package httpx

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/auth"
)

func TestAuthorizePageLocalesPreserveEscapingAndConsent(t *testing.T) {
	values := url.Values{
		"response_type": {"code"}, "client_id": {`fixture"><script>alert(1)</script>`},
		"redirect_uri":   {"https://client.example:8443/oauth/callback?source=fixture"},
		"code_challenge": {oauthTestChallenge}, "code_challenge_method": {"S256"},
		"resource": {"https://fixture.example/mcp"}, "state": {`state <&> 한국어 中文`},
	}
	fieldPattern := regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)
	for _, tc := range []struct {
		header string
		text   authorizePageText
	}{
		{"ko-KR,ko;q=0.9,en;q=0.5", authorizePageKorean},
		{"en-US", authorizePageEnglish}, {"zh-CN", authorizePageChinese}, {"", authorizePageChinese},
	} {
		t.Run(tc.header, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeAuthorizeForm(response, values, "fixture-password-must-not-be-echoed", `Fixture <script>client</script>`, tc.header)
			body := response.Body.String()
			for _, expected := range []string{`<html lang="` + tc.text.Lang + `">`, tc.text.Heading, tc.text.PasswordLabel, tc.text.Submit, tc.text.Cancel, tc.text.InvalidPassword, tc.text.WarningTitle, tc.text.WarningText, `<form method="post" action="/oauth/authorize" autocomplete="on">`, `type="password"`, `aria-invalid="true"`} {
				if !strings.Contains(body, expected) {
					t.Errorf("locale %s missing %q", tc.text.Lang, expected)
				}
			}
			for _, forbidden := range []string{"fixture-password-must-not-be-echoed", "<script>", "<img src=", `action="/oauth/authorize?`} {
				if strings.Contains(body, forbidden) {
					t.Errorf("unsafe rendered value %q", forbidden)
				}
			}
			fields := map[string]string{}
			for _, match := range fieldPattern.FindAllStringSubmatch(body, -1) {
				fields[match[1]] = html.UnescapeString(match[2])
			}
			for _, name := range authorizationParameterNames {
				if fields[name] != values.Get(name) {
					t.Errorf("localization changed %s: %q", name, fields[name])
				}
			}
			if !strings.Contains(html.UnescapeString(body), "error=access_denied") || !strings.Contains(body, "state=") {
				t.Fatal("deny link lost error or state")
			}
			if response.Header().Get("Content-Security-Policy") != authorizationPageCSP || response.Header().Get("X-Frame-Options") != "DENY" || response.Header().Get("Content-Language") != tc.text.Lang || response.Header().Get("Vary") != "Accept-Language" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("security or language headers changed")
			}
		})
	}
}

func TestKoreanAuthorizeStillRequiresPasswordAndRegisteredRedirect(t *testing.T) {
	const password = "isolated-korean-password"
	t.Setenv("AGENTDOCK_OAUTH_PASSWORD", password)
	t.Setenv("AGENTDOCK_OAUTH_TOKEN_SECRET", "isolated-korean-signing")
	cfg := oauthTestConfig(t)
	store := auth.NewOAuthStore()
	clientID := oauthRegisteredClientID(t, store, oauthTestRedirect)
	values := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {oauthTestRedirect}, "code_challenge": {oauthTestChallenge}, "code_challenge_method": {"S256"}, "resource": {cfg.OAuthServerURL + "/mcp"}, "state": {"unchanged-korean-state"}}
	request := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+values.Encode(), nil)
	request.Header.Set("Accept-Language", "ko-KR")
	response := httptest.NewRecorder()
	handleAuthorize(response, request, cfg, store)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "확인 후 연결") || response.Header().Get("Location") != "" {
		t.Fatal("Korean GET did not present consent")
	}
	wrong := cloneValues(values)
	wrong.Set("password", "invalid-korean-password")
	request = formAuthorizeRequest(wrong)
	request.Header.Set("Accept-Language", "ko")
	response = httptest.NewRecorder()
	handleAuthorize(response, request, cfg, store)
	if response.Code != 200 || !strings.Contains(response.Body.String(), authorizePageKorean.InvalidPassword) || response.Header().Get("Location") != "" || strings.Contains(response.Body.String(), "invalid-korean-password") {
		t.Fatal("Korean invalid password bypassed or leaked password")
	}
	valid := cloneValues(values)
	valid.Set("password", password)
	request = formAuthorizeRequest(valid)
	request.Header.Set("Accept-Language", "ko-KR")
	response = httptest.NewRecorder()
	handleAuthorize(response, request, cfg, store)
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil || response.Code != 302 || location.Query().Get("code") == "" || location.Query().Get("state") != values.Get("state") {
		t.Fatal("valid consent lost code or original state")
	}
	invalid := cloneValues(valid)
	invalid.Set("redirect_uri", "https://unregistered.example/callback")
	request = formAuthorizeRequest(invalid)
	request.Header.Set("Accept-Language", "ko-KR")
	response = httptest.NewRecorder()
	handleAuthorize(response, request, cfg, store)
	if response.Code != 400 || response.Header().Get("Location") != "" || !strings.Contains(response.Body.String(), "client_id 또는 redirect_uri") {
		t.Fatal("localized invalid-client error weakened redirect validation")
	}
	request = formAuthorizeRequest(valid)
	request.URL.RawQuery = "password=must-not-appear"
	request.Header.Set("Accept-Language", "ko-KR")
	response = httptest.NewRecorder()
	handleAuthorize(response, request, cfg, store)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "URL이 아닌 요청 본문") || strings.Contains(response.Body.String(), "must-not-appear") {
		t.Fatal("query-password rejection or redaction regressed")
	}
}

func TestBrowserLanguageQualityRejectsInvalidWeights(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"ko-KR,en;q=0.8", "ko-KR"}, {"en;q=0.5,ko;q=0.9", "ko-KR"},
		{"ko;q=0,en;q=0.5", "en"}, {"ko;q=NaN,en;q=0.5", "en"},
		{"ko;q=Inf,en;q=0.5", "en"}, {"ko;q=2,en;q=0.5", "en"},
		{"ko;q=-1,zh;q=0.5", "zh-CN"}, {"fr;q=1,ko;q=0.7", "ko-KR"},
		{"en;q=0.9,ko;q=0.8", "en"}, {"zh;q=0.9,ko;q=0.8", "zh-CN"},
	} {
		t.Run(tc.header, func(t *testing.T) {
			if actual := preferredStatusPageText(tc.header).Lang; actual != tc.want {
				t.Fatalf("got %s want %s", actual, tc.want)
			}
		})
	}
}

func TestBrowserLocaleInventoryAndKoreanStatusPage(t *testing.T) {
	for _, value := range []any{statusPageEnglish, statusPageChinese, statusPageKorean, authorizePageEnglish, authorizePageChinese, authorizePageKorean} {
		fields := reflect.ValueOf(value)
		for index := 0; index < fields.NumField(); index++ {
			if strings.TrimSpace(fields.Field(index).String()) == "" {
				t.Errorf("empty locale field %s", fields.Type().Field(index).Name)
			}
		}
	}
	cfg := testConfig(t)
	cfg.OAuthEnabled = true
	cfg.AuthToken = "fixture-token-not-rendered"
	cfg.BrowserEnabled = true
	request := httptest.NewRequest(http.MethodGet, "https://fixture.example/", nil)
	request.Header.Set("Accept-Language", "ko-KR")
	response := httptest.NewRecorder()
	statusPageHandler(nil, cfg).ServeHTTP(response, request)
	body := html.UnescapeString(response.Body.String())
	for _, expected := range []string{`<html lang="ko-KR">`, "MCP 엔드포인트", "OAuth + 접근 토큰", `data-copied="복사됨"`, "소스 코드, 배포 버전", agentDockRepositoryURL, agentDockDocsURL, "QQ 커뮤니티"} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing Korean status %q", expected)
		}
	}
	if strings.Contains(body, cfg.AuthToken) || response.Header().Get("Content-Language") != "ko-KR" || response.Header().Get("Content-Security-Policy") != statusPageCSP {
		t.Fatal("status page leaked a token or lost security/language headers")
	}
	for code, texts := range authorizeBrowserErrors {
		for index, language := range []string{"en", "zh-CN", "ko-KR"} {
			if texts[index] == "" || strings.Count(texts[index], "%s") != strings.Count(texts[0], "%s") {
				t.Errorf("invalid browser error translation %s/%s", code, language)
			}
			request := httptest.NewRequest(http.MethodPost, "/oauth/authorize", nil)
			request.Header.Set("Accept-Language", language)
			response := httptest.NewRecorder()
			if code == "parameter_repeated" {
				writeAuthorizeBrowserError(response, request, code, 400, "client_id")
			} else {
				writeAuthorizeBrowserError(response, request, code, 400)
			}
			if response.Code != 400 || response.Header().Get("Vary") != "Accept-Language" || strings.Contains(response.Body.String(), "%!") {
				t.Errorf("invalid rendered browser error %s/%s", code, language)
			}
		}
	}
}
