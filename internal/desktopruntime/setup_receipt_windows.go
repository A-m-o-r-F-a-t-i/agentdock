//go:build windows

package desktopruntime

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

const maximumSetupReceiptBytes = 64 << 10

// Atomic replacement can briefly collide with a scanner's exclusive handle.
// Treat only missing/sharing/lock states as pending. The broker keeps its
// existing deadline and nonce checks; access denial and damaged JSON still fail.
func setupReceiptPending(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func readSetupReceipt(path string) (setupLaunchResult, bool, error) {
	var result setupLaunchResult
	file, err := os.Open(path)
	if err != nil {
		if setupReceiptPending(err) {
			return result, false, nil
		}
		return result, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximumSetupReceiptBytes {
		return result, false, errors.New("invalid or oversized native setup receipt")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumSetupReceiptBytes+1))
	if err != nil {
		if setupReceiptPending(err) {
			return result, false, nil
		}
		return result, false, err
	}
	if len(data) > maximumSetupReceiptBytes {
		return result, false, errors.New("native setup receipt exceeds size limit")
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, false, err
	}
	return result, true, nil
}
