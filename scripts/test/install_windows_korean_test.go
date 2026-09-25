package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestWindowsKoreanInstallerResourceContract(t *testing.T) {
	root := filepath.Join("..", "..", "packaging", "windows")
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	source := read(filepath.Join("includes", "messages.iss"))
	locales := map[string]map[string]string{"english": {}, "chinesesimplified": {}, "korean": {}}
	for _, match := range regexp.MustCompile(`(?m)^(english|chinesesimplified|korean)\.([^=\r\n]+)=([^\r\n]*)`).FindAllStringSubmatch(source, -1) {
		dictionary := locales[match[1]]
		if _, exists := dictionary[match[2]]; exists {
			t.Fatalf("duplicate %s/%s", match[1], match[2])
		}
		dictionary[match[2]] = match[3]
	}
	if len(locales["english"]) < 60 {
		t.Fatal("installer inventory unexpectedly shrank")
	}
	tokens := regexp.MustCompile(`%[0-9nt]+`)
	for locale, dictionary := range locales {
		if len(dictionary) != len(locales["english"]) {
			t.Fatalf("%s: key count mismatch", locale)
		}
		for key, english := range locales["english"] {
			value, exists := dictionary[key]
			if !exists || strings.TrimSpace(value) == "" {
				t.Errorf("%s/%s missing or empty", locale, key)
			}
			before, after := tokens.FindAllString(english, -1), tokens.FindAllString(value, -1)
			sort.Strings(before)
			sort.Strings(after)
			if !reflect.DeepEqual(before, after) {
				t.Errorf("%s/%s placeholder mismatch", locale, key)
			}
			if locale == "korean" && key != "BearerToken" && !regexp.MustCompile(`[가-힣]`).MatchString(value) {
				t.Errorf("Korean owned message has no translation: %s", key)
			}
		}
	}
	main := read("AgentDock.iss")
	code := read(filepath.Join("includes", "code.iss"))
	native := read(filepath.Join("includes", "native-launch.iss"))
	for _, required := range []string{`Name: "korean"; MessagesFile: "languages\Korean.isl"`, `LanguageDetectionMethod=uilanguage`, `PrivilegesRequired=lowest`, `languages\LICENSE-InnoSetup.txt`} {
		if !strings.Contains(main, required) {
			t.Errorf("missing installer contract %s", required)
		}
	}
	keyPattern := regexp.MustCompile(`(?:GetLocalizedMessage|CustomMessage)\('([^']+)'`)
	for _, match := range keyPattern.FindAllStringSubmatch(main+code+native, -1) {
		for locale, dictionary := range locales {
			if _, exists := dictionary[match[1]]; !exists {
				t.Errorf("called key %s missing in %s", match[1], locale)
			}
		}
	}
	for _, required := range []string{"OriginalInstallerDiagnostic", "LocalizedInstallFailure(ErrorCode, ErrorMessage, ExitCode)", "ErrorCode = 'rollback-failed'", "ErrorCode = 'elevated-task-rollback-failed'", "ErrorCode = 'stale-rollback-recovery-unsafe'", "ErrorCode = 'stale-rollback-recovery-failed'", "ErrorCode = 'install-validation-failed'", "ErrorCode = 'task-scheduler-unavailable'"} {
		if !strings.Contains(code, required) {
			t.Errorf("missing coded installer guidance: %s", required)
		}
	}
	for _, forbidden := range []string{"Unsupported control character in native command", "AgentDock native uninstall launcher could not be copied."} {
		if strings.Contains(native, forbidden) {
			t.Errorf("unlocalized native error: %s", forbidden)
		}
	}
	if !strings.Contains(locales["korean"]["PurgeStateQuestion"], "삭제") || !strings.Contains(locales["korean"]["PurgeStateQuestion"], "아니요") || !strings.Contains(locales["korean"]["InstallerRollbackFailed"], "복구도 완료되지 않았습니다") {
		t.Fatal("destructive/recovery guidance lost its safety meaning")
	}
}

func TestWindowsOfficialKoreanInstallerTranslationPins(t *testing.T) {
	root := filepath.Join("..", "..", "packaging", "windows", "languages")
	data, err := os.ReadFile(filepath.Join(root, "korean-source.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		SchemaVersion int               `json:"schema_version"`
		Modified      bool              `json:"modified"`
		Files         map[string]string `json:"files"`
		Compiler      string            `json:"compiler_package"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.SchemaVersion != 1 || metadata.Modified || metadata.Compiler != "6.7.3" || len(metadata.Files) != 2 {
		t.Fatal("unexpected translation provenance")
	}
	for _, name := range []string{"Korean.isl", "LICENSE-InnoSetup.txt"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != metadata.Files[name] {
			t.Errorf("pinned official file changed: %s", name)
		}
		if name == "Korean.isl" {
			for _, required := range []string{"VenusGirl", "LanguageID=$0412", "[Messages]", "[LangOptions]"} {
				if !strings.Contains(string(data), required) {
					t.Errorf("official language metadata missing: %s", required)
				}
			}
		}
		if name == "LICENSE-InnoSetup.txt" {
			for _, required := range []string{"Inno Setup License", "1997-2026 Jordan Russell", "2000-2026 Martijn Laan", "All redistributions of source code files", "https://jrsoftware.org/"} {
				if !strings.Contains(string(data), required) {
					t.Errorf("license notice missing: %s", required)
				}
			}
		}
	}
}
