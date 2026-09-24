package shim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
)

// MarkerVersion 是当前生成物的标记版本。只删自己认识的版本,
// 未来版本的文件遇到旧 ak 时会被跳过而非误删。
const MarkerVersion = 1

// markerRe 匹配标记行。probe 只读前若干字节,避免把大文件读进内存。
var markerRe = regexp.MustCompile(`^#\s*ak:generated v(\d+)(?:\s+(.*))?$`)

// probeBytes 是识别标记时最多读取的字节数。
const probeBytes = 4096

// Marker 是生成物首行携带的元信息。
type Marker struct {
	Version  int
	Kind     string
	Provider string
	Hash     string
}

// Line 渲染标记行。
func (m Marker) Line() string {
	return fmt.Sprintf("# ak:generated v%d kind=%s provider=%s hash=%s",
		MarkerVersion, m.Kind, m.Provider, m.Hash)
}

// hashBody 计算正文指纹,用于判断内容是否变化(幂等)与 drift 检测。
func hashBody(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])[:16]
}

// readMarker 尝试从文件头部解析标记。
// 返回 ok=false 表示这不是 ak 生成的文件 —— 调用方必须据此拒绝删除。
func readMarker(path string) (Marker, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Marker{}, false
	}
	defer f.Close()

	buf := make([]byte, probeBytes)
	n, err := f.Read(buf)
	if n == 0 || (err != nil && err != io.EOF) {
		return Marker{}, false
	}
	head := buf[:n]

	// 标记必须在前几行内。逐行扫,遇到第一个匹配即止。
	start := 0
	for line := 0; line < 5 && start < len(head); line++ {
		end := start
		for end < len(head) && head[end] != '\n' {
			end++
		}
		if m, ok := parseMarkerLine(string(head[start:end])); ok {
			return m, true
		}
		start = end + 1
	}
	return Marker{}, false
}

func parseMarkerLine(line string) (Marker, bool) {
	sub := markerRe.FindStringSubmatch(trimCR(line))
	if sub == nil {
		return Marker{}, false
	}
	v, err := strconv.Atoi(sub[1])
	if err != nil {
		return Marker{}, false
	}
	m := Marker{Version: v}
	for _, kv := range splitFields(sub[2]) {
		k, val, ok := cut(kv, '=')
		if !ok {
			continue
		}
		switch k {
		case "kind":
			m.Kind = val
		case "provider":
			m.Provider = val
		case "hash":
			m.Hash = val
		}
	}
	return m, true
}

func trimCR(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\r' {
		return s[:n-1]
	}
	return s
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(s[i])
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func cut(s string, sep byte) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
