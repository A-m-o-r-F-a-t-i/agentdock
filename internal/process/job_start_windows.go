//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job creates its children inside the kill-on-close boundary, before their
// initial thread can execute. The job handle is never inherited. There is no
// Start/Attach fallback: a policy or unsupported platform fails closed.
type Job struct {
	mu     sync.Mutex
	handle windows.Handle
}
type JobChild struct {
	Handle  windows.Handle
	PID     uint32
	Created uint64
}

func NewJob() (*Job, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(handle, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	return &Job{handle: handle}, nil
}

func (job *Job) Close() error {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.handle == 0 {
		return nil
	}
	h := job.handle
	job.handle = 0
	return windows.CloseHandle(h)
}

func (job *Job) Start(binary string, args []string, directory string, environment []string, stdin, stdout, stderr *os.File) (*JobChild, error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.handle == 0 {
		return nil, errors.New("job is closed")
	}
	if !filepath.IsAbs(binary) || !filepath.IsAbs(directory) {
		return nil, errors.New("job child requires absolute executable and directory")
	}
	app, err := windows.UTF16PtrFromString(binary)
	if err != nil {
		return nil, err
	}
	dir, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return nil, err
	}
	quoted := []string{syscall.EscapeArg(binary)}
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return nil, errors.New("NUL in child argument")
		}
		quoted = append(quoted, syscall.EscapeArg(arg))
	}
	command, err := windows.UTF16FromString(strings.Join(quoted, " "))
	if err != nil {
		return nil, err
	}
	// Match os/exec's Windows last-value-wins, case-insensitive environment
	// contract even though CreateProcess receives our explicit UTF-16 block.
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		if strings.ContainsRune(entry, 0) {
			return nil, errors.New("NUL in child environment")
		}
		start := 0
		if strings.HasPrefix(entry, "=") {
			start = 1
		}
		equals := strings.IndexByte(entry[start:], '=')
		if equals < 0 {
			return nil, errors.New("child environment entry has no value separator")
		}
		equals += start
		values[strings.ToUpper(entry[:equals])] = entry
	}
	env := make([]string, 0, len(values))
	for _, entry := range values {
		env = append(env, entry)
	}
	sort.SliceStable(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	handles := make([]windows.Handle, 0, 3)
	defer func() {
		for _, h := range handles {
			windows.CloseHandle(h)
		}
	}()
	for _, file := range []*os.File{stdin, stdout, stderr} {
		if file == nil {
			return nil, errors.New("job child requires explicit standard handles")
		}
		var h windows.Handle
		if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(), &h, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return nil, err
		}
		handles = append(handles, h)
	}
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	// PROC_THREAD_ATTRIBUTE_JOB_LIST = ProcThreadAttributeValue(13, false, true, false).
	const jobList = 0x0002000d
	if err := attributes.Update(jobList, unsafe.Pointer(&job.handle), unsafe.Sizeof(job.handle)); err != nil {
		return nil, fmt.Errorf("create-time job assignment: %w", err)
	}
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	startup.ShowWindow = windows.SW_HIDE
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[2]
	var info windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(app, &command[0], nil, nil, true, flags, &block[0], dir, &startup.StartupInfo, &info); err != nil {
		return nil, fmt.Errorf("create job child: %w", err)
	}
	windows.CloseHandle(info.Thread)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(info.Process, &created, &exited, &kernel, &user); err != nil {
		windows.TerminateProcess(info.Process, 1)
		windows.CloseHandle(info.Process)
		return nil, err
	}
	return &JobChild{Handle: info.Process, PID: info.ProcessId, Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}, nil
}

func (child *JobChild) Alive() (bool, error) {
	if child == nil || child.Handle == 0 {
		return false, nil
	}
	state, err := windows.WaitForSingleObject(child.Handle, 0)
	if err != nil {
		return false, err
	}
	if state == uint32(windows.WAIT_TIMEOUT) {
		return true, nil
	}
	if state == windows.WAIT_OBJECT_0 {
		return false, nil
	}
	return false, fmt.Errorf("unexpected child wait: %#x", state)
}
func (child *JobChild) Wait(ctx context.Context) (uint32, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		state, err := windows.WaitForSingleObject(child.Handle, 100)
		if err != nil {
			return 0, err
		}
		if state == windows.WAIT_OBJECT_0 {
			var code uint32
			err = windows.GetExitCodeProcess(child.Handle, &code)
			return code, err
		}
		if state != uint32(windows.WAIT_TIMEOUT) {
			return 0, fmt.Errorf("unexpected child wait: %#x", state)
		}
	}
}
func (child *JobChild) Stop(ctx context.Context) error {
	alive, err := child.Alive()
	if err != nil || !alive {
		return err
	}
	if err := windows.TerminateProcess(child.Handle, 1); err != nil {
		return err
	}
	_, err = child.Wait(ctx)
	return err
}
func (child *JobChild) Close() error {
	if child == nil || child.Handle == 0 {
		return nil
	}
	h := child.Handle
	child.Handle = 0
	return windows.CloseHandle(h)
}
