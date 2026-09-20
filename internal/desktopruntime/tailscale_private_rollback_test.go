package desktopruntime

import (
	"context"
	"testing"
)

func TestTailscalePrivateMCPMigrationRestoresVisibility(t *testing.T) {
	for _, failure := range []int{0, 1, 2} {
		for _, partial := range []bool{false, true} {
			target := "http://127.0.0.1:8765"
			original := testServeWithHandlers(false, map[string]*tailscaleHTTPHandler{"/mcp": {Proxy: target + "/mcp"}})
			delete(original.AllowFunnel, testTailscaleNode().DNSName+":443")
			fake := newMemoryTailscale(original)
			fake.failWrite, fake.partialWrite = failure, partial
			change, err := prepareTailscaleMapping(fake.node, fake.config, target, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			err = change.apply(context.Background(), fake.client())
			if (err == nil) != (failure == 0) {
				t.Fatalf("failure=%d partial=%v: %v", failure, partial, err)
			}
			if err := change.rollback(context.Background(), fake.client()); err != nil {
				t.Fatalf("failure=%d partial=%v: %v", failure, partial, err)
			}
			if !sameTailscaleServe(original, fake.config) {
				t.Fatalf("private mapping not restored: failure=%d partial=%v", failure, partial)
			}
		}
	}
}
