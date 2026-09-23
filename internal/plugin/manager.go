package plugin

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
)

func (s *Store) TempPath(prefix string) (string, error) { return os.MkdirTemp(s.tempRoot, prefix+"-") }

// PrepareSource imports upstream adapters without changing plugins/<name>,
// Heavy, native member names, or the existing host-state transaction.
func (s *Store) PrepareSource(ctx context.Context, request SourceRequest) (PreparedSource, error) {
	if err := ctx.Err(); err != nil {
		return PreparedSource{}, err
	}
	staged, err := s.stagePluginSource(ctx, request)
	if err != nil {
		return PreparedSource{}, err
	}
	snapshot, err := s.TempPath("source-snapshot")
	if err != nil {
		staged.Cleanup()
		return PreparedSource{}, err
	}
	cleanupSnapshot := func() { _ = os.RemoveAll(snapshot); staged.Cleanup() }
	if err := copyPackageTree(staged.Root, snapshot); err != nil {
		cleanupSnapshot()
		return PreparedSource{}, err
	}
	staged.Root = snapshot
	normalized, pkg, cleanupAdapter, err := s.adaptStagedSource(staged, request)
	if err != nil {
		cleanupSnapshot()
		return PreparedSource{}, err
	}
	cleanup := func() { cleanupAdapter(); cleanupSnapshot() }
	if err := ctx.Err(); err != nil {
		cleanup()
		return PreparedSource{}, err
	}
	definition, err := s.Validate(normalized)
	if err != nil {
		cleanup()
		return PreparedSource{}, err
	}
	source := staged.Source
	source.Adapter = pkg.Compatibility.Adapter
	review := Review{Valid: len(pkg.Unsupported) == 0, Name: pkg.Manifest.Name, Version: pkg.Manifest.Version,
		Description: pkg.Manifest.Description, PackageDigest: pkg.PackageDigest, Source: source,
		Skills: pkg.Components.Skills, MCP: reviewMCPComponents(pkg.Components.MCP), Unsupported: pkg.Unsupported,
		Warnings: pkg.Warnings, Executables: pkg.Executables, Issues: []string{}, Compatibility: pkg.Compatibility}
	if !review.Valid {
		review.Issues = append(review.Issues, "Plugin contains unsupported components: "+strings.Join(pkg.Unsupported, ", "))
	}
	return PreparedSource{Root: normalized, Definition: definition, Source: source, Compatibility: pkg.Compatibility, Cleanup: cleanup, Review: review}, nil
}

func (s *Store) ValidateSource(ctx context.Context, request SourceRequest) Review {
	prepared, err := s.PrepareSource(ctx, request)
	if err != nil {
		return Review{Valid: false, Issues: []string{err.Error()}, Skills: []SkillComponent{}, MCP: []MCPReview{}, Unsupported: []string{}, Warnings: []string{}, Executables: []string{}, Compatibility: Compatibility{Supported: []string{}, Unsupported: []string{}, Warnings: []string{}}}
	}
	defer prepared.Cleanup()
	return prepared.Review
}

func (s *Store) InstallPrepared(ctx context.Context, candidate PreparedSource, replace, confirmSourceChange bool) (Definition, error) {
	if err := ctx.Err(); err != nil {
		return Definition{}, err
	}
	if !candidate.Review.Valid {
		return Definition{}, pluginError("PLUGIN_UNSUPPORTED_COMPONENT", "install.validate", errors.New(strings.Join(candidate.Review.Issues, ", ")))
	}
	return s.installPrepared(candidate.Root, replace, &candidate, confirmSourceChange)
}

func (s *Store) InstallSource(ctx context.Context, request SourceRequest, replace, confirmSourceChange bool) (Definition, error) {
	candidate, err := s.PrepareSource(ctx, request)
	if err != nil {
		return Definition{}, err
	}
	defer candidate.Cleanup()
	return s.InstallPrepared(ctx, candidate, replace, confirmSourceChange)
}

func copyPluginTree(source, destination string) error { return copyPackageTree(source, destination) }

func samePluginSourceBinding(left, right Source) bool {
	leftAdapter := strings.TrimSpace(left.Adapter)
	rightAdapter := strings.TrimSpace(right.Adapter)
	// P2 persisted only portable Plugins and had no adapter field. Treat an
	// empty historical adapter as portable so P2 -> P3 does not create a
	// false source-rebind prompt.
	if leftAdapter == "" {
		leftAdapter = "portable"
	}
	if rightAdapter == "" {
		rightAdapter = "portable"
	}
	return left.Type == right.Type &&
		left.Ref == right.Ref &&
		left.Selector == right.Selector &&
		left.Subdir == right.Subdir &&
		leftAdapter == rightAdapter &&
		left.Catalog == right.Catalog &&
		left.CatalogItem == right.CatalogItem &&
		left.ResolvedType == right.ResolvedType &&
		left.ResolvedRef == right.ResolvedRef &&
		left.ResolvedSubdir == right.ResolvedSubdir
}

func reviewMCPComponents(components []MCPComponent) []MCPReview {
	items := make([]MCPReview, 0, len(components))
	for _, component := range components {
		envNames := make([]string, 0, len(component.Environment)+len(component.EnvBindings))
		for name := range component.Environment {
			envNames = append(envNames, name)
		}
		for _, name := range component.EnvBindings {
			envNames = append(envNames, name)
		}
		envNames = append(envNames, component.RequiredEnv...)
		sort.Strings(envNames)
		envNames = uniqueStrings(envNames)

		headerNames := make([]string, 0, len(component.Headers)+len(component.HeaderEnv))
		for name := range component.Headers {
			headerNames = append(headerNames, name)
		}
		for name := range component.HeaderEnv {
			headerNames = append(headerNames, name)
		}
		sort.Strings(headerNames)
		headerNames = uniqueStrings(headerNames)

		items = append(items, MCPReview{
			Name: component.Name, Description: component.Description, Transport: component.Transport,
			URL: component.URL, Command: component.Command, CWD: component.CWD,
			EnvironmentNames: envNames, HeaderNames: headerNames,
			RuntimeName: component.RuntimeName, StorageKey: component.StorageKey,
		})
	}
	return items
}
