package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type identityWorkflow struct {
	Jobs map[string]identityJob `yaml:"jobs"`
}
type identityJob struct {
	Needs   yaml.Node         `yaml:"needs"`
	Outputs map[string]string `yaml:"outputs"`
	Steps   []struct {
		ID   string            `yaml:"id"`
		Uses string            `yaml:"uses"`
		Run  string            `yaml:"run"`
		With map[string]any    `yaml:"with"`
		Env  map[string]string `yaml:"env"`
	} `yaml:"steps"`
}

func identityDependencies(job identityJob) []string {
	if job.Needs.Kind == yaml.ScalarNode {
		return []string{job.Needs.Value}
	}
	var dependencies []string
	for _, node := range job.Needs.Content {
		dependencies = append(dependencies, node.Value)
	}
	return dependencies
}

func TestWorkflowConsumersUseSingleResolvedCommit(t *testing.T) {
	for _, test := range []struct {
		file, resolver string
		consumers      []string
	}{
		{"ci.yml", "source", []string{"go", "browser-integration", "container"}},
		{"windows-package.yml", "resolve-source", []string{"linux-contracts", "build", "publish"}},
	} {
		var workflow identityWorkflow
		if err := yaml.Unmarshal([]byte(readWorkflow(t, test.file)), &workflow); err != nil {
			t.Fatal(err)
		}
		resolver, ok := workflow.Jobs[test.resolver]
		if !ok || resolver.Outputs["commit"] != "${{ steps.identity.outputs.commit }}" {
			t.Fatalf("%s has no authoritative resolver", test.file)
		}
		resolved := false
		for _, step := range resolver.Steps {
			if step.ID == "identity" && strings.Contains(step.Run, "git rev-parse --verify 'HEAD^{commit}'") && strings.Contains(step.Run, "[0-9a-f]{40}") {
				resolved = true
			}
		}
		if !resolved {
			t.Fatal("resolver does not validate immutable commit identity")
		}
		for _, name := range test.consumers {
			job, ok := workflow.Jobs[name]
			if !ok {
				t.Fatalf("missing job %s", name)
			}
			dependency := false
			for _, needed := range identityDependencies(job) {
				dependency = dependency || needed == test.resolver
			}
			if !dependency {
				t.Fatalf("%s does not depend on source resolution", name)
			}
			checkedOut := false
			for _, step := range job.Steps {
				if !strings.HasPrefix(step.Uses, "actions/checkout@") {
					continue
				}
				checkedOut = true
				expected := "${{ needs." + test.resolver + ".outputs.commit }}"
				if name == "publish" {
					expected = "${{ needs.build.outputs.commit }}"
				}
				if step.With["ref"] != expected {
					t.Fatalf("%s/%s re-resolves mutable source: %v", test.file, name, step.With["ref"])
				}
			}
			if !checkedOut {
				t.Fatalf("consumer %s lacks an explicit checkout", name)
			}
		}
	}
}

func TestWorkflowPublicationCrossChecksActualEvidence(t *testing.T) {
	workflow := readWorkflow(t, "windows-package.yml")
	for _, required := range []string{
		"TESTED_COMMIT: ${{ needs.linux-contracts.outputs.commit }}",
		"$commit -ne $env:EXPECTED_COMMIT -or $commit -ne $env:TESTED_COMMIT",
		"test \"$commit\" = \"$EXPECTED_COMMIT\"",
		"linux_tested_commit = '${{ needs.linux-contracts.outputs.commit }}'",
		"$scope.commit -ne $commit", "$scope.resolved_commit -ne $commit", "$scope.linux_tested_commit -ne $commit",
		"'${{ needs.build.outputs.commit }}' -ne $commit", "'${{ needs.linux-contracts.outputs.commit }}' -ne $commit",
		"git ls-remote --tags origin", "Remote release tag moved after source resolution.",
		"manual_desktop_tests = 'not_run'", "'not_run_user_requested'",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("missing evidence contract: %s", required)
		}
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		t.Fatal(err)
	}
	on := document["on"].(map[string]any)
	inputs := on["workflow_dispatch"].(map[string]any)["inputs"].(map[string]any)
	for _, name := range []string{"publish", "installation_tests", "prerelease"} {
		if inputs[name].(map[string]any)["default"] != false {
			t.Fatalf("unsafe default: %s", name)
		}
	}
}

func TestResolvedGitCommitSurvivesMovingRef(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git executable unavailable for isolated ref-race fixture")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	checkout := filepath.Join(root, "checkout")
	if err = os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		config := []string{"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(root, "empty-hooks"), "-c", "user.name=Workflow Fixture", "-c", "user.email=fixture@example.invalid"}
		command := exec.Command(git, append(config, args...)...)
		command.Dir = dir
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run(repository, "init", "-b", "main")
	path := filepath.Join(repository, "payload.txt")
	if err = os.WriteFile(path, []byte("tested-source"), 0600); err != nil {
		t.Fatal(err)
	}
	run(repository, "add", "payload.txt")
	run(repository, "commit", "-m", "tested source")
	resolved := run(repository, "rev-parse", "HEAD")
	run(repository, "branch", "candidate", resolved)
	run(repository, "tag", "v0.0.1", resolved)
	if err = os.WriteFile(path, []byte("later-unverified-source"), 0600); err != nil {
		t.Fatal(err)
	}
	run(repository, "add", "payload.txt")
	run(repository, "commit", "-m", "move input ref")
	later := run(repository, "rev-parse", "HEAD")
	run(repository, "update-ref", "refs/heads/candidate", later)
	run(repository, "tag", "-f", "v0.0.1", later)
	if run(repository, "rev-parse", "candidate") == resolved || run(repository, "rev-list", "-n", "1", "v0.0.1") == resolved {
		t.Fatal("fixture did not move both references")
	}
	run(repository, "worktree", "add", "--detach", checkout, resolved)
	if run(checkout, "rev-parse", "HEAD") != resolved {
		t.Fatal("consumer followed the moved ref")
	}
	content, err := os.ReadFile(filepath.Join(checkout, "payload.txt"))
	if err != nil || string(content) != "tested-source" {
		t.Fatalf("consumer mixed source generations: %v", err)
	}
}
