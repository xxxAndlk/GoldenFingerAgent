package wechatdb

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type DBFile struct {
	Rel  string
	Path string
	Size int64
}

var windowsPathRE = regexp.MustCompile(`(?i)[a-z]:[\\/][^\r\n\x00"']+`)

func DetectDBDir() string {
	if value := strings.TrimSpace(os.Getenv("GFA_WECHAT_DB_DIR")); value != "" {
		if root := locateAccountRoot(value); root != "" {
			return root
		}
	}
	var configDirs []string
	for _, env := range []string{"APPDATA", "LOCALAPPDATA"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		configDirs = append(configDirs,
			filepath.Join(base, "Tencent", "xwechat", "config"),
			filepath.Join(base, "Tencent", "xwechat"),
			filepath.Join(base, "Tencent", "WeChat"),
		)
	}
	for _, dir := range configDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			if len(raw) > 8192 {
				raw = raw[:8192]
			}
			for _, match := range windowsPathRE.FindAllString(string(raw), -1) {
				candidate := strings.TrimSpace(strings.Trim(match, `"'`))
				if root := locateAccountRoot(candidate); root != "" {
					return root
				}
			}
		}
	}
	home, _ := os.UserHomeDir()
	for _, candidate := range []string{
		filepath.Join(home, "Documents"),
		filepath.Join(home, "WeChat Files"),
		home,
	} {
		if root := locateAccountRoot(candidate); root != "" {
			return root
		}
	}
	return ""
}

func locateAccountRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	candidates := []string{
		root,
		filepath.Join(root, "xwechat_files"),
		filepath.Join(root, "WeChat Files"),
		filepath.Join(root, "xwechat_files_data"),
	}
	if strings.EqualFold(filepath.Base(root), "xwechat_files") {
		candidates = append(candidates, filepath.Dir(root))
	}
	for _, candidate := range candidates {
		entries, err := os.ReadDir(candidate)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if info, err := os.Stat(filepath.Join(candidate, entry.Name(), "db_storage")); err == nil && info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

func ListAccounts(dbDir string) []string {
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if info, err := os.Stat(filepath.Join(dbDir, entry.Name(), "db_storage")); err == nil && info.IsDir() {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out
}

func CollectDBFiles(accountDir string) []DBFile {
	base := filepath.Join(accountDir, "db_storage")
	var files []DBFile
	_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".db") || strings.HasSuffix(strings.ToLower(name), "-wal") || strings.HasSuffix(strings.ToLower(name), "-shm") {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return nil
		}
		if strings.HasPrefix(strings.ToLower(rel), "migrate") {
			return nil
		}
		files = append(files, DBFile{Rel: filepath.ToSlash(rel), Path: path, Size: info.Size()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files
}
