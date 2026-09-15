//go:build windows

package desktopruntime

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestTaskCOMProceduresResolve(t *testing.T) {
	for _, test := range []struct {
		name string
		find func() error
	}{
		{name: "CoInitializeEx", find: procCoInitializeEx.Find},
		{name: "CoUninitialize", find: procCoUninitialize.Find},
		{name: "CoCreateInstance", find: procCoCreateInstance.Find},
		{name: "CLSIDFromProgID", find: procCLSIDFromProgID.Find},
		{name: "SysAllocString", find: procSysAllocString.Find},
		{name: "SysFreeString", find: procSysFreeString.Find},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.find(); err != nil {
				t.Fatalf("resolve Windows COM procedure %s: %v", test.name, err)
			}
		})
	}
}

// VARIANT includes the two-pointer BRECORD union even when only I4/BSTR is used.
func TestTaskCOMVariantABI(t *testing.T) {
	want := uintptr(8) + 2*unsafe.Sizeof(uintptr(0))
	if got := unsafe.Sizeof(variant{}); got != want {
		t.Fatalf("VARIANT size=%d, want %d on %s; IDispatch argument stride is invalid", got, want, runtime.GOARCH)
	}
	if got := unsafe.Offsetof(variant{}.Val); got != 8 {
		t.Fatalf("VARIANT union offset=%d, want 8", got)
	}
}

func TestTaskCOMMultipleArguments(t *testing.T) {
	// Check layout before asking COM to write a result buffer; a short buffer can corrupt memory.
	if unsafe.Sizeof(variant{}) != 8+2*unsafe.Sizeof(uintptr(0)) {
		t.Fatal("invalid VARIANT ABI; refusing unsafe COM invocation")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coInitApartmentThreaded)
	if hr != 0 && hr != 1 {
		t.Fatalf("CoInitializeEx: 0x%X", hr)
	}
	defer procCoUninitialize.Call()
	dictionary, err := createDispatch("Scripting.Dictionary")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDispatch(dictionary)
	if _, err := invokeDispatch(dictionary, "Add", dispatchMethod, variantBSTR("installer-argument"), variantInt32(42)); err != nil {
		t.Fatalf("two-argument IDispatch call: %v", err)
	}
	result, err := invokeDispatch(dictionary, "Item", dispatchPropertyGet, variantBSTR("installer-argument"))
	if err != nil {
		t.Fatal(err)
	}
	if got := variantInt(result); got != 42 {
		t.Fatalf("Item=%d, want 42", got)
	}
}
