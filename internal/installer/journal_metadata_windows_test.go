package installer

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

func nativeBackupTest(t *testing.T) {
	t.Helper()
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("native metadata/recovery fixture runs only on isolated GitHub Windows")
	}
}

func TestWindowsBackupPreservesOwnerACLAndAttributes(t *testing.T) {
	nativeBackupTest(t)
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "inherited", true: "protected"}[protected], func(t *testing.T) {
			root := t.TempDir()
			original := filepath.Join(root, "original")
			if err = os.Mkdir(original, 0700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(original, "settings.json")
			if err = os.WriteFile(source, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			metadata, err := readBackupNativeMetadata(source)
			if err != nil {
				t.Fatal(err)
			}
			if protected {
				metadata.Security = "O:" + token.User.Sid.String() + "G:" + token.User.Sid.String() + "D:P(A;;FA;;;" + token.User.Sid.String() + ")(A;;FR;;;BU)"
			}
			metadata.Attributes = windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_READONLY | windows.FILE_ATTRIBUTE_ARCHIVE
			if err = applyBackupNativeMetadata(source, metadata); err != nil {
				t.Fatal(err)
			}
			expected, err := readBackupNativeMetadata(source)
			if err != nil {
				t.Fatal(err)
			}
			j := newJournal(filepath.Join(root, "state"), "native-backup")
			if err = j.Snapshot(original); err != nil {
				t.Fatal(err)
			}
			name, _ := windows.UTF16PtrFromString(source)
			if err = windows.SetFileAttributes(name, windows.FILE_ATTRIBUTE_NORMAL); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(source, []byte("new-generation"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = j.Restore(t.Context(), Request{}); err != nil {
				t.Fatal(err)
			}
			actual, err := readBackupNativeMetadata(source)
			if err != nil || !equalBackupNativeMetadata(actual, expected) {
				t.Fatalf("permissions or attributes changed: expected=%+v actual=%+v error=%v", expected, actual, err)
			}
			assertJournalFile(t, source, "original")
			if err = j.Restore(t.Context(), Request{}); err != nil {
				t.Fatal(err)
			}
			// Only this temporary fixture is cleared; readonly must not hinder test cleanup.
			_ = windows.SetFileAttributes(name, windows.FILE_ATTRIBUTE_NORMAL)
		})
	}
}

func TestWindowsBackupRejectsADSAndEABeforeJournalCommit(t *testing.T) {
	nativeBackupTest(t)
	for _, kind := range []string{"ads", "ea"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "original.txt")
			if err := os.WriteFile(source, []byte("keep-original"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "ads" {
				if err := os.WriteFile(source+":retained", []byte("must-not-drop"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				name, _ := windows.UTF16PtrFromString(source)
				handle, err := windows.CreateFile(name, windows.FILE_WRITE_EA, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				// FILE_FULL_EA_INFORMATION: next/flags/name length/value length/name/NUL/value.
				data := []byte{0, 0, 0, 0, 0, 4, 3, 0, 't', 'e', 's', 't', 0, 'e', 'a', '1'}
				var status windows.IO_STATUS_BLOCK
				result, _, _ := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtSetEaFile").Call(uintptr(handle), uintptr(unsafe.Pointer(&status)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
				windows.CloseHandle(handle)
				if result != 0 {
					t.Fatalf("NTFS EA fixture failed: %#x", result)
				}
			}
			j := newJournal(filepath.Join(root, "state"), kind)
			err := j.Snapshot(source)
			if err == nil || !strings.Contains(err.Error(), map[string]string{"ads": "alternate data streams", "ea": "extended attributes"}[kind]) {
				t.Fatalf("unsupported metadata was lost: %v", err)
			}
			if len(j.Backups) != 0 {
				t.Fatal("unsupported backup was committed")
			}
			assertJournalFile(t, source, "keep-original")
			if kind == "ads" {
				assertJournalFile(t, source+":retained", "must-not-drop")
			}
		})
	}
}

func TestWindowsRecoverySharingViolationKeepsOriginalAndResumes(t *testing.T) {
	nativeBackupTest(t)
	j, state, original := journalFixture(t)
	source := filepath.Join(original, "secret")
	name, _ := windows.UTF16PtrFromString(source)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = j.Restore(t.Context(), Request{})
	windows.CloseHandle(handle)
	if err == nil {
		t.Fatal("native open file did not block replacement as expected")
	}
	assertJournalFile(t, source, "current-secret")
	loaded, err := loadJournal(state, "recovery-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = loaded.Restore(t.Context(), Request{}); err != nil {
		t.Fatal(err)
	}
	assertJournalFile(t, source, "original-secret")
}

func TestWindowsRecoveryRejectsChangedACLBeforeMovingCurrent(t *testing.T) {
	nativeBackupTest(t)
	j, _, original := journalFixture(t)
	if err := j.prepareRestore(t.Context()); err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(j.RestoreEntries[0].Stage, "prepared", "secret")
	metadata, err := readBackupNativeMetadata(prepared)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Attributes ^= windows.FILE_ATTRIBUTE_HIDDEN
	if err = applyBackupNativeMetadata(prepared, metadata); err != nil {
		t.Fatal(err)
	}
	if err = j.Restore(t.Context(), Request{}); err == nil {
		t.Fatal("changed metadata passed integrity check")
	}
	assertJournalFile(t, filepath.Join(original, "secret"), "current-secret")
	if _, err = os.Stat(j.Dir); errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery evidence removed")
	}
}
