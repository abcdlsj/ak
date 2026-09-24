package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/secrets"
)

// TestCheckRemovable_Safety 是最高优先级的测试:误删用户自有工具的代价极高。
// ~/.local/bin 里有 warren、minions、agy、aicoding 等真实工具。
func TestCheckRemovable_Safety(t *testing.T) {
	dir := t.TempDir()
	s := &Syncer{Cfg: config.Default()}

	write := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}

	generated := "#!/usr/bin/env bash\n# ak:generated v1 kind=claude provider=x hash=abc\necho hi\n"

	tests := []struct {
		name    string
		path    string
		wantOK  bool
		reasonC string // 期望 reason 包含的子串
	}{
		{
			name:   "带标记的生成物可删",
			path:   write("ak-generated", generated, 0o700),
			wantOK: true,
		},
		{
			name:    "用户自己的同前缀脚本不可删",
			path:    write("ak-mine", "#!/bin/bash\necho my own script\n", 0o755),
			wantOK:  false,
			reasonC: "没有 ak:generated 标记",
		},
		{
			name:    "标记版本高于本程序时跳过",
			path:    write("ak-future", "#!/usr/bin/env bash\n# ak:generated v99 kind=claude provider=y hash=z\n", 0o700),
			wantOK:  false,
			reasonC: "标记版本",
		},
		{
			name:    "空文件不可删",
			path:    write("ak-empty", "", 0o700),
			wantOK:  false,
			reasonC: "标记",
		},
		{
			name:    "标记出现得太靠后则不认",
			path:    write("ak-late", strings.Repeat("# filler\n", 10)+"# ak:generated v1 kind=claude provider=z hash=q\n", 0o700),
			wantOK:  false,
			reasonC: "标记",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := dirEntryOf(t, tt.path)
			res, ok := s.checkRemovable(tt.path, e)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, 期望 %v (reason=%q)", ok, tt.wantOK, res.Reason)
			}
			if !ok && tt.reasonC != "" && !strings.Contains(res.Reason, tt.reasonC) {
				t.Errorf("reason = %q, 期望包含 %q", res.Reason, tt.reasonC)
			}
		})
	}
}

// TestCheckRemovable_RefusesSymlink 确认不跟随 symlink —— 否则可能顺着链接删掉要害文件。
func TestCheckRemovable_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	s := &Syncer{Cfg: config.Default()}

	// 目标文件本身带合法标记,但通过 symlink 访问时仍必须拒绝。
	target := filepath.Join(dir, "real-target")
	if err := os.WriteFile(target, []byte("# ak:generated v1 kind=claude provider=x hash=a\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "ak-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	res, ok := s.checkRemovable(link, dirEntryOf(t, link))
	if ok {
		t.Fatal("symlink 被判定为可删,这会导致误删链接目标")
	}
	if !strings.Contains(res.Reason, "symlink") {
		t.Errorf("reason = %q, 期望提到 symlink", res.Reason)
	}
	if _, err := os.Lstat(target); err != nil {
		t.Errorf("链接目标不应受影响: %v", err)
	}
}

// TestCollectOrphans_SkipsForeignPrefix 确认前缀不匹配的文件永不进入候选。
func TestCollectOrphans_SkipsForeignPrefix(t *testing.T) {
	dir := t.TempDir()
	// 模拟 ~/.local/bin 里的真实用户工具。
	for _, n := range []string{"warren", "minions", "agy", "aicoding", "codex-cpa"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/bash\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	s := &Syncer{Cfg: cfg, Resolver: secrets.Default(), DryRun: true}

	results, err := s.collectOrphans(dir, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		t.Errorf("用户工具 %s 不应出现在结果里(action=%s)", r.Path, r.Action)
	}

	// 全部文件必须还在。
	for _, n := range []string{"warren", "minions", "agy", "aicoding", "codex-cpa"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s 被删除了: %v", n, err)
		}
	}
}

// TestWriteFile_RefusesUnmarkedOverwrite 确认不覆盖用户自己的同名文件。
func TestWriteFile_RefusesUnmarkedOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ak-mine")
	original := "#!/bin/bash\necho user script\n"
	if err := os.WriteFile(p, []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}

	s := &Syncer{Cfg: config.Default()}
	res := s.writeFile(p, "#!/usr/bin/env bash\n# ak:generated v1\n", 0o700)

	if res.Action != ActionSkipped {
		t.Fatalf("action = %s, 期望 skipped", res.Action)
	}
	got, _ := os.ReadFile(p)
	if string(got) != original {
		t.Error("用户文件被覆盖了")
	}
}

func dirEntryOf(t *testing.T, path string) os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == filepath.Base(path) {
			return e
		}
	}
	t.Fatalf("找不到 %s", path)
	return nil
}
