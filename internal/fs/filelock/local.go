package filelock

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type localGate struct {
	token      chan struct{}
	references int
}

var localGates = struct {
	sync.Mutex
	entries map[string]*localGate
}{entries: make(map[string]*localGate)}

func acquireLocal(ctx context.Context, path string) (func(), error) {
	key, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	localGates.Lock()
	gate := localGates.entries[key]
	if gate == nil {
		gate = &localGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		localGates.entries[key] = gate
	}
	gate.references++
	localGates.Unlock()
	dropReference := func() {
		localGates.Lock()
		gate.references--
		if gate.references == 0 {
			delete(localGates.entries, key)
		}
		localGates.Unlock()
	}
	select {
	case <-ctx.Done():
		dropReference()
		return nil, ctx.Err()
	case <-gate.token:
	}
	var once sync.Once
	return func() { once.Do(func() { gate.token <- struct{}{}; dropReference() }) }, nil
}

// RecoverAbandoned removes only the exact lock shape of a confirmed dead
// process (or an abandoned empty initialization). Live and unknown owners are
// preserved. New installers call it before launching older rollback binaries.
func RecoverAbandoned(path string) bool {
	return removeSafeStale(path, time.Now())
}
