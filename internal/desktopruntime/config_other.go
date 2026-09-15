//go:build !windows

package desktopruntime

import (
	"context"
	"errors"
)

func platformUpdateConfig(context.Context, ConfigUpdateRequest) error {
	return errors.New("当前平台的桌面配置由原生配置文件控制器管理")
}

func platformReadRuntimeOptions(string) (RuntimeOptionsView, error) {
	return RuntimeOptionsView{}, errors.New("当前平台的桌面配置由原生配置文件控制器管理")
}

func platformSaveRuntimeOptions(context.Context, string, RuntimeOptions) error {
	return errors.New("当前平台的桌面配置由原生配置文件控制器管理")
}
