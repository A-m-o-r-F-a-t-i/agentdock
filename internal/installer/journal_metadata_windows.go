package installer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"io"
	"runtime"
	"unicode/utf16"
	"unsafe"
)

const backupNativeMetadataVersion = 1
const nativeFileEAInformation = 7
const nativeFileStreamInformation = 7
const backupSecurityInformation = windows.OWNER_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION
const backupSettableAttributes = windows.FILE_ATTRIBUTE_READONLY | windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM | windows.FILE_ATTRIBUTE_ARCHIVE | windows.FILE_ATTRIBUTE_TEMPORARY | windows.FILE_ATTRIBUTE_NOT_CONTENT_INDEXED

type backupNativeMetadata struct {
	Security   string `json:"owner_group_dacl"`
	Attributes uint32 `json:"attributes"`
}

var writeBackupSecurity = windows.NewLazySystemDLL("kernel32.dll").NewProc("BackupWrite")

func readBackupNativeMetadata(path string) (*backupNativeMetadata, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return nil, err
	}
	if attributes & ^uint32(backupSettableAttributes|windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_NORMAL) != 0 {
		return nil, errors.New("backup refuses unsupported file attributes, reparse, encrypted, sparse or compressed data")
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_EA|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(handle)
	// Extended attributes and named streams are explicitly refused until they
	// have a bounded preservation contract. Never silently drop those data.
	var status windows.IO_STATUS_BLOCK
	// NtQueryInformationFile probes native alignment even on x64. A small
	// Go byte array may be packed at an unaligned address by the allocator.
	var ea uint64
	if err = windows.NtQueryInformationFile(handle, &status, (*byte)(unsafe.Pointer(&ea)), 4, nativeFileEAInformation); err != nil {
		return nil, fmt.Errorf("cannot establish extended-attribute fidelity: %w", err)
	}
	if uint32(ea) != 0 {
		return nil, errors.New("backup refuses NTFS extended attributes; original retained")
	}
	// FILE_STREAM_INFO requires an 8-byte base and entry alignment.
	// Keep native storage aligned, while parsing bounded bytes explicitly.
	var streamStorage [8192]uint64
	streams := unsafe.Slice((*byte)(unsafe.Pointer(&streamStorage[0])), len(streamStorage)*8)
	err = windows.GetFileInformationByHandleEx(handle, nativeFileStreamInformation, &streams[0], uint32(len(streams)))
	if err != nil && !errors.Is(err, windows.ERROR_HANDLE_EOF) {
		return nil, fmt.Errorf("cannot enumerate file streams: %w", err)
	}
	if err == nil {
		for offset := 0; ; {
			if offset+24 > len(streams) {
				return nil, errors.New("invalid stream enumeration")
			}
			next := int(binary.LittleEndian.Uint32(streams[offset:]))
			length := int(binary.LittleEndian.Uint32(streams[offset+4:]))
			if length < 0 || length%2 != 0 || length > len(streams)-offset-24 {
				return nil, errors.New("invalid stream name")
			}
			units := make([]uint16, length/2)
			for i := range units {
				units[i] = binary.LittleEndian.Uint16(streams[offset+24+i*2:])
			}
			name := string(utf16.Decode(units))
			if name != "" && name != "::$DATA" {
				return nil, errors.New("backup refuses alternate data streams; original retained")
			}
			if next == 0 {
				break
			}
			if next%8 != 0 || next < 24+length || next > len(streams)-offset {
				return nil, errors.New("invalid stream offset")
			}
			offset += next
		}
	}
	security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, backupSecurityInformation)
	if err != nil {
		return nil, err
	}
	encoded := security.String()
	if encoded == "" || len(encoded) > 65536 {
		return nil, errors.New("unsupported security descriptor size")
	}
	return &backupNativeMetadata{Security: encoded, Attributes: attributes & backupSettableAttributes}, nil
}

func applyBackupNativeMetadata(path string, metadata *backupNativeMetadata) (resultErr error) {
	if metadata == nil {
		return errors.New("missing Windows backup metadata")
	}
	security, err := windows.SecurityDescriptorFromString(metadata.Security)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	length := int(security.Length())
	if length < 20 || length > 65536 {
		return errors.New("invalid backup security descriptor size")
	}
	// BACKUP_SECURITY_DATA is the documented restoration path. General ACL
	// editing APIs can recompute inherited ACEs from our private stage parent.
	// No privilege is enabled here: the handle must grant WRITE_DAC/WRITE_OWNER.
	handle, err := windows.CreateFile(name, windows.WRITE_DAC|windows.WRITE_OWNER|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	storage := make([]uint64, (20+length+7)/8)
	buffer := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), 20+length)
	binary.LittleEndian.PutUint32(buffer[0:], 3) // BACKUP_SECURITY_DATA
	binary.LittleEndian.PutUint32(buffer[4:], 2) // STREAM_CONTAINS_SECURITY
	binary.LittleEndian.PutUint64(buffer[8:], uint64(length))
	copy(buffer[20:], unsafe.Slice((*byte)(unsafe.Pointer(security)), length))
	runtime.KeepAlive(security)
	var context uintptr
	defer func() {
		if context != 0 {
			ok, _, failure := writeBackupSecurity.Call(uintptr(handle), 0, 0, 0, 1, 1, uintptr(unsafe.Pointer(&context)))
			if ok == 0 {
				resultErr = errors.Join(resultErr, fmt.Errorf("release backup restoration context: %w", failure))
			}
		}
	}()
	var written uint32
	ok, _, failure := writeBackupSecurity.Call(uintptr(handle), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&written)), 0, 1, uintptr(unsafe.Pointer(&context)))
	runtime.KeepAlive(storage)
	if ok == 0 {
		return fmt.Errorf("restore backup security stream: %w", failure)
	}
	if int(written) != len(buffer) {
		return io.ErrShortWrite
	}
	attributes := metadata.Attributes
	if attributes == 0 {
		attributes = windows.FILE_ATTRIBUTE_NORMAL
	}
	return windows.SetFileAttributes(name, attributes)
}

func backupNativeMetadataSize(value *backupNativeMetadata) int {
	if value == nil {
		return 0
	}
	return len(value.Security) + 4
}
func equalBackupNativeMetadata(a, b *backupNativeMetadata) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func backupNativeMetadataDifference(actual, expected *backupNativeMetadata) string {
	if actual == nil || expected == nil {
		return "metadata unavailable"
	}
	if actual.Attributes != expected.Attributes {
		return fmt.Sprintf("attribute mismatch: actual=%#x expected=%#x", actual.Attributes, expected.Attributes)
	}
	if actual.Security != expected.Security {
		a, ae := windows.SecurityDescriptorFromString(actual.Security)
		b, be := windows.SecurityDescriptorFromString(expected.Security)
		if ae == nil && be == nil {
			ac, _, _ := a.Control()
			bc, _, _ := b.Control()
			return fmt.Sprintf("security descriptor mismatch: actual control=%#x expected control=%#x", ac, bc)
		}
		return "security descriptor mismatch"
	}
	return "metadata matches"
}
