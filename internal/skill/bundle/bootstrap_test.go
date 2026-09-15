package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	skills "github.com/uvwt/agentdock/internal/skill"
	skillstate "github.com/uvwt/agentdock/internal/skill/state"
)

func TestBootstrapInstallsActivatesAndRecordsBundledSkills(t *testing.T) {
	state, manager := newTestManager(t)
	bundle := t.TempDir()
	manifest := Manifest{Skills: []ManifestSkill{
		writeBundledSkill(t, bundle, "skill-authoring", "1.0.0"),
		writeBundledSkill(t, bundle, "skill-installation", "1.1.0"),
	}}
	writeManifest(t, bundle, manifest)

	result, err := Bootstrap(context.Background(), state, manager, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 2 {
		t.Fatalf("Bootstrap() skills = %#v", result.Skills)
	}
	for _, entry := range manifest.Skills {
		active, err := state.ActiveVersion(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		if active != entry.Version {
			t.Fatalf("%s active version = %q, want %q", entry.Name, active, entry.Version)
		}
	}
	bundled, err := state.BundledSkills()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bundled, []string{"skill-authoring", "skill-installation"}) {
		t.Fatalf("BundledSkills() = %#v", bundled)
	}

	// 重复执行同一 Bundle 应保持幂等，不创建重复状态或报同版本冲突。
	if _, err := Bootstrap(context.Background(), state, manager, bundle); err != nil {
		t.Fatalf("second Bootstrap() failed: %v", err)
	}
}

func TestBootstrapReplacesExistingBundledSkillWithSameVersion(t *testing.T) {
	state, manager := newTestManager(t)
	localRoot := t.TempDir()
	local := writeBundledSkillWithBody(t, localRoot, "skill-authoring", "1.0.0", "Local modification")
	if _, err := manager.Install(context.Background(), skills.InstallRequest{
		Source:   filepath.Join(localRoot, local.Name),
		Activate: true,
	}); err != nil {
		t.Fatal(err)
	}

	bundle := t.TempDir()
	official := writeBundledSkillWithBody(t, bundle, local.Name, local.Version, "Official content")
	writeManifest(t, bundle, Manifest{Skills: []ManifestSkill{official}})
	if _, err := Bootstrap(context.Background(), state, manager, bundle); err != nil {
		t.Fatal(err)
	}

	installedPath, err := state.Resolve(local.Name, local.Version)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(installedPath, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Official content") || strings.Contains(string(data), "Local modification") {
		t.Fatalf("bundled Skill was not replaced with official content: %s", data)
	}
}

func TestBootstrapRestoresReplacedBundledSkillWhenActivationFails(t *testing.T) {
	state, manager := newTestManager(t)
	localRoot := t.TempDir()
	local := writeBundledSkillWithBody(t, localRoot, "first-skill", "1.0.0", "Local modification")
	if _, err := manager.Install(context.Background(), skills.InstallRequest{
		Source:   filepath.Join(localRoot, local.Name),
		Activate: true,
	}); err != nil {
		t.Fatal(err)
	}

	bundle := t.TempDir()
	first := writeBundledSkillWithBody(t, bundle, local.Name, local.Version, "Official replacement")
	second := writeBundledSkill(t, bundle, "second-skill", "1.0.0")
	writeManifest(t, bundle, Manifest{Skills: []ManifestSkill{first, second}})

	if err := bootstrapWithBlockedSecondActivation(t, state, manager, bundle, first, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Bootstrap() error = %v, want context canceled", err)
	}

	installedPath, err := state.Resolve(local.Name, local.Version)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(installedPath, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Local modification") || strings.Contains(string(data), "Official replacement") {
		t.Fatalf("failed Bootstrap did not restore original Skill content: %s", data)
	}
}

func TestBootstrapValidatesWholeBundleBeforeInstalling(t *testing.T) {
	state, manager := newTestManager(t)
	bundle := t.TempDir()
	first := writeBundledSkill(t, bundle, "first-skill", "1.0.0")
	second := writeBundledSkill(t, bundle, "second-skill", "1.0.0")
	second.Digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	writeManifest(t, bundle, Manifest{Skills: []ManifestSkill{first, second}})

	if _, err := Bootstrap(context.Background(), state, manager, bundle); err == nil {
		t.Fatal("Bootstrap() succeeded with invalid digest")
	}
	versions, err := state.ListVersions(first.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Fatalf("first skill was installed before full validation: %#v", versions)
	}
}

func TestBootstrapRestoresStateWhenActivationFails(t *testing.T) {
	state, manager := newTestManager(t)
	bundle := t.TempDir()
	first := writeBundledSkill(t, bundle, "first-skill", "1.0.0")
	second := writeBundledSkill(t, bundle, "second-skill", "1.0.0")
	writeManifest(t, bundle, Manifest{Skills: []ManifestSkill{first, second}})

	if err := bootstrapWithBlockedSecondActivation(t, state, manager, bundle, first, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Bootstrap() error = %v, want context canceled", err)
	}
	for _, name := range []string{first.Name, second.Name} {
		active, err := state.ActiveVersion(name)
		if err != nil {
			t.Fatal(err)
		}
		if active != "" {
			t.Fatalf("%s active version after rollback = %q", name, active)
		}
		versions, err := state.ListVersions(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != 0 {
			t.Fatalf("%s versions after rollback = %#v", name, versions)
		}
	}
	bundled, err := state.BundledSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundled) != 0 {
		t.Fatalf("BundledSkills() after rollback = %#v", bundled)
	}
	if _, err := os.Stat(filepath.Join(state.Root(), ".system", ".agentdock-system-skills.marker")); !os.IsNotExist(err) {
		t.Fatalf("failed bootstrap created bundled list: %v", err)
	}
}

func bootstrapWithBlockedSecondActivation(
	t *testing.T,
	state *skillstate.Store,
	manager *skills.Manager,
	bundle string,
	first ManifestSkill,
	second ManifestSkill,
) error {
	t.Helper()

	initialSelection, err := state.Snapshot(first.Name)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(state.Root(), ".locks", second.Name+".lock")
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(lockPath) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, bootstrapErr := Bootstrap(ctx, state, manager, bundle)
		result <- bootstrapErr
	}()

	// 先等第一个 Skill 真正完成激活，再取消 context。第二个 Skill 的锁始终保留，
	// 因此失败点由事务状态决定，不再依赖 race 模式下不稳定的毫秒时间窗。
	waitForSelection(t, state, first.Name, func(selection skillstate.Selection) bool {
		return selection.ActiveVersion == first.Version && !selection.UpdatedAt.Equal(initialSelection.UpdatedAt)
	}, "first bundled Skill activation")
	cancel()

	// 回滚会先恢复已激活 Skill 的 selection，再清理已安装版本。等 selection 恢复后
	// 才释放第二个 Skill 的锁，既确认走到了 activation 回滚，也允许事务正常收尾。
	waitForSelection(t, state, first.Name, func(selection skillstate.Selection) bool {
		return reflect.DeepEqual(selection, initialSelection)
	}, "first bundled Skill rollback")
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("release blocked activation lock: %v", err)
	}

	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Bootstrap() did not finish after blocked activation was released")
		return nil
	}
}

