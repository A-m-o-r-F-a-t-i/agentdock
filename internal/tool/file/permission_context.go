package file

import "context"

type restrictedFileToolsKey struct{}

// WithRestrictedFileTools is set only by the host dispatcher after evaluating
// the fixed profile. It is not a public tool argument. Restricted reads must not
// spawn PATH-selected helpers or follow Git configuration outside their target.
func WithRestrictedFileTools(ctx context.Context) context.Context {
	return context.WithValue(ctx, restrictedFileToolsKey{}, true)
}

func restrictedFileTools(ctx context.Context) bool {
	enabled, _ := ctx.Value(restrictedFileToolsKey{}).(bool)
	return enabled
}

func loadContextIgnoreMatcher(ctx context.Context, root string) *ignoreMatcher {
	if restrictedFileTools(ctx) {
		// Built-in hidden/directory filters and explicit request globs still
		// apply. Repository ignore files can point through symlinks or worktree
		// gitdir/commondir references, so they are not read in restricted mode.
		return &ignoreMatcher{disabled: true}
	}
	return loadIgnoreMatcher(root)
}
