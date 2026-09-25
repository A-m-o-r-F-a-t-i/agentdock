package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWorkbenchSeparatesPerformanceWithoutDroppingPackages(t *testing.T) {
	var workflow identityWorkflow
	if err := yaml.Unmarshal([]byte(readWorkflow(t, "workbench-release.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	var validation string
	for _, step := range workflow.Jobs["unix"].Steps {
		if strings.Contains(step.Run, "all_packages=") {
			validation = step.Run
		}
	}
	if validation == "" {
		t.Fatal("missing isolated native validation")
	}
	for _, contract := range []string{
		`all_packages="$(go list ./...)"`,
		`activity_package="github.com/uvwt/agentdock/internal/activity"`,
		`if [[ "$package" == "$activity_package" ]]`,
		`remaining+=("$package")`,
		`test "$found" -eq 1`,
		`go test -p 1 ./internal/activity -count=1 -timeout=8m`,
		`go test -p 1 ./internal/activity -run '^TestExecutionProjectionScale100k$' -count=3 -v -timeout=8m`,
		`go test -p 2 "${remaining[@]}" -count=1 -timeout=8m`,
		`go vet ./...`,
	} {
		if !strings.Contains(validation, contract) {
			t.Fatalf("native validation lost contract: %s", contract)
		}
	}
	if strings.Contains(validation, "-short") || strings.Contains(validation, "|| true") {
		t.Fatal("bounded tests were bypassed")
	}
	activityAt := strings.Index(validation, "go test -p 1 ./internal/activity -count=1")
	otherAt := strings.Index(validation, `go test -p 2 "${remaining[@]}"`)
	if activityAt > otherAt {
		t.Fatal("timed package must run independently before unrelated test compilers")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "activity", "scale_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "coldLimit := 2 * time.Second") || !strings.Contains(string(data), "if cold > coldLimit") {
		t.Fatal("normal cold projection timing contract was weakened")
	}
}