func waitForSelection(
	t *testing.T,
	state *skillstate.Store,
	skill string,
	ready func(skillstate.Selection) bool,
	description string,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastReadErr error
	for time.Now().Before(deadline) {
		selection, err := state.Snapshot(skill)
		if err != nil {
			// Windows 在原子替换状态文件的极短窗口内可能返回 sharing violation。
			// 这里本来就在等待并发事务推进，因此把读取失败视为“尚未就绪”，
			// 但保留最后一次错误，超时后仍能给出真实失败证据。
			lastReadErr = err
			time.Sleep(10 * time.Millisecond)
			continue
		}
		lastReadErr = nil
		if ready(selection) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lastReadErr != nil {
		t.Fatalf("timed out waiting for %s; last state read error: %v", description, lastReadErr)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func newTestManager(t *testing.T) (*skillstate.Store, *skills.Manager) {
	t.Helper()
	state, err := skillstate.New(filepath.Join(t.TempDir(), "skills"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := skills.New(state)
	if err != nil {
		t.Fatal(err)
	}
	return state, manager
}

func writeBundledSkill(t *testing.T, bundle, name, version string) ManifestSkill {
	t.Helper()
	return writeBundledSkillWithBody(t, bundle, name, version, "Test")
}

func writeBundledSkillWithBody(t *testing.T, bundle, name, version, body string) ManifestSkill {
	t.Helper()
	packageDir := filepath.Join(bundle, name)
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: " + name + "\ndescription: Test bundled Skill.\nversion: " + version + "\n---\n\n# " + body + "\n"
	if err := os.WriteFile(filepath.Join(packageDir, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := skills.DigestDirectory(packageDir)
	if err != nil {
		t.Fatal(err)
	}
	return ManifestSkill{Name: name, Version: version, Path: name, Digest: digest}
}

func writeManifest(t *testing.T, bundle string, manifest Manifest) {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, ManifestFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
