package conf

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/viper"
)

var Cfg Config

// Server 对应 NapCat 的 OneBot11 HTTP / WebSocket 接口配置。
//
// Address       NapCat HTTP 服务的根地址（用来发送 send_group_msg 等动作），结尾必须带 /
//               例如 https://bot-http.xiaoen.xyz/
// WSAddress     NapCat WebSocket Server 地址（事件推送），形如 wss://bot-ws.xiaoen.xyz/
//               消息接收走 WS 而不是轮询 get_group_msg_history（NapCat 那个接口在某些群里
//               返回会卡住"最新 20 条"不更新；详见 utils/wsclient 包注释里的踩坑记录）。
// AccessToken   HTTP server 鉴权 token（Authorization: Bearer ...）；空表示 NapCat 未启用鉴权
// WSAccessToken WS server 鉴权 token；NapCat 把 HTTP / WS 当两套独立的网络适配器，token 各自配
//               留空时回退到 AccessToken（适合两边配同一个 token 的简单场景）
type Server struct {
	Address       string `mapstructure:"address"`
	WSAddress     string `mapstructure:"ws_address,omitempty"`
	AccessToken   string `mapstructure:"access_token,omitempty"`
	WSAccessToken string `mapstructure:"ws_access_token,omitempty"`
}

type Pixiv struct {
	PixivAddress string `mapstructure:"pixiv_address"`
	Size         string `mapstructure:"size"`
}

type Log struct {
	StdOutLogLevel string `mapstructure:"std_out_log_level"`
	LogLevel       string `mapstructure:"log_level"`
	// LogFile 日志输出文件路径，留空时使用 ./logs/qq-bot.log
	LogFile string `mapstructure:"log_file"`
}

// Group 群相关配置。
//
// GroupID  bot 监听的群号列表；留空则启动时调 NapCat /get_group_list 自动获取。
//
//	WS 模式下"监听哪些群"完全由 NapCat 那边的 push 决定，这个字段保留只是为了
//	启动时打印一下方便排查，不参与消息接收。
//
// 历史的 get_group_history_interval / update_group_list_interval / retry 字段已弃用
// （WS 模式不再轮询历史消息），yaml 里如果还有这些字段会被 viper 静默忽略。
type Group struct {
	GroupID []int64 `mapstructure:"group_id,omitempty"`
}

type User struct {
	UserID *int64 `mapstructure:"user_id,omitempty"`
}

type Cache struct {
	TmpDir        string `mapstructure:"tmp_dir"`
	PdfTmpDir     string `mapstructure:"pdf_tmp_dir"`
	MaxSize       int64  `mapstructure:"max_size"`
	ClearInterval int64  `mapstructure:"clear_interval"`
}

// Tools 跨平台外部工具命令路径。
//
//	JmcomicBin  抓本子用的 jmcomic 命令
//	  - Windows 默认 ./package/jmcomic.exe（PyInstaller 打包版）
//	  - Linux   默认 jmcomic（pip install jmcomic 后在 PATH 中）
//	Img2pdfBin  图片转 pdf 用的 img2pdf 命令
//	  - Windows 默认 ./package/img2pdf.exe
//	  - Linux   默认 img2pdf（pip install img2pdf 后在 PATH 中）
//	PythonBin   Python 解释器（仅当 Img2pdfBin 是 .py 脚本时才用），默认 python3
//
// 任意一个均可被环境变量 TOOLS_JMCOMIC_BIN / TOOLS_IMG2PDF_BIN / TOOLS_PYTHON_BIN 覆盖。
type Tools struct {
	JmcomicBin string `mapstructure:"jmcomic_bin"`
	Img2pdfBin string `mapstructure:"img2pdf_bin"`
	PythonBin  string `mapstructure:"python_bin"`
}

// Gpt Kimi/Moonshot 聊天能力配置。
//
//	APIKey         Moonshot API key；为空则不启用聊天，CmdDefault 回退到打印菜单
//	               env: GPT_API_KEY；兼容老变量 MOONSHOT_KEY
//	MaxContextSize 每个用户保留的历史 Q/A 对数量（环形缓冲区大小），默认 40
//	SystemPrompt   人设/系统提示词，留空使用一个简洁的默认值
type Gpt struct {
	APIKey         string `mapstructure:"api_key"`
	MaxContextSize int64  `mapstructure:"max_context_size"`
	SystemPrompt   string `mapstructure:"system_prompt"`
}

