package desktopruntime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testTailscaleNode() tailscaleNode {
	return tailscaleNode{BackendState: "Running", ID: "test-node", HostName: "device", DNSName: "device.example-tailnet.ts.net", Online: true,
		capabilities: map[string]bool{"funnel": true, "https": true, "https://tailscale.com/cap/funnel-ports?ports=443,8443,10000": true}}
}

func TestTailscaleNodeStatus(t *testing.T) {
	data := []byte(`{"BackendState":"Running","Self":{"ID":"test-node","HostName":"device","DNSName":"device.example-tailnet.ts.net.","Online":true,"CapMap":{"funnel":null,"https":null,"https://tailscale.com/cap/funnel-ports?ports=443,8443,10000":null}},"Peer":{"ignored":{"PrivateKey":"never retained"}}}`)
	node, err := decodeTailscaleNode(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.checkFunnelReady(time.Now()); err != nil {
		t.Fatal(err)
	}
	if node.DNSName != testTailscaleNode().DNSName {
		t.Fatal(node.DNSName)
	}
	encoded, _ := json.Marshal(node)
	if strings.Contains(string(encoded), "never retained") || strings.Contains(string(encoded), "Peer") {
		t.Fatal("peer data retained")
	}
	for _, input := range []string{`{`, `{}`, `null`, `{"BackendState":"Running"}`, `{"BackendState":"Running","Self":{"DNSName":"https://bad.ts.net"}}`} {
		if _, err := decodeTailscaleNode([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestTailscaleNodeDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		edit func(*tailscaleNode)
		code string
	}{
		{"login", func(n *tailscaleNode) { n.BackendState = "NeedsLogin" }, "needs_login"},
		{"stopped", func(n *tailscaleNode) { n.BackendState = "Stopped" }, "not_running"},
		{"offline", func(n *tailscaleNode) { n.Online = false }, "offline"},
		{"missing DNS", func(n *tailscaleNode) { n.DNSName = "" }, "missing_identity"},
		{"no funnel", func(n *tailscaleNode) { delete(n.capabilities, "funnel") }, "funnel_permission_required"},
		{"expired", func(n *tailscaleNode) { v := time.Now().Add(-time.Hour); n.KeyExpiry = &v }, "key_expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := testTailscaleNode()
			tc.edit(&n)
			if got := tailscaleDiagnosticCode(n.checkFunnelReady(time.Now())); got != tc.code {
				t.Fatalf("%s != %s", got, tc.code)
			}
		})
	}
}

func TestTailscaleServeJSONFailsClosed(t *testing.T) {
	for _, input := range []string{`null`, `{}`, `{"TCP":{},"Web":{},"AllowFunnel":{}}`} {
		if _, err := decodeTailscaleServe([]byte(input)); err != nil {
			t.Errorf("%s: %v", input, err)
		}
	}
	for _, input := range []string{
		``, `{`, `[]`, `{} {}`, `{"UnknownConfig":true}`, `{"TCP":{"443":null}}`,
		`{"TCP":{"65536":{"HTTPS":true}}}`, `{"Web":{"x:443":null}}`,
		`{"Web":{"x:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8765","UnknownField":true}}}}}`,
		`{"Foreground":{"a":{"Foreground":{"b":{}}}}}`, strings.Repeat("x", tailscaleJSONLimit+1),
	} {
		if _, err := decodeTailscaleServe([]byte(input)); err == nil {
			t.Errorf("accepted incompatible JSON %.100s", input)
		}
	}
}

func testServeWithHandlers(public bool, handlers map[string]*tailscaleHTTPHandler) *tailscaleServeConfig {
	hostPort := testTailscaleNode().DNSName + ":443"
	return &tailscaleServeConfig{TCP: map[string]*tailscaleTCPHandler{"443": {HTTPS: true}}, Web: map[string]*tailscaleWebConfig{hostPort: {Handlers: handlers}}, AllowFunnel: map[string]bool{hostPort: public}}
}

func TestTailscalePortConflicts(t *testing.T) {
	hostPort := testTailscaleNode().DNSName + ":443"
	cases := []*tailscaleServeConfig{
		{TCP: map[string]*tailscaleTCPHandler{"443": {TCPForward: "127.0.0.1:22"}}},
		{TCP: map[string]*tailscaleTCPHandler{"443": {HTTPS: true}}},
		{Foreground: map[string]*tailscaleServeConfig{"session": {TCP: map[string]*tailscaleTCPHandler{"443": {HTTPS: true}}}}},
		testServeWithHandlers(true, map[string]*tailscaleHTTPHandler{"/": {Proxy: "http://127.0.0.1:8765"}}),
	}
	cases[3].Web["other.example-tailnet.ts.net:443"] = cases[3].Web[hostPort]
	for _, config := range cases {
		if err := config.checkHTTPSPort(hostPort); err == nil {
			t.Fatalf("accepted conflict: %+v", config)
		}
	}
}

func TestTailscaleUnownedProjection(t *testing.T) {
	node := testTailscaleNode()
	before := testServeWithHandlers(true, map[string]*tailscaleHTTPHandler{"/other": {Proxy: "http://127.0.0.1:9000"}})
	after := cloneTailscaleServe(before)
	after.Web[node.DNSName+":443"].Handlers["/"] = &tailscaleHTTPHandler{Proxy: "http://127.0.0.1:8765"}
	if !sameTailscaleServe(unownedTailscaleServe(before, node.DNSName+":443", []string{"/"}), unownedTailscaleServe(after, node.DNSName+":443", []string{"/"})) {
		t.Fatal("root addition changed unowned projection")
	}
	after.Web[node.DNSName+":443"].Handlers["/other"].Proxy = "http://127.0.0.1:9001"
	if sameTailscaleServe(unownedTailscaleServe(before, node.DNSName+":443", []string{"/"}), unownedTailscaleServe(after, node.DNSName+":443", []string{"/"})) {
		t.Fatal("foreign mutation was missed")
	}
}
