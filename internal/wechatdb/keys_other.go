//go:build !windows

package wechatdb

import "errors"

func ExtractKeyCandidates() ([]Candidate, error) {
	return nil, errors.New("微信数据库连接器仅支持 Windows")
}

func FindKeys(candidates []Candidate, files []DBFile) map[string]KeyBlob {
	return nil
}

func findKeyForFile(candidates []Candidate, file DBFile) (KeyBlob, bool) {
	return KeyBlob{}, false
}
