// Package config 读写 ak 的唯一事实来源 ~/.config/ak/providers.toml。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

// Version 是配置文件格式版本。
const Version = 1

// Kind 区分供应商背后的引擎。
type Kind string

const (
	KindClaude Kind = "claude"
	KindCodex  Kind = "codex"
)

// Config 是 providers.toml 的顶层结构。
type Config struct {
	Version   int                 `toml:"version"`
	Settings  Settings            `toml:"settings"`
	Providers map[string]Provider `toml:"providers"`
}

// Settings 是全局设置。
type Settings struct {
	BinDir    string `toml:"bin_dir"`
	Prefix    string `toml:"prefix"`
	Default   string `toml:"default,omitempty"`
	ClaudeBin string `toml:"claude_bin,omitempty"`
	CodexBin  string `toml:"codex_bin,omitempty"`
}

// Provider 是单个供应商。claude 与 codex 共用此结构,各自忽略无关字段。
type Provider struct {
	Kind    Kind   `toml:"kind"`
	Display string `toml:"display,omitempty"`
	BaseURL string `toml:"base_url"`
	APIKey  string `toml:"api_key,omitempty"`
	// APIKeyRef 形如 env:NAME / cmd:... / keychain:...,与 APIKey 互斥。
	APIKeyRef string `toml:"api_key_ref,omitempty"`
	Model     string `toml:"model,omitempty"`

	// claude 专属:三档模型映射,空则由 model 推导。
	Haiku  string `toml:"haiku,omitempty"`
	Sonnet string `toml:"sonnet,omitempty"`
	Opus   string `toml:"opus,omitempty"`
	// KeyField 决定 key 写入哪个环境变量:auth_token(默认) 或 api_key。
	KeyField string `toml:"key_field,omitempty"`
	// ConfigDir 预留:非空则 shim 额外 export CLAUDE_CONFIG_DIR。
	ConfigDir string `toml:"config_dir,omitempty"`

	// codex 专属。
	ProviderID string `toml:"provider_id,omitempty"`
	WireAPI    string `toml:"wire_api,omitempty"`
	Reasoning  string `toml:"reasoning,omitempty"`
	// CodexHome 预留:非空则 shim 额外 export CODEX_HOME。
	CodexHome string `toml:"codex_home,omitempty"`

	// Env 是任意长尾环境变量,最后合并,可覆盖派生键。
	Env map[string]string `toml:"env,omitempty"`
	// Pricing 覆盖 models.dev 查不到的模型定价。
	Pricing *Pricing `toml:"pricing,omitempty"`
	// Variants 是命令的位置参数变体。
	Variants map[string]Variant `toml:"variants,omitempty"`
}

// Pricing 是每百万 token 的价格,单位美元。Discount 非零时作为 models.dev 价格的折扣系数。
type Pricing struct {
	Discount   float64 `toml:"discount,omitempty"`
	Input      float64 `toml:"input,omitempty"`
	Output     float64 `toml:"output,omitempty"`
	CacheRead  float64 `toml:"cache_read,omitempty"`
	CacheWrite float64 `toml:"cache_write,omitempty"`
}

// Variant 是一个变体:覆盖模型或 reasoning effort。
type Variant struct {
	Model     string            `toml:"model,omitempty"`
	Reasoning string            `toml:"reasoning,omitempty"`
	Env       map[string]string `toml:"env,omitempty"`
	// Shim 为 true 时额外生成独立命令 ak-<provider>-<variant>。
	Shim bool `toml:"shim,omitempty"`
}

// Names 返回排序后的供应商名,保证所有遍历确定性。
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Providers))
	for n := range c.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Dir 返回配置目录 ~/.config/ak。
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ak"), nil
}

// Path 返回 providers.toml 的绝对路径。
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "providers.toml"), nil
}

// DataDir 返回 ~/.local/share/ak,存放用量聚合与 session 归属索引。
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "ak"), nil
}

// Default 返回带默认值的空配置。
func Default() *Config {
	return &Config{
		Version:   Version,
		Settings:  Settings{BinDir: "~/.local/bin", Prefix: "ak-"},
		Providers: map[string]Provider{},
	}
}

// Load 读取配置。文件不存在时返回默认配置而非错误,首次运行即可用。
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	if cfg.Settings.BinDir == "" {
		cfg.Settings.BinDir = "~/.local/bin"
	}
	if cfg.Settings.Prefix == "" {
		cfg.Settings.Prefix = "ak-"
	}
	return cfg, nil
}

// Save 以 0600 原子写回配置。
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

// atomicWrite 先写同目录临时文件再 rename,避免中断留下半截文件。
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".ak-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AtomicWrite 供其他包复用的原子写。
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	return atomicWrite(path, data, mode)
}

// ExpandHome 把开头的 ~ 展开为用户主目录。
func ExpandHome(p string) string {
	if p == "" || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if len(p) > 1 && (p[1] == '/' || p[1] == filepath.Separator) {
		return filepath.Join(home, p[2:])
	}
	return p
}
