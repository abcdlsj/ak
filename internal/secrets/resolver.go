// Package secrets 解析供应商密钥。第一阶段只支持明文,
// 接口留出 env:/cmd:/keychain: 三种引用形式,以后扩展不动调用方。
package secrets

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
)

// Resolver 把配置里的密钥声明解析成明文。
type Resolver interface {
	Resolve(p config.Provider) (string, error)
}

// Default 返回默认解析器。
func Default() Resolver { return plain{} }

type plain struct{}

// Resolve 优先用 api_key 明文;否则按 api_key_ref 的前缀分派。
func (plain) Resolve(p config.Provider) (string, error) {
	if p.APIKey != "" {
		return p.APIKey, nil
	}
	ref := p.APIKeyRef
	if ref == "" {
		return "", nil
	}
	scheme, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return "", fmt.Errorf("api_key_ref %q 缺少 scheme,应形如 env:NAME", ref)
	}
	switch scheme {
	case "env":
		v := os.Getenv(rest)
		if v == "" {
			return "", fmt.Errorf("环境变量 %s 为空", rest)
		}
		return v, nil
	case "cmd":
		out, err := exec.Command("sh", "-c", rest).Output()
		if err != nil {
			return "", fmt.Errorf("执行 %q: %w", rest, err)
		}
		return strings.TrimSpace(string(out)), nil
	case "keychain":
		return resolveKeychain(rest)
	default:
		return "", fmt.Errorf("不支持的 api_key_ref scheme %q", scheme)
	}
}

// resolveKeychain 从 macOS Keychain 取密钥,格式 keychain:<service>/<account>。
func resolveKeychain(spec string) (string, error) {
	service, account, ok := strings.Cut(spec, "/")
	if !ok {
		return "", fmt.Errorf("keychain 引用 %q 应形如 keychain:service/account", spec)
	}
	out, err := exec.Command("security", "find-generic-password",
		"-s", service, "-a", account, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("从 Keychain 读取 %s/%s: %w", service, account, err)
	}
	return strings.TrimSpace(string(out)), nil
}
