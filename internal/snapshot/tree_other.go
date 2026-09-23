//go:build !windows

package snapshot

import "github.com/fsnotify/fsnotify"

func startTreeWatch(_ string, _ func(fsnotify.Event), _ func()) (func(), bool) { return nil, false }
