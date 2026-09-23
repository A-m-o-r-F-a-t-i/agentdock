package plugin

func pluginError(code, stage string, err error) error {
	return newError(code, stage+": "+err.Error(), map[string]any{"stage": stage}, err)
}
