package kimi

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestReadSkillProgressiveDisclosure 验证渐进式披露重构后的子文件全部能被白名单 + handler 正常读取。
//
// readSkillHandler 用相对路径 "skill"，依赖 CWD 在仓库根（生产 docker 是 /app）。
// 测试时 go test 把 CWD 设到包目录 utils/kimi/，需要先 chdir 到仓库根。
func TestReadSkillProgressiveDisclosure(t *testing.T) {
	if err := os.Chdir("../.."); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}

	cases := []struct {
		name      string
		args      string
		wantOK    bool
		wantInBody string
	}{
		{"empty path → top index", `{"path":""}`, true, "可用 skill"},
		{"top SKILL.md", `{"path":"SKILL.md"}`, true, "可用 skill"},
		{"bot main", `{"path":"bot/SKILL.md"}`, true, "渐进式披露"},
		{"bot recognition", `{"path":"bot/recognition.md"}`, true, "窗口聚合"},
		{"bot account-model", `{"path":"bot/account-model.md"}`, true, "接收人重定向"},
		{"bot orphan", `{"path":"bot/orphan.md"}`, true, "请求下架"},
		{"bot qq-bind", `{"path":"bot/qq-bind.md"}`, true, "加急通路"},
		{"bot verbosity", `{"path":"bot/verbosity.md"}`, true, "verbose"},
		{"bot phases", `{"path":"bot/phases.md"}`, true, "P2c"},
		{"hfut-union rejected", `{"path":"hfut-union/SKILL.md"}`, false, "不在允许范围内"},
		{"absolute rejected", `{"path":"/etc/passwd"}`, false, "不允许"},
		{"dotdot rejected", `{"path":"bot/../../../etc/passwd"}`, false, "不允许"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := readSkillHandler(context.Background(), c.args)
			if err != nil {
				t.Fatalf("unexpected handler err: %v", err)
			}
			isError := strings.HasPrefix(out, "ERROR")
			if c.wantOK && isError {
				t.Fatalf("expected ok, got ERROR: %s", out)
			}
			if !c.wantOK && !isError {
				t.Fatalf("expected ERROR, got: %.100s", out)
			}
			if !strings.Contains(out, c.wantInBody) {
				t.Fatalf("expected body to contain %q, got first 200: %.200s", c.wantInBody, out)
			}
		})
	}
}
