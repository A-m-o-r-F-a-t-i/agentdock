package securepath

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CanonicalSystemAncestors expands only Darwin's root-owned /var, /tmp and
// /etc aliases to their exact /private targets. It never resolves user links,
// the final path component, or an alias with unexpected ownership/target.
// Callers must still reject linked entries throughout the returned path.
func CanonicalSystemAncestors(path string) string {
	clean := filepath.Clean(path)
	for _, name := range []string{"var", "tmp", "etc"} {
		alias := "/" + name
		if !strings.HasPrefix(clean, alias+"/") {
			continue
		}
		target := "/private/" + name
		link, err := os.Lstat(alias)
		if err != nil || link.Mode()&os.ModeSymlink == 0 || !rootOwned(link) {
			return path
		}
		value, err := os.Readlink(alias)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join("/", value)
		}
		if filepath.Clean(value) != target {
			return path
		}
		for _, parent := range []string{"/", "/private"} {
			info, err := os.Lstat(parent)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !rootOwned(info) || info.Mode().Perm()&0022 != 0 {
				return path
			}
		}
		info, err := os.Lstat(target)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !rootOwned(info) {
			return path
		}
		return target + strings.TrimPrefix(clean, alias)
	}
	return path
}

func rootOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
