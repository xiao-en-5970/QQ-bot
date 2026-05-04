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

// Hfut hfut 后端接口配置。
//
// 故意单独成一个 section（而不是塞 Server 里），让 env 前缀就是 HFUT_ 而不是 SERVER_HFUT_。
//
// APIURL       hfut 后端 base URL（不带 /api/v1，工具集自己拼路径），例如 https://api.xiaoen.xyz
// APIJWTSecret bot 跟 hfut 共享的 service-to-service JWT 签名 secret（HS256）。
//              bot 这边每次请求自签 60s 有效期的 JWT 放 X-Bot-Service-Token 头；
//              hfut 那边用同一个 secret 验签 + 检 exp + 检 iss，0 维护数据库 token。
//              跟 hfut 主 JWT secret（user 登录用）独立，方便单独 rotate。
//              空时 bot 不能跟 hfut 联动（识别仍然能跑，但只发"[识别测试] 占位 ack" 不真上架）
//
// env 名（直白前缀，跟 hfut 那边的 BOT_SERVICE_JWT_SECRET 一组使用）：
//
//	HFUT_API_URL
//	HFUT_API_JWT_SECRET
type Hfut struct {
	APIURL       string `mapstructure:"api_url,omitempty"`
	APIJWTSecret string `mapstructure:"api_jwt_secret,omitempty"`
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
// AutoReplyWhitelist 启用了"非 @bot 也回复"模式的群号白名单。
//
//	默认空 = 没有任何群启用——全部群都只在被 @ 时才响应（默认安全行为）。
//	非空时，列表里的群会让 bot 抓取所有非自己的聊天记录，按 skill/bot/SKILL.md 里描述的
//	策略走"窗口聚合 + LLM 识别"流程。
//
// AutoReplyWindowSeconds 自动监听窗口的"沉默触发"秒数。
//
//	per-(group, user) 滑动窗口内，最后一条消息距今超过这个秒数就把整段交给 LLM 判定。
//	默认 60 秒。详见 skill/bot/SKILL.md 的"窗口聚合"章节。
//
// AutoReplyMaxWindowSize 单个窗口里允许攒多少条消息，超过就强制 flush（防止异常情况下一直攒不结算）。
//
//	默认 20 条。
//
// 历史的 get_group_history_interval / update_group_list_interval / retry 字段已弃用
// （WS 模式不再轮询历史消息），yaml 里如果还有这些字段会被 viper 静默忽略。
type Group struct {
	GroupID                []int64 `mapstructure:"group_id,omitempty"`
	AutoReplyWhitelist     []int64 `mapstructure:"auto_reply_whitelist,omitempty"`
	AutoReplyWindowSeconds int     `mapstructure:"auto_reply_window_seconds,omitempty"`
	AutoReplyMaxWindowSize int     `mapstructure:"auto_reply_max_window_size,omitempty"`
}

// IsAutoReplyGroup 判断某群是否启用了"非 @bot 也回复"白名单模式。
func (g Group) IsAutoReplyGroup(groupID int64) bool {
	for _, id := range g.AutoReplyWhitelist {
		if id == groupID {
			return true
		}
	}
	return false
}

// Commands 子命令开关 + 默认命令兜底。
//
// === Enabled（白名单，严格 opt-in）===
//
// 不配置 = 全部禁用，bot 不会响应任何 @；要让 bot 工作必须**显式列出**想启用的命令。
// 这种"未配置即关闭"语义是为了避免新部署 / 升级时管理员忘了配，bot 默默暴露所有功能
// 导致的风险（带宽 / 风控 / 上游服务挂掉 / 滥用）。
//
// 被禁用的命令直接静默 return，不向群里发任何消息，也不在菜单里展示——
// 管理员关命令的常见动机是带宽爆了 / 上游挂了 / 临时维护，这种场景下"群里多一条
// 'xxx 已禁用' 的回复"反而会刷屏，干脆完全闭嘴。
//
// 已识别的命令名：jm / pix / help / github / chat
//   - 前 4 个对应 ExecCmd switch 里同名 case
//   - chat 是与 Kimi 聊天的入口（显式 `@bot chat 你好`）
//
// === Default（无前缀兜底）===
//
// 当用户输入 `@bot xxx` 但 xxx 不是已识别的命令关键字时，按 Default 指定的命令兜底。
// 例如 Default="chat" 时 `@bot 你好` 会被当作 `@bot chat 你好` 处理；
// Default="" 时显示菜单（菜单里只列被启用的命令）。
//
// Default 应该是 Enabled 列表里的一项；如果指向被禁用的命令则会被白名单挡掉，静默忽略。
//
// === 配置示例 ===
//
// 完整启用所有命令并把 chat 设为默认（最接近老 LLOneBot 行为）：
//
//	commands:
//	  enabled: [jm, pix, help, github, chat]
//	  default: chat
//
// env：
//
//	COMMANDS_ENABLED=jm,pix,help,github,chat
//	COMMANDS_DEFAULT=chat
//
// === viper 自动 split ===
//
// viper 1.20+ 的默认 mapstructure 会 StringToSliceHookFunc(",")，所以 env 写
// `COMMANDS_ENABLED=jm,pix` 会被解析成 []string{"jm","pix"}，无需额外 hook。
type Commands struct {
	Enabled []string `mapstructure:"enabled,omitempty"`
	Default string   `mapstructure:"default,omitempty"`
}

// IsEnabled 判断某个子命令是否启用。严格 opt-in：
//
//	列表为空 -> 一律 false（bot 不响应任何命令，需要管理员显式配置开启）
//	列表非空 -> 只有出现在列表里的命令返回 true
func (c Commands) IsEnabled(name string) bool {
	for _, s := range c.Enabled {
		if strings.TrimSpace(s) == name {
			return true
		}
	}
	return false
}

// AnyEnabled 是否至少启用了一个命令。用于启动期 sanity check 和 Menu() 判断"是否
// 有任何东西可展示"——全禁用时菜单返回空，让 bot 表现得彻底安静。
func (c Commands) AnyEnabled() bool {
	for _, s := range c.Enabled {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
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
//	MaxToolRounds  单次 Chat() 里允许的"工具往返"轮次上限（防止 Kimi 反复调工具不出最终答案）。
//	               <=0 时取代码内置默认 5。env: GPT_MAX_TOOL_ROUNDS
//	SystemPrompt   人设/系统提示词，留空使用一个简洁的默认值
type Gpt struct {
	APIKey         string `mapstructure:"api_key"`
	MaxContextSize int64  `mapstructure:"max_context_size"`
	MaxToolRounds  int    `mapstructure:"max_tool_rounds"`
	SystemPrompt   string `mapstructure:"system_prompt"`
}

type Config struct {
	Log      Log      `mapstructure:"log"`
	Server   Server   `mapstructure:"server"`
	Hfut     Hfut     `mapstructure:"hfut"`
	Pixiv    Pixiv    `mapstructure:"pixiv"`
	Group    Group    `mapstructure:"group"`
	User     User     `mapstructure:"user"`
	Cache    Cache    `mapstructure:"cache"`
	Tools    Tools    `mapstructure:"tools"`
	Gpt      Gpt      `mapstructure:"gpt"`
	Commands Commands `mapstructure:"commands"`
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
		"hfut.api_url", "hfut.api_jwt_secret",
		"log.std_out_log_level", "log.log_level", "log.log_file",
		"pixiv.pixiv_address", "pixiv.size",
		"group.auto_reply_whitelist",
		"group.auto_reply_window_seconds",
		"group.auto_reply_max_window_size",
		"user.user_id",
		"cache.tmp_dir", "cache.pdf_tmp_dir", "cache.max_size", "cache.clear_interval",
		"tools.jmcomic_bin", "tools.img2pdf_bin", "tools.python_bin",
		"gpt.api_key", "gpt.max_context_size", "gpt.max_tool_rounds", "gpt.system_prompt",
		"commands.enabled", "commands.default",
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

	// gpt 默认值：环上下文 40 条；prompt 留空让 Init 时给一个简洁的默认人设；
	// tool 循环上限 5 轮（实测正常一两轮就出答案，5 轮够兜底防 yo-yo）
	if c.Gpt.MaxContextSize <= 0 {
		c.Gpt.MaxContextSize = 40
	}
	if c.Gpt.MaxToolRounds <= 0 {
		c.Gpt.MaxToolRounds = 5
	}

	// 自动监听窗口默认值：60 秒沉默触发、单窗口最多 20 条
	if c.Group.AutoReplyWindowSeconds <= 0 {
		c.Group.AutoReplyWindowSeconds = 60
	}
	if c.Group.AutoReplyMaxWindowSize <= 0 {
		c.Group.AutoReplyMaxWindowSize = 20
	}
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}
