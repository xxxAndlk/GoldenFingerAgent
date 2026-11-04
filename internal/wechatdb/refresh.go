package wechatdb

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"
)

// fileState 描述源库上一次同步时的状态。首页指纹用来识别 checkpoint 后
// 内容变化但文件大小不变的库。
type fileState struct {
	size    int64
	modTime time.Time
	page1   string
}

func probeFile(path string) (fileState, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileState{}, err
	}
	handle, err := os.Open(path)
	if err != nil {
		return fileState{}, err
	}
	defer handle.Close()
	head := make([]byte, PageSize)
	n, err := handle.ReadAt(head, 0)
	if err != nil && n == 0 {
		return fileState{}, err
	}
	sum := sha256.Sum256(head[:n])
	return fileState{
		size: info.Size(), modTime: info.ModTime(),
		page1: hex.EncodeToString(sum[:]),
	}, nil
}

// changed 判断源库是否需要重新解密。
func (s fileState) changed(next fileState) bool {
	return s.size != next.size || s.page1 != next.page1 || !s.modTime.Equal(next.modTime)
}

// refreshLocked 把源库的最新内容同步到解密副本。微信的新写入先进 WAL，
// 所以每轮都要重放已提交的 WAL 帧；源库本体变化（checkpoint）时重新解密整库。
// 单个库失败不影响其它库，失败时保留上一轮副本，下一轮重试。
func (r *Reader) refreshLocked() {
	for _, file := range CollectDBFiles(r.AccountDir) {
		if _, known := r.states[file.Rel]; known {
			continue
		}
		key, ok := findKeyForFile(r.candidates, file)
		if !ok {
			r.Unkeyed = append(r.Unkeyed, file.Rel)
			r.states[file.Rel] = fileState{}
			continue
		}
		r.Keys[file.Rel] = key
		r.Files = append(r.Files, file)
		r.states[file.Rel] = fileState{}
	}
	for _, file := range r.Files {
		key, ok := r.Keys[file.Rel]
		if !ok {
			continue
		}
		dst := filepath.Join(r.WorkDir, filepath.FromSlash(file.Rel))
		state, err := probeFile(file.Path)
		if err != nil {
			continue
		}
		previous := r.states[file.Rel]
		if previous.changed(state) {
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				continue
			}
			// 微信正在写库时可能读到不完整的页，解密失败就保留旧副本。
			if err := decryptFile(file.Path, dst, key); err != nil {
				continue
			}
			r.states[file.Rel] = state
		}
		if walPath := file.Path + "-wal"; fileExists(walPath) {
			_ = mergeWAL(dst, walPath, key)
		}
	}
	r.names = nil
	r.shards = nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
