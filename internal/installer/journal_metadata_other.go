//go:build !windows

package installer

const backupNativeMetadataVersion = 0

type backupNativeMetadata struct{}

func readBackupNativeMetadata(string) (*backupNativeMetadata, error)   { return nil, nil }
func applyBackupNativeMetadata(string, *backupNativeMetadata) error    { return nil }
func backupNativeMetadataSize(*backupNativeMetadata) int               { return 0 }
func equalBackupNativeMetadata(a, b *backupNativeMetadata) bool        { return a == b }
func backupNativeMetadataDifference(a, b *backupNativeMetadata) string { return "metadata mismatch" }
