//go:build windows

package agenthub

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// persistentEnvironment 读取用户和系统环境，避免服务继承的 PATH 不完整。
func persistentEnvironment() map[string]string {
	values := make(map[string]string)
	for _, item := range os.Environ() {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			values[strings.ToUpper(name)] = value
		}
	}
	scopes := []struct {
		root registry.Key
		path string
	}{
		{registry.CURRENT_USER, `Environment`},
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
	}
	for _, scope := range scopes {
		key, err := registry.OpenKey(scope.root, scope.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		for _, name := range []string{"Path", "PNPM_HOME", "NVM_HOME", "NVM_SYMLINK", "NPM_CONFIG_PREFIX"} {
			if value, _, err := key.GetStringValue(name); err == nil {
				keyName := strings.ToUpper(name)
				if keyName == "PATH" && values[keyName] != "" {
					values[keyName] += ";" + value
				} else {
					values[keyName] = value
				}
			}
		}
		_ = key.Close()
	}
	return values
}

func persistentPathDirs() []string {
	environment := persistentEnvironment()
	var directories []string
	seen := make(map[string]bool)
	for _, name := range []string{"PATH", "NPM_CONFIG_PREFIX"} {
		raw := os.Expand(environment[name], func(key string) string {
			return environment[strings.ToUpper(key)]
		})
		for _, directory := range strings.Split(raw, ";") {
			directory = strings.Trim(strings.TrimSpace(directory), `"`)
			if directory == "" || !filepath.IsAbs(directory) {
				continue
			}
			normalized := strings.ToLower(filepath.Clean(directory))
			if !seen[normalized] {
				seen[normalized] = true
				directories = append(directories, directory)
			}
		}
	}
	return directories
}
