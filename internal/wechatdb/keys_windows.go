//go:build windows

package wechatdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	configCipherName = []byte("com.Tencent.WCDB.Config.Cipher")
	configXorMask    = []byte{
		0xd2, 0xc7, 0x44, 0x24, 0x58, 0x02, 0x00, 0x00,
		0x00, 0x48, 0x89, 0x44, 0x24, 0x50, 0x48, 0x8b,
		0x45, 0x00, 0x48, 0x84, 0x4c, 0x24, 0x48, 0x48,
		0x89, 0x44, 0x25, 0x40, 0x48, 0x58, 0x4c, 0x24,
	}
	hexLiteralRE = regexp.MustCompile(`(?i)[xX]'([0-9a-f]{64,192})'`)
)

type Candidate struct {
	Key  []byte
	Salt []byte
}

func weixinPIDs() []uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err == nil {
		defer windows.CloseHandle(snapshot)
		var entry windows.ProcessEntry32
		entry.Size = uint32(unsafe.Sizeof(entry))
		if err := windows.Process32First(snapshot, &entry); err == nil {
			var pids []uint32
			for {
				name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
				if name == "weixin.exe" || name == "wechat.exe" {
					pids = append(pids, entry.ProcessID)
				}
				if err := windows.Process32Next(snapshot, &entry); err != nil {
					break
				}
			}
			if len(pids) > 0 {
				return pids
			}
		}
	}
	return weixinPIDsTasklist()
}

func weixinPIDsTasklist() []uint32 {
	output, err := exec.Command("tasklist", "/FI", "IMAGENAME eq Weixin.exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil
	}
	var pids []uint32
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(strings.Trim(line, `"`), `","`)
		if len(parts) < 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "Weixin.exe") {
			continue
		}
		if pid, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32); err == nil {
			pids = append(pids, uint32(pid))
		}
	}
	return pids
}

func readRemote(h windows.Handle, address uintptr, size int) ([]byte, bool) {
	if size <= 0 || size > 256<<20 {
		return nil, false
	}
	buf := make([]byte, size)
	var read uintptr
	if err := windows.ReadProcessMemory(h, address, &buf[0], uintptr(size), &read); err != nil || read == 0 {
		return nil, false
	}
	return buf[:read], true
}

