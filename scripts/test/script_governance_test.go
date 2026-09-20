package scripts

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestScriptGovernanceInventoryCoversWorkspaceScripts(t *testing.T) {
	root := filepath.Join("..", "..")
	inventoryPath := filepath.Join(root, "scripts", "governance", "inventory.yaml")
	inventory, err := parseGovernanceInventory(inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Threshold <= 0 {
		t.Fatal("review_threshold_lines is required")
	}

	listed := map[string]governanceScript{}
	for _, script := range inventory.Scripts {
		if script.Path == "" || script.Class == "" {
			t.Fatalf("inventory entry missing path or class: %+v", script)
		}
		listed[filepath.ToSlash(script.Path)] = script
	}

	tracked, err := gitWorkspaceScripts(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range tracked {
		entry, ok := listed[path]
		if !ok {
			t.Fatalf("workspace script is missing from scripts/governance/inventory.yaml: %s", path)
		}
		full := filepath.Join(root, filepath.FromSlash(path))
		lines, err := countLines(full)
		if err != nil {
			t.Fatal(err)
		}
		if lines > inventory.Threshold && !entry.LegacyOversize && strings.TrimSpace(entry.OversizeJustification) == "" {
			t.Fatalf("%s has %d lines (> %d) and needs oversize_justification or legacy_oversize", path, lines, inventory.Threshold)
		}
		if entry.Class == "legacy" && !entry.LegacyOversize && lines > inventory.Threshold {
			t.Fatalf("legacy script %s must be marked legacy_oversize", path)
		}
	}
	for path := range listed {
		found := false
		for _, trackedPath := range tracked {
			if trackedPath == path {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("inventory lists a script that is not in the workspace: %s", path)
		}
	}
}

type governanceInventory struct {
	Threshold int
	Scripts   []governanceScript
}

type governanceScript struct {
	Path                  string
	Class                 string
	LegacyOversize        bool
	OversizeJustification string
}

func parseGovernanceInventory(path string) (governanceInventory, error) {
	file, err := os.Open(path)
	if err != nil {
		return governanceInventory{}, err
	}
	defer file.Close()

	inventory := governanceInventory{}
	scanner := bufio.NewScanner(file)
	section := ""
	current := governanceScript{}
	flush := func() {
		if current.Path != "" {
			inventory.Scripts = append(inventory.Scripts, current)
			current = governanceScript{}
		}
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "review_threshold_lines:"):
			inventory.Threshold, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "review_threshold_lines:")))
			section = ""
		case line == "scripts:":
			flush()
			section = "scripts"
		case section == "scripts" && strings.HasPrefix(line, "- path:"):
			flush()
			current.Path = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "- path:")), `"'`)
		case section == "scripts" && strings.HasPrefix(line, "class:"):
			current.Class = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "class:")), `"'`)
		case section == "scripts" && strings.HasPrefix(line, "legacy_oversize:"):
			current.LegacyOversize = strings.TrimSpace(strings.TrimPrefix(line, "legacy_oversize:")) == "true"
		case section == "scripts" && strings.HasPrefix(line, "oversize_justification:"):
			current.OversizeJustification = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "oversize_justification:")), `"'`)
		}
	}
	flush()
	return inventory, scanner.Err()
}

func gitWorkspaceScripts(root string) ([]string, error) {
	// New scripts must be checked before git add/commit too. NUL-delimited names
	// preserve Unicode paths without depending on Git's display quoting policy.
	cmd := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "*.sh", "*.ps1", "*.py", "*.iss")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(output), "\x00") {
		path := filepath.ToSlash(strings.TrimSpace(line))
		if path == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if strings.HasPrefix(path, "scripts/") || strings.HasPrefix(path, "packaging/") || path == "docker-entrypoint.sh" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func TestScriptInventoryIncludesNewUntrackedScripts(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %q: %s: %v", args, output, err)
		}
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		".gitignore": "scripts/ignored.ps1\n", "scripts/tracked.ps1": "# tracked\n",
		"scripts/草稿.ps1": "# new\n", "scripts/ignored.ps1": "# generated\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "scripts/tracked.ps1")
	check := func() {
		t.Helper()
		paths, err := gitWorkspaceScripts(root)
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(paths)
		if strings.Join(paths, "\n") != "scripts/tracked.ps1\nscripts/草稿.ps1" {
			t.Fatalf("inventory skipped untracked script or included generated data: %q", paths)
		}
	}
	check()
	git("add", "scripts/草稿.ps1")
	check()
}

func countLines(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}
	return strings.Count(string(data), "\n") + 1, nil
}