type Config struct {
	Log    Log    `mapstructure:"log"`
	Server Server `mapstructure:"server"`
	Pixiv  Pixiv  `mapstructure:"pixiv"`
	Group  Group  `mapstructure:"group"`
	User   User   `mapstructure:"user"`
	Cache  Cache  `mapstructure:"cache"`
	Tools  Tools  `mapstructure:"tools"`
	Gpt    Gpt    `mapstructure:"gpt"`
}

// Init 加载配置：YAML（test.yaml） + 环境变量，env 优先级最高。
//
// 路径搜索：
//   - ./test.yaml          本地开发
//   - /app/test.yaml       Docker 容器
//   - /etc/qq-bot/test.yaml 系统级
//
// 生产部署（Docker）通常不挂 yaml，全部走环境变量（HFUT 模式：宿主机 /qq-bot-server/.env -> --env-file）。
// 本地开发（Windows）继续用 ./test.yaml 即可，env 不存在时不会覆盖。
func Init() (err error) {
	viper.SetConfigName("test")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("/app")
	viper.AddConfigPath("/etc/qq-bot")

	// 环境变量映射：server.address -> SERVER_ADDRESS
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	bindEnvKeys()

	if err = viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return fmt.Errorf("配置文件读取失败: %w", err)
		}
		// 没找到 yaml 也 OK，全部从 env 读
	}

	if err = viper.Unmarshal(&Cfg); err != nil {
		return fmt.Errorf("无法解析配置: %w", err)
	}

	if Cfg.Server.Address != "" && !strings.HasSuffix(Cfg.Server.Address, "/") {
		Cfg.Server.Address = Cfg.Server.Address + "/"
	}

	applyDefaults(&Cfg)
	return nil
}

// bindEnvKeys 显式 BindEnv 让 Unmarshal 也能拿到 env，否则 viper 默认只在 Get 时读 env。
//
// 注：group.group_id（[]int64）暂不支持环境变量，生产建议留空让 main.go 自动从 NapCat 拉群列表。
func bindEnvKeys() {
	keys := []string{
		"server.address", "server.ws_address", "server.access_token", "server.ws_access_token",
		"log.std_out_log_level", "log.log_level", "log.log_file",
		"pixiv.pixiv_address", "pixiv.size",
		"user.user_id",
		"cache.tmp_dir", "cache.pdf_tmp_dir", "cache.max_size", "cache.clear_interval",
		"tools.jmcomic_bin", "tools.img2pdf_bin", "tools.python_bin",
		"gpt.api_key", "gpt.max_context_size", "gpt.system_prompt",
	}
	for _, k := range keys {
		_ = viper.BindEnv(k)
	}
	// 兼容 moonshot SDK 老惯用的环境变量名 MOONSHOT_KEY（无前缀）
	_ = viper.BindEnv("gpt.api_key", "GPT_API_KEY", "MOONSHOT_KEY")
}

func applyDefaults(c *Config) {
	if c.Tools.PythonBin == "" {
		c.Tools.PythonBin = "python3"
	}
	if c.Tools.JmcomicBin == "" {
		if isWindows() {
			c.Tools.JmcomicBin = "./package/jmcomic.exe"
		} else {
			c.Tools.JmcomicBin = "jmcomic"
		}
	}
	if c.Tools.Img2pdfBin == "" {
		if isWindows() {
			c.Tools.Img2pdfBin = "./package/img2pdf.exe"
		} else {
			c.Tools.Img2pdfBin = "img2pdf"
		}
	}

	// 缓存与日志一律放到系统临时目录下的 qq-bot 子目录
	//   Linux / Mac : /tmp/qq-bot/...
	//   Windows     : %TEMP%/qq-bot/...
	// 不再污染工作目录；Docker 部署时建议宿主机 /qq-bot-server/cache:/tmp/qq-bot 整个挂出来
	cacheRoot := filepath.Join(os.TempDir(), "qq-bot")
	if c.Cache.TmpDir == "" {
		c.Cache.TmpDir = filepath.Join(cacheRoot, "jm")
	}
	if c.Cache.PdfTmpDir == "" {
		c.Cache.PdfTmpDir = filepath.Join(cacheRoot, "pdf")
	}
	if c.Log.LogFile == "" {
		c.Log.LogFile = filepath.Join(cacheRoot, "logs", "qq-bot.log")
	}

	// gpt 默认值：环上下文 40 条；prompt 留空让 Init 时给一个简洁的默认人设
	if c.Gpt.MaxContextSize <= 0 {
		c.Gpt.MaxContextSize = 40
	}
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}
