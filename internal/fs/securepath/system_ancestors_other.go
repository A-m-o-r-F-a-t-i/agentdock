//go:build !darwin

package securepath

// Non-Darwin systems do not receive Darwin-specific link exceptions.
func CanonicalSystemAncestors(path string) string { return path }