func findBytes(h windows.Handle, needle []byte) []uintptr {
	var hits []uintptr
	var address uintptr
	regions, readableCount, readOK := 0, 0, 0
	defer func() {
		debugf("scan regions=%d readable=%d readOK=%d hits=%d", regions, readableCount, readOK, len(hits))
	}()
	for {
		var mbi windows.MemoryBasicInformation
		if err := windows.VirtualQueryEx(h, address, &mbi, unsafe.Sizeof(mbi)); err != nil {
			break
		}
		regions++
		if mbi.RegionSize == 0 {
			break
		}
		readable := mbi.State == windows.MEM_COMMIT &&
			mbi.Protect&0x100 == 0 &&
			mbi.Protect&0xE6 != 0 &&
			mbi.RegionSize > 0 && mbi.RegionSize < 0x10000000
		if readable {
			readableCount++
			if buf, ok := readRemote(h, mbi.BaseAddress, int(mbi.RegionSize)); ok {
				readOK++
				for offset := 0; ; {
					index := bytes.Index(buf[offset:], needle)
					if index < 0 {
						break
					}
					hits = append(hits, mbi.BaseAddress+uintptr(offset+index))
					offset += index + 1
				}
			}
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if next <= address {
			break
		}
		address = next
	}
	return hits
}

func u64(value []byte, offset int) uint64 {
	if len(value) < offset+8 {
		return 0
	}
	return binary.LittleEndian.Uint64(value[offset : offset+8])
}

func probableKey(key []byte) bool {
	if len(key) != 32 {
		return false
	}
	seen := map[byte]bool{}
	for _, b := range key {
		seen[b] = true
	}
	if len(seen) < 15 {
		return false
	}
	allZero := true
	allFF := true
	for _, b := range key {
		if b != 0 {
			allZero = false
		}
		if b != 0xff {
			allFF = false
		}
	}
	return !allZero && !allFF
}

func ExtractKeyCandidates() ([]Candidate, error) {
	pids := weixinPIDs()
	debugf("pids=%v", pids)
	if len(pids) == 0 {
		return nil, errors.New("未检测到 Weixin.exe")
	}
	seen := map[string]bool{}
	var out []Candidate
	for _, pid := range pids {
		handle, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, pid)
		if err != nil {
			debugf("pid=%d open_error=%v", pid, err)
			continue
		}
		needles := findBytes(handle, configCipherName)
		debugf("pid=%d needles=%d", pid, len(needles))
		var pairs [][]byte
		for _, address := range needles {
			pair := make([]byte, 16)
			binary.LittleEndian.PutUint64(pair[:8], uint64(address))
			binary.LittleEndian.PutUint64(pair[8:], uint64(len(configCipherName)))
			pairs = append(pairs, pair)
		}
		for _, pair := range pairs {
			matches := findBytes(handle, pair)
			debugf("pid=%d pair_matches=%d", pid, len(matches))
			for _, qaddr := range matches {
				node, ok := readRemote(handle, qaddr-0x10, 0x50)
				if !ok || len(node) < 0x40 {
					continue
				}
				if !containsUintptr(needles, u64(node, 0x10)) {
					continue
				}
				if u64(node, 0x18) != uint64(len(configCipherName)) {
					continue
				}
				configPtr := u64(node, 0x28)
				if configPtr < 0x10000 || configPtr >= 0x800000000000 {
					continue
				}
				obj, ok := readRemote(handle, uintptr(configPtr+0x88), 0x28)
				if !ok || len(obj) < 0x18 {
					continue
				}
				dataPtr := u64(obj, 0x8)
				dataLen := u64(obj, 0x10)
				if dataLen == 0 || dataLen > 1024 || dataPtr < 0x10000 || dataPtr >= 0x800000000000 {
					continue
				}
				blob, ok := readRemote(handle, uintptr(dataPtr), int(dataLen))
				if !ok || uint64(len(blob)) != dataLen {
					continue
				}
				decoded := make([]byte, len(blob))
				for i, b := range blob {
					decoded[i] = b ^ configXorMask[i%len(configXorMask)]
				}
				for _, match := range hexLiteralRE.FindAllSubmatch(decoded, -1) {
					run := strings.ToLower(string(match[1]))
					starts := []int{0}
					if len(run) > 96 {
						for start := 0; start <= len(run)-64; start += 32 {
							starts = append(starts, start)
						}
						starts = append(starts, len(run)-64)
					}
					added := map[int]bool{}
					for _, start := range starts {
						if added[start] || start+64 > len(run) {
							continue
						}
						added[start] = true
						key := decodeHex(run[start : start+64])
						if !probableKey(key) {
							continue
						}
						id := string(key)
						var salt []byte
						if start+96 <= len(run) {
							salt = decodeHex(run[start+64 : start+96])
						}
						if !seen[id] {
							seen[id] = true
							out = append(out, Candidate{Key: key})
						}
						if len(salt) == 16 {
							saltID := id + string(salt)
							if !seen[saltID] {
								seen[saltID] = true
								out = append(out, Candidate{Key: key, Salt: salt})
							}
						}
					}
				}
			}
		}
		windows.CloseHandle(handle)
	}
	debugf("candidates=%d", len(out))
	if len(out) == 0 {
		return nil, errors.New("微信进程中未找到数据库密钥")
	}
	return out, nil
}

func debugf(format string, args ...any) {
	file := os.Getenv("GFA_WECHAT_GO_DEBUG")
	if file == "" {
		return
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}

func containsUintptr(values []uintptr, value uint64) bool {
	for _, item := range values {
		if uint64(item) == value {
			return true
		}
	}
	return false
}

func decodeHex(value string) []byte {
	out := make([]byte, len(value)/2)
	for i := 0; i < len(out); i++ {
		out[i] = hexNibble(value[i*2])<<4 | hexNibble(value[i*2+1])
	}
	return out
}

func hexNibble(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10
	default:
		return 0
	}
}

// findKeyForFile 用候选密钥校验某个库文件的第一页。校验通过才说明密钥可用。
func findKeyForFile(candidates []Candidate, file DBFile) (KeyBlob, bool) {
	page1 := make([]byte, PageSize)
	fileHandle, err := os.Open(file.Path)
	if err != nil {
		return KeyBlob{}, false
	}
	_, err = fileHandle.ReadAt(page1, 0)
	fileHandle.Close()
	if err != nil {
		return KeyBlob{}, false
	}
	return findKeyForPage(candidates, page1)
}

func findKeyForPage(candidates []Candidate, page1 []byte) (KeyBlob, bool) {
	for _, cand := range candidates {
		if verifyKey(cand.Key, page1, nil) {
			return KeyBlob{Key: cand.Key}, true
		}
		if len(cand.Salt) == 16 && verifyKey(cand.Key, page1, cand.Salt) {
			return KeyBlob{Key: cand.Key, Salt: cand.Salt}, true
		}
	}
	return KeyBlob{}, false
}

func FindKeys(candidates []Candidate, files []DBFile) map[string]KeyBlob {
	keys := map[string]KeyBlob{}
	debugf("find_keys candidates=%d files=%d", len(candidates), len(files))
	for _, file := range files {
		if key, ok := findKeyForFile(candidates, file); ok {
			keys[file.Rel] = key
		}
	}
	return keys
}

func OpenProcessForDebug(pid uint32) (uint32, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, pid)
	if err != nil {
		return 0, fmt.Errorf("open process: %w", err)
	}
	windows.CloseHandle(handle)
	return pid, nil
}
