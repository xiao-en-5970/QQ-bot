package conf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// resetViper 让每个 test case 互不污染——viper 是全局单例，前一个 test 写的 BindEnv
// 会泄漏到下一个 test。
func resetViper(t *testing.T) {
	t.Helper()
	viper.Reset()
	Cfg = Config{}
}

// initViperForTest 还原 Init() 的关键 viper 配置，但用 test 提供的目录而不是固定路径。
// 这样测试既能完整覆盖 BindEnv / SetEnvKeyReplacer 行为，又跟生产 Init 解耦。
func initViperForTest(t *testing.T, dir string) {
	t.Helper()
	viper.SetConfigName("test")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(dir)
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	bindEnvKeys()
	if err := viper.ReadInConfig(); err != nil {
		// 没有 yaml 也行——让纯 env 测试也能跑
		var notFound viper.ConfigFileNotFoundError
		if !asNotFound(err, &notFound) {
			t.Fatalf("ReadInConfig: %v", err)
		}
	}
}

func asNotFound(err error, target *viper.ConfigFileNotFoundError) bool {
	if e, ok := err.(viper.ConfigFileNotFoundError); ok {
		*target = e
		return true
	}
	return false
}

// TestReload_FromYaml_PicksUpFileChanges：写 yaml → Init → 改 yaml → Reload → 字段更新。
func TestReload_FromYaml_PicksUpFileChanges(t *testing.T) {
	resetViper(t)

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "test.yaml")

	if err := os.WriteFile(yamlPath, []byte(`
gpt:
  recognize_model: kimi-k2-0905-preview
  quota_cooldown_seconds: 300
`), 0o644); err != nil {
		t.Fatal(err)
	}

	initViperForTest(t, dir)
	if err := reloadFromViper(false); err != nil {
		t.Fatalf("first reload: %v", err)
	}

	if got, want := Cfg.Gpt.RecognizeModel, "kimi-k2-0905-preview"; got != want {
		t.Errorf("init: RecognizeModel=%q, want %q", got, want)
	}
	if got, want := Cfg.Gpt.QuotaCooldownSeconds, 300; got != want {
		t.Errorf("init: QuotaCooldownSeconds=%d, want %d", got, want)
	}

	// 改 yaml + reload
	if err := os.WriteFile(yamlPath, []byte(`
gpt:
  recognize_model: moonshot-v1-8k
  quota_cooldown_seconds: 60
  quota_error_threshold: 5
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if got, want := Cfg.Gpt.RecognizeModel, "moonshot-v1-8k"; got != want {
		t.Errorf("after reload: RecognizeModel=%q, want %q", got, want)
	}
	if got, want := Cfg.Gpt.QuotaCooldownSeconds, 60; got != want {
		t.Errorf("after reload: QuotaCooldownSeconds=%d, want %d", got, want)
	}
	if got, want := Cfg.Gpt.QuotaErrorThreshold, 5; got != want {
		t.Errorf("after reload: QuotaErrorThreshold=%d, want %d", got, want)
	}
}

// TestReload_PreservesRuntimeFields：reload 后 main.go 启动期填的运行时字段不被覆盖。
func TestReload_PreservesRuntimeFields(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(yamlPath, []byte("gpt:\n  recognize_model: kimi-k2-0905-preview\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	initViperForTest(t, dir)
	if err := reloadFromViper(false); err != nil {
		t.Fatal(err)
	}

	// 模拟 main.go 启动期赋值
	uid := int64(2642865714)
	Cfg.User.UserID = &uid
	Cfg.Group.GroupID = []int64{1001, 1002, 1003}

	// 改 yaml（不动 user/group）+ reload
	if err := os.WriteFile(yamlPath, []byte("gpt:\n  recognize_model: moonshot-v1-8k\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Reload(); err != nil {
		t.Fatal(err)
	}

	if Cfg.User.UserID == nil || *Cfg.User.UserID != uid {
		t.Errorf("UserID 被 reload 覆盖了: got=%v want=%d", Cfg.User.UserID, uid)
	}
	if len(Cfg.Group.GroupID) != 3 {
		t.Errorf("GroupID 被 reload 覆盖了: got=%v", Cfg.Group.GroupID)
	}
	if Cfg.Gpt.RecognizeModel != "moonshot-v1-8k" {
		t.Errorf("yaml 字段没被 reload: got=%q", Cfg.Gpt.RecognizeModel)
	}
}

// TestReload_ReadsEnvAtRuntime：reload 时读当前 process env，让运行时 setenv 也能生效。
//
// 实际部署 env 来自 docker --env-file（容器外改要重启）；这个 case 模拟测试 / 调试时
// 用 os.Setenv 改了 env、然后 SIGHUP 触发 reload 的场景。
func TestReload_ReadsEnvAtRuntime(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte("gpt: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	initViperForTest(t, dir)

	t.Setenv("GPT_RECOGNIZE_MODEL", "moonshot-v1-32k")
	if err := Reload(); err != nil {
		t.Fatal(err)
	}
	if got, want := Cfg.Gpt.RecognizeModel, "moonshot-v1-32k"; got != want {
		t.Errorf("env reload: RecognizeModel=%q, want %q", got, want)
	}

	// 改 env 再 reload，配置跟着变
	t.Setenv("GPT_RECOGNIZE_MODEL", "kimi-k2-0905-preview")
	if err := Reload(); err != nil {
		t.Fatal(err)
	}
	if got, want := Cfg.Gpt.RecognizeModel, "kimi-k2-0905-preview"; got != want {
		t.Errorf("env re-reload: RecognizeModel=%q, want %q", got, want)
	}
}
