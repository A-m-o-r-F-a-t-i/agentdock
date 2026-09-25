//go:build windows

package desktopruntime

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"unsafe"
)

// Only these same-user coordination objects permit Medium/High communication.
// This does not change filesystem ACLs, process tokens, Task ACLs or OS policy.
func runtimeCoordinationAttributes() (*windows.SecurityAttributes, string, error) {
	sid, err := tokenUserSID(windows.GetCurrentProcessToken())
	if err != nil {
		return nil, "", err
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "G:" + sid + "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + sid + ")S:(ML;;NW;;;ME)")
	if err != nil {
		return nil, "", err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}, sid, nil
}
func validateRuntimeCoordinationObject(handle windows.Handle, sid string) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || owner.String() != sid {
		return errors.New("runtime coordination object has an unexpected owner")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 3 {
		return errors.New("runtime coordination object does not have a private DACL")
	}
	allowed := map[string]bool{sid: false, "S-1-5-18": false, "S-1-5-32-544": false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
			return errors.New("unexpected coordination ACE")
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		seen, known := allowed[principal]
		if !known || seen {
			return errors.New("coordination DACL includes an untrusted or duplicate principal")
		}
		allowed[principal] = true
		if ace.Mask != windows.GENERIC_ALL && ace.Mask != 0x1f0001 && ace.Mask != 0x1f0003 {
			return errors.New("unexpected coordination access mask")
		}
	}
	label, _, err := descriptor.SACL()
	if err != nil || label == nil || label.AceCount != 1 {
		return errors.New("coordination object has no explicit integrity boundary")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(label, 0, &ace); err != nil {
		return err
	}
	if ace.Header.AceType != 0x11 /* SYSTEM_MANDATORY_LABEL_ACE_TYPE, winnt.h */ || (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String() != "S-1-16-8192" || ace.Mask != 1 {
		return fmt.Errorf("coordination object integrity boundary is not Medium/no-write-up")
	}
	return nil
}
