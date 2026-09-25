package installer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"unicode/utf16"
	"unsafe"
)

const backupNativeMetadataVersion = 1
const backupSecurityInformation = windows.OWNER_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION
const backupSettableAttributes = windows.FILE_ATTRIBUTE_READONLY | windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM | windows.FILE_ATTRIBUTE_ARCHIVE | windows.FILE_ATTRIBUTE_TEMPORARY | windows.FILE_ATTRIBUTE_NOT_CONTENT_INDEXED

type backupNativeMetadata struct {
	Security   string `json:"owner_group_dacl"`
	Attributes uint32 `json:"attributes"`
}

var setBackupFileSecurity = windows.NewLazySystemDLL("advapi32.dll").NewProc("SetFileSecurityW")

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
	var ea [4]byte
	if err = windows.NtQueryInformationFile(handle, &status, &ea[0], 4, 7); err != nil {
		return nil, fmt.Errorf("cannot establish extended-attribute fidelity: %w", err)
	}
	if binary.LittleEndian.Uint32(ea[:]) != 0 {
		return nil, errors.New("backup refuses NTFS extended attributes; original retained")
	}
	var streams [65536]byte
	err = windows.GetFileInformationByHandleEx(handle, 7, &streams[0], uint32(len(streams)))
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
			if next < 24+length || next > len(streams)-offset {
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

func applyBackupNativeMetadata(path string, metadata *backupNativeMetadata) error {
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
	// SetFileSecurity applies the supplied descriptor instead of deriving rights
	// from the temporary parent. A second read requires actual metadata fidelity.
	result, _, callErr := setBackupFileSecurity.Call(uintptr(unsafe.Pointer(name)), uintptr(backupSecurityInformation), uintptr(unsafe.Pointer(security)))
	if result == 0 {
		return fmt.Errorf("restore Windows security metadata: %w", callErr)
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
