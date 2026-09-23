//go:build windows

package snapshot

import (
	"encoding/binary"
	"errors"
	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/windows"
	"path/filepath"
	"runtime"
	"sync"
	"unicode/utf16"
	"unsafe"
)

func startTreeWatch(root string, emit func(fsnotify.Event), failed func()) (func(), bool) {
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, false
	}
	handle, err := windows.CreateFile(path, windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, false
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, false
	}
	var mu sync.Mutex
	closed := false
	done := make(chan struct{})
	ready := make(chan struct{})
	var once sync.Once
	ov := windows.Overlapped{HEvent: event}
	go func() {
		defer close(done)
		first := true
		aligned := make([]uint32, 16384)
		buffer := unsafe.Slice((*byte)(unsafe.Pointer(&aligned[0])), 65536)
		for {
			mu.Lock()
			if closed {
				mu.Unlock()
				if first {
					close(ready)
				}
				return
			}
			_ = windows.ResetEvent(event)
			err := windows.ReadDirectoryChanges(handle, &buffer[0], uint32(len(buffer)), true, windows.FILE_NOTIFY_CHANGE_FILE_NAME|windows.FILE_NOTIFY_CHANGE_DIR_NAME|windows.FILE_NOTIFY_CHANGE_SIZE|windows.FILE_NOTIFY_CHANGE_LAST_WRITE|windows.FILE_NOTIFY_CHANGE_ATTRIBUTES, nil, &ov, 0)
			mu.Unlock()
			if first {
				close(ready)
				first = false
			}
			if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
				failed()
				return
			}
			var n uint32
			err = windows.GetOverlappedResult(handle, &ov, &n, true)
			runtime.KeepAlive(aligned)
			mu.Lock()
			stopping := closed
			mu.Unlock()
			if stopping {
				return
			}
			if err != nil {
				failed()
				return
			}
			if n == 0 || int(n) > len(buffer) {
				failed()
				continue
			}
			valid := true
			for offset := 0; offset < int(n); {
				if offset+12 > int(n) {
					valid = false
					break
				}
				next := int(binary.LittleEndian.Uint32(buffer[offset : offset+4]))
				action := binary.LittleEndian.Uint32(buffer[offset+4 : offset+8])
				length := int(binary.LittleEndian.Uint32(buffer[offset+8 : offset+12]))
				if length%2 != 0 || length < 0 || offset+12+length > int(n) {
					valid = false
					break
				}
				units := make([]uint16, length/2)
				for i := range units {
					units[i] = binary.LittleEndian.Uint16(buffer[offset+12+i*2 : offset+14+i*2])
				}
				name := filepath.Join(root, string(utf16.Decode(units)))
				op := fsnotify.Write
				switch action {
				case windows.FILE_ACTION_ADDED, windows.FILE_ACTION_RENAMED_NEW_NAME:
					op = fsnotify.Create
				case windows.FILE_ACTION_REMOVED:
					op = fsnotify.Remove
				case windows.FILE_ACTION_RENAMED_OLD_NAME:
					op = fsnotify.Rename
				}
				emit(fsnotify.Event{Name: name, Op: op})
				if next == 0 {
					break
				}
				if next < 12 || offset+next >= int(n) {
					valid = false
					break
				}
				offset += next
			}
			if !valid {
				failed()
			}
		}
	}()
	<-ready
	closeWatch := func() {
		once.Do(func() {
			mu.Lock()
			closed = true
			_ = windows.CancelIoEx(handle, &ov)
			mu.Unlock()
			// The OVERLAPPED and its buffers stay alive until cancellation completes.
			<-done
			_ = windows.CloseHandle(event)
			_ = windows.CloseHandle(handle)
		})
	}
	return closeWatch, true
}
