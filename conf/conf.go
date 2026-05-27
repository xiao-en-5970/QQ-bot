package conf

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/fsnotify/fsnotify"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// envFileCandidates 是 bot 启动时按顺序尝试加载的 .env 路径。
//
// 设计动机：用户希望去 yaml、走纯 .env 风格。viper.AutomaticEnv() 会让所有
// viper.Get(key) 直接读 os.Getenv，因此只要把 .env 内容塞进进程 env，就能
// 让现有的 viper 配置体系无缝吃到 .env 值。
//
// 路径选择跟现有 yaml 的搜索路径对齐：
//
//	./.env             本地开发（项目根）
//	/.env              生产宿主机
//	/app/.env          Docker 容器
//	/etc/qq-bot/.env   系统级
//
// 第一个**存在**的文件会被使用；不存在则跳过（继续走 docker --env-file / shell export 注入的 env）。
var envFileCandidates = []string{
	"./.env",
	"/.env",
	"/app/.env",
	"/etc/qq-bot/.env",
}

// loadedEnvPath 记录启动时实际加载到的 .env 路径——reload 时同路径 Overload，
// 也用来给 fsnotify watcher 监听文件改动。空 = 没找到任何 .env，纯 env 模式。
var loadedEnvPath string

// Cfg 是全局配置——读侧直接 conf.Cfg.X 即可，无需加锁。
//
// 热更新机制（参考 HFUT 后端 `app/config/config.go`）：
//   - yaml 文件变化 → viper.WatchConfig 自动触发 reload
//   - SIGHUP 信号 → 显式触发 reload（kill -HUP <pid>）
//   - reload 时整体 Unmarshal 到临时 `newCfg`、applyDefaults、再一次性赋值给 Cfg
//
// 字段级原子性：Go struct 整体赋值不保证多字段原子可见，但所有字段都是
// string/int/slice 类型，单字段读单字段写不会撕裂；reload 频率极低（人工触发 / 文件改动），
// 业务读到"半新半旧"的窗口实际只有 µs 级，可接受不加锁。
//
// 想要严格快照请用 conf.Snapshot()，它在写锁保护下返回一份深拷贝。
var Cfg Config

// cfgMu 写锁——只在 reload 路径上拿；读侧不加锁（见 Cfg 注释）。
var cfgMu sync.Mutex

// Server 对应 NapCat 的 OneBot11 HTTP / WebSocket 接口配置。
//
// Address       NapCat HTTP 服务的根地址（用来发送 send_group_msg 等动作），结尾必须带 /
//
//	例如 https://bot-http.xiaoen.xyz/
//
// WSAddress     NapCat WebSocket Server 地址（事件推送），形如 wss://bot-ws.xiaoen.xyz/
//
//	消息接收走 WS 而不是轮询 get_group_msg_history（NapCat 那个接口在某些群里
//	返回会卡住"最新 20 条"不更新；详见 utils/wsclient 包注释里的踩坑记录）。
//
// AccessToken   HTTP server 鉴权 token（Authorization: Bearer ...）；空表示 NapCat 未启用鉴权
// WSAccessToken WS server 鉴权 token；NapCat 把 HTTP / WS 当两套独立的网络适配器，token 各自配
//
//	留空时回退到 AccessToken（适合两边配同一个 token 的简单场景）
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
//
//	bot 这边每次请求自签 60s 有效期的 JWT 放 X-Bot-Service-Token 头；
//	hfut 那边用同一个 secret 验签 + 检 exp + 检 iss，0 维护数据库 token。
//	跟 hfut 主 JWT secret（user 登录用）独立，方便单独 rotate。
//	空时 bot 不能跟 hfut 联动（识别仍然能跑，但只发"[识别测试] 占位 ack" 不真上架）
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
//	默认 300 秒（5 分钟）。详见 skill/bot/SKILL.md 的"窗口聚合"章节。
//
//	历史上默认 60s；实际场景里用户在一个上架会话里会断断续续发图 + 文字 + 补充
//	价格 + 重新换图，3-5 分钟才发完是常见节奏。60s 太短会把同一上架切成多份
//	snapshot 给 Kimi，识别成多件商品（其实是同一件）造成去重撞重 / 价格漂移。
//	300s 大幅降低这种误切分。
//
// AutoReplyMaxWindowSize 单个窗口里允许攒多少条消息，超过就强制 flush（防止异常情况下一直攒不结算）。
//
//	默认 20 条。
//
// AutoReplyVerbosity 控制 bot 在群里"开口的频率"。
//
//	verbose（默认，推荐开发/调试用）：所有 dispatch 函数返回的回执都发——
//	  含成功落库 / 去重命中 / 反问 / 同步失败 等所有动作。这是当前调试期的行为。
//	normal（推荐生产用）：只在"产生了实际后果"才出声——
//	  - 真落库成功（含 goods_id / article_id）
//	  - 去重命中（用户需要知道为什么没发）
//	  - 反问（多个在售时让用户指明，用户主动发起的必须反馈）
//	  其它情况静默：同步 hfut 失败、网络错、识别失败兜底——避免让群友看到无意义错误，
//	  让 bot 体验更"无感"（用户没主动喊 bot，就不该出现 bot 的提示）。
//
//	生产切换方式：env 设 GROUP_AUTO_REPLY_VERBOSITY=normal 或 yaml 改这个字段，
//	不需要重新发版。SKILL.md 的"无感模式"章节有详细说明。
//
// 历史的 get_group_history_interval / update_group_list_interval / retry 字段已弃用
// （WS 模式不再轮询历史消息），yaml 里如果还有这些字段会被 viper 静默忽略。
type Group struct {
	GroupID                []int64 `mapstructure:"group_id,omitempty"`
	AutoReplyWhitelist     []int64 `mapstructure:"auto_reply_whitelist,omitempty"`
	AutoReplyWindowSeconds int     `mapstructure:"auto_reply_window_seconds,omitempty"`
	AutoReplyMaxWindowSize int     `mapstructure:"auto_reply_max_window_size,omitempty"`
	AutoReplyVerbosity     string  `mapstructure:"auto_reply_verbosity,omitempty"`
}

// IsAutoReplyGroup 判断某群是否启用了"非 @bot 也回复"白名单模式。
//
// 优先用 hfut 同步过来的 RuntimeOverlay.AutoReplyWhitelist；未同步过 / 拉取失败 →
// 兜底用 env/yaml 加载的 g.AutoReplyWhitelist。
func (g Group) IsAutoReplyGroup(groupID int64) bool {
	for _, id := range g.effectiveAutoReplyWhitelist() {
		if id == groupID {
			return true
		}
	}
	return false
}

// effectiveAutoReplyWhitelist 取当前生效的 AutoReplyWhitelist（runtime 优先，env 兜底）。
func (g Group) effectiveAutoReplyWhitelist() []int64 {
	if ov := GetRuntimeOverlay(); ov != nil && ov.AutoReplyWhitelist != nil {
		return ov.AutoReplyWhitelist
	}
	return g.AutoReplyWhitelist
}

// EffectiveAutoReplyWhitelist 公开版——给运维 / 日志展示用。
func (g Group) EffectiveAutoReplyWhitelist() []int64 {
	return g.effectiveAutoReplyWhitelist()
}

// IsAutoReplyVerbose 当前 verbosity 是否 verbose（默认/未配置 = verbose）。
//
// 注意：null/空串/任何不认识的值都视作 verbose——这是为了让"忘记配 / 配错"时仍然
// 保持开发者友好的 debug 行为，而不是悄无声息地把回执全屏蔽。
// 想真切到生产无感模式，必须显式配 normal。
func (g Group) IsAutoReplyVerbose() bool {
	return strings.ToLower(strings.TrimSpace(g.AutoReplyVerbosity)) != "normal"
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
//
//	Model          @bot chat 用的模型名（Moonshot 模型 ID，例如 moonshot-v1-128k / moonshot-v1-auto /
//	               kimi-latest / kimi-k2-0905-preview）。env: GPT_MODEL；留空 = moonshot-v1-auto
//	               （省钱：Moonshot 自动按上下文大小选 v1-8k/32k/128k）
//	RecognizeModel 自动监听路径下"业务动作识别"用的模型；这条任务对中文理解 + 严格 JSON 输出要求高，
//	               默认走 Kimi K2（kimi-k2-0905-preview）。env: GPT_RECOGNIZE_MODEL；留空 = 默认
//	               设计：识别和闲聊分别配模型——识别要稳准、闲聊用便宜的；运维可独立调
//	QuotaCooldownSeconds 连续 ≥ QuotaErrorThreshold 次撞 exceeded_current_quota_error 后整个 LLM
//	               调用层进入冷却 N 秒（期间 short-circuit 不再调 API、当前窗口直接静默丢弃）。
//	               <=0 时取默认 1800 秒（30 分钟）。env: GPT_QUOTA_COOLDOWN_SECONDS
//	QuotaErrorThreshold 触发冷却所需的连续 quota 错次数，<=0 时取默认 3。env: GPT_QUOTA_ERROR_THRESHOLD
type Gpt struct {
	APIKey               string `mapstructure:"api_key"`
	MaxContextSize       int64  `mapstructure:"max_context_size"`
	MaxToolRounds        int    `mapstructure:"max_tool_rounds"`
	SystemPrompt         string `mapstructure:"system_prompt"`
	Model                string `mapstructure:"model"`
	RecognizeModel       string `mapstructure:"recognize_model"`
	// VisionModel 用于 OCR 上架的多模态模型。默认 moonshot-v1-32k-vision-preview；
	// 仅当窗口里只有图片、用户没补任何业务文字、又过了沉默期时触发——对每张图单独
	// 调一次 vision API 让模型直接从图片里抽 title / price 等再上架。
	// env: GPT_VISION_MODEL
	VisionModel          string `mapstructure:"vision_model"`
	QuotaCooldownSeconds int    `mapstructure:"quota_cooldown_seconds"`
	QuotaErrorThreshold  int    `mapstructure:"quota_error_threshold"`
}

// Internal bot 暴露给 hfut 调用的"反向 HTTP API" 配置。
//
// 用途：
//   - hfut 在 QQ 绑定流程里需要让 bot 帮忙：判断 QQ 是不是 bot 好友、给 QQ 发私聊验证码等
//   - hfut 反向调 bot 的 internal HTTP server，鉴权 = JWT 验签
//
// 鉴权设计（跟 bot → hfut 方向对称）：
//   - 两个调用方向**共享同一个 HS256 secret**——bot 端从 conf.Hfut.APIJWTSecret 读
//     （env HFUT_API_JWT_SECRET），hfut 端从 config.BotServiceJWTSecret 读
//     （env BOT_SERVICE_JWT_SECRET）；两个 env 名不同但值相同
//   - 调用方每次自签 60s 有效期 JWT 放 X-Service-Token 头
//   - 接收方靠 iss 区分调用方向：
//     bot → hfut：iss = "HFUT-Graduation-Project-bot"
//     hfut → bot：iss = "HFUT-Graduation-Project-hfut"
//     这样即便 secret 共享，两个方向的 token 也不能互换使用
//   - HFUT_API_JWT_SECRET 为空 → server 整体不启动（安全降级）
//
// 安全考虑：
//   - 这个 server 不该对公网开放——只让 hfut（同一 docker network）能访问
//   - docker compose 部署时 bot 容器的这个端口**不要 ports 映射到宿主机**，
//     只让 hfut 容器通过 internal network 访问；name resolution: http://qq-bot-server:8090
//
// 字段：
//
//	Port   监听端口；默认 8090
//
// env：
//
//	BOT_INTERNAL_API_PORT
type Internal struct {
	Port int `mapstructure:"port,omitempty"`
}

// Bot bot 自身的运营 / 通知配置——跟"对内通知群"相关的开关都集中在这里。
//
// OpsGroupIDs   bot 的"对内通知 / 操作记录"群号列表；任何上架 / 群接入申请等关键事件都会
//
//	转发一份到这些群里，方便运营盯。空 = 不通知。
//	env: BOT_OPS_GROUP_IDS（逗号分隔，例如 "1084352497,1234567890"）
//	兼容老的 BOT_OPS_GROUP_ID（单值）：未配置 IDS 但配了 ID 时按单元素列表使用。
//
//	所有 OpsGroup 同时是"运维查询群"——在这些群里 @bot 提问会进 ops_query 路径，
//	bot 调 LLM 生成只读 SQL，调 hfut /bot/admin/sql 执行后把结果回到群里。
//	详见 logic/ops_query.go 与 skill/bot/ops_query.md。
type Bot struct {
	OpsGroupIDs []int64 `mapstructure:"ops_group_ids,omitempty"`

	// OpsGroupID 旧字段——保留作为向后兼容入口；reload 时会被合并进 OpsGroupIDs。
	// 新部署请直接使用 OPS_GROUP_IDS。
	OpsGroupID int64 `mapstructure:"ops_group_id,omitempty"`

	// SilentMode 灰度静默模式。上线前实战演练时打开：
	//   - 任何对**非 OpsGroupIDs** 的群消息（识别回执 / @bot 命令 / dup ack / 反向 internal
	//     API 发出的群消息）都被静默掉，bot 不在群里发任何字
	//   - 任何群文件上传（jm pdf / pix）也静默
	//   - **运维群仍正常**：`NotifyOps*` 系列发往 OpsGroupIDs 的运维提醒原样下发，
	//     ops_query @bot 的运维查询也照常工作
	//   - **私聊不受影响**：QQ 绑定 / 解绑验证码、订单加急、群接入申请回执等均正常下发。
	//     这是用户主动触发的链路，不会打扰群友，灰度期间应保留以便真实用户能完成绑定
	//
	// 业务侧（hfut 数据库写入 / Kimi 识别 / bot_dispatch_event 记录等）**不受影响**——
	// 静默只是关掉"群里对外可见的消息出口"，目的是在真实校园群里跑识别准确率实测，
	// 而群友感受不到 bot 存在。
	//
	// env: BOT_SILENT_MODE=true|false（默认 false）；admin UI 也可改（bot_runtime_config 表）
	SilentMode bool `mapstructure:"silent_mode,omitempty"`
}

// IsOpsGroup 群号是否运维群。
//
// 优先用 hfut 同步过来的 RuntimeOverlay.OpsGroupIDs；未同步过 / 拉取失败 → 兜底
// 用 env/yaml 加载的静态字段 b.OpsGroupIDs。
func (b Bot) IsOpsGroup(groupID int64) bool {
	if groupID == 0 {
		return false
	}
	for _, id := range b.effectiveOpsGroupIDs() {
		if id == groupID {
			return true
		}
	}
	return false
}

// effectiveOpsGroupIDs 取当前生效的 OpsGroupIDs（runtime 优先，env 兜底）。
func (b Bot) effectiveOpsGroupIDs() []int64 {
	if ov := GetRuntimeOverlay(); ov != nil && ov.OpsGroupIDs != nil {
		return ov.OpsGroupIDs
	}
	return b.OpsGroupIDs
}

// EffectiveOpsGroupIDs 公开版——给 ops_notify 等需要遍历下发的调用方。
//
// 不直接读 b.OpsGroupIDs 是为了在 runtime 配置启用后，所有 ops 通知都按新列表广播。
func (b Bot) EffectiveOpsGroupIDs() []int64 {
	return b.effectiveOpsGroupIDs()
}

// PrimaryOpsGroup 默认通知群——返回 OpsGroupIDs 第一个，列表为空时返回 0。
//
// 给"老 NotifyOps 调用单群发"路径兜底；新调用建议遍历 OpsGroupIDs 全发。
func (b Bot) PrimaryOpsGroup() int64 {
	if len(b.OpsGroupIDs) == 0 {
		return 0
	}
	return b.OpsGroupIDs[0]
}

// effectiveSilentMode 取当前生效的 SilentMode（runtime 优先，env 兜底）。
func (b Bot) effectiveSilentMode() bool {
	if ov := GetRuntimeOverlay(); ov != nil && ov.SilentMode != nil {
		return *ov.SilentMode
	}
	return b.SilentMode
}

// IsSilentForGroup 该群是否应被静默——SilentMode 开 + 非 OpsGroup → true。
//
// 详见 SilentMode 字段注释。
func (b Bot) IsSilentForGroup(groupID int64) bool {
	if !b.effectiveSilentMode() {
		return false
	}
	return !b.IsOpsGroup(groupID)
}

// IsSilentForPrivate 私聊是否应被静默——**永远 false**。
//
// 设计取舍：SilentMode 灰度静默的核心目标是"用户在群里感受不到 bot"，但 QQ 绑定 /
// 解绑流程依赖私聊验证码——这条链路属于"用户主动操作 App 触发的私聊"，不会打扰群
// 友，灰度演练阶段也应该让真实用户能完成绑定。所以私聊出口（SendPrivateText）保留，
// 不受 SilentMode 影响。
//
// 私聊出口包括：QQ 绑定 / 解绑验证码、订单加急私聊、群接入申请回执等，都允许下发。
func (b Bot) IsSilentForPrivate() bool {
	return false
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
	Internal Internal `mapstructure:"internal"`
	Bot      Bot      `mapstructure:"bot"`
}

// Init 加载配置：.env 文件 + 环境变量 + 可选的 yaml，env 优先。
//
// 路径搜索：
//
//	.env：./.env  /.env  /app/.env  /etc/qq-bot/.env  （第一个存在的）
//	yaml：./test.yaml  /app/test.yaml  /etc/qq-bot/test.yaml
//
// 启动期 .env 加载用 godotenv.Load——**仅当**进程 env 里还没有该 key 时才注入，
// 也就是 docker `--env-file` / shell `export` 的值优先级高于 .env 文件，
// .env 提供合理 default。
//
// 生产推荐：
//
//   - Docker：宿主机 /qq-bot-server/.env --env-file 注入即可，不需要再放 .env 进容器
//   - 裸机：直接放 ./.env 在工作目录
//   - 不再依赖 yaml（用户决定去 yaml）；有遗留 yaml 也能跑，env 优先生效
//
// 热更新：
//
//   - SIGHUP（kill -HUP <pid>）→ Reload()：godotenv.Overload 重读 .env **覆盖**进程 env，
//     再 viper Unmarshal 让 Cfg 拿到新值。这是让"改 .env 立即生效"工作的关键。
//   - .env 文件 fsnotify 改动 → 同样路径
//   - yaml 文件改动（viper.WatchConfig）→ 同样路径
//
// 哪些字段改了 reload 后会生效（hot-swappable）：
//
//   - GROUP_AUTO_REPLY_VERBOSITY / WHITELIST / WINDOW_SECONDS / MAX_WINDOW_SIZE
//   - GPT_API_KEY / MODEL / RECOGNIZE_MODEL / QUOTA_* / MAX_TOOL_ROUNDS
//   - COMMANDS_ENABLED / COMMANDS_DEFAULT
//   - TOOLS_*
//   - BOT_OPS_GROUP_ID / BOT_OPS_GROUP_IDS（运维群列表；下次 @bot 时立即生效）
//
// 哪些字段必须**重启**进程才生效：
//
//   - SERVER_ADDRESS / WS_ADDRESS / ACCESS_TOKEN / WS_ACCESS_TOKEN  （wsclient 已建立连接）
//   - HFUT_API_URL / HFUT_API_JWT_SECRET                            （http client 已构造）
//   - INTERNAL_PORT                                                 （internal HTTP server 已绑定）
//   - LOG_*                                                         （logger 已初始化）
//
// 简单原则：跟"已建立的连接 / 已开始的监听"相关的字段都需重启。
//
// **特别提醒**：reload 只覆盖**配置值**，不覆盖**编译后的代码逻辑**。
// 改了 Go 源代码（新增/修改 handler / 分流逻辑 / dao / dispatch 等）必须
// `go build` 后重启 bot 进程；SIGHUP 不会让新代码生效。
func Init() (err error) {
	// 启动期：先把 .env 加进进程 env（不覆盖已有），让 viper.AutomaticEnv 看到 .env 的值
	loadedEnvPath = pickFirstExisting(envFileCandidates)
	if loadedEnvPath != "" {
		if loadErr := godotenv.Load(loadedEnvPath); loadErr == nil {
			log.Printf("[conf] loaded .env from %s (existing env wins over .env)", loadedEnvPath)
		} else {
			log.Printf("[conf] WARN: load .env from %s failed: %v (继续用 docker/shell 注入的 env)", loadedEnvPath, loadErr)
		}
	}

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

	return reloadFromViper(false)
}

// pickFirstExisting 返回 paths 中第一个文件存在的路径；都不存在返回 ""。
func pickFirstExisting(paths []string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Reload 从 .env 文件 + viper（yaml + env）重新解析配置，整体替换 Cfg。
//
// 关键步骤：
//
//  1. godotenv.Overload(loadedEnvPath)——**覆盖**模式重读 .env，让用户对 .env 文件
//     的修改立即写入进程 env。如果不 Overload，启动后 .env 改动永远读不出来：
//     启动那次 Load 已经把所有 key 写入 env，之后再 Load（不覆盖）会全部跳过。
//  2. viper.ReadInConfig()——重读 yaml 文件（如果有）
//  3. viper.Unmarshal——viper.AutomaticEnv 让 Unmarshal 优先取进程 env 里的最新值
//  4. 整体替换 Cfg，保留 User.UserID / Group.GroupID 等运行时字段
//
// 调用场景：
//
//   - .env 文件 fsnotify 检测到改动（自动）
//   - viper 文件 watcher 检测到 yaml 改动（自动）
//   - 进程收到 SIGHUP（kill -HUP <pid>）
//   - 测试 / 调试时显式手工触发
//
// 安全性：
//
//   - 用临时 newCfg 完成解析、defaults、normalize 后才写回 Cfg，失败不会污染当前 Cfg
//   - User.UserID / Group.GroupID 是运行时探测的（NapCat /get_login_info / /get_group_list），
//     reload 不应覆盖；reloadFromViper(preserveRuntime=true) 显式保留旧值
//   - cfgMu 串行化写——多个 reload 并发触发时（比如 .env 改两次 + 一个 SIGHUP）互不冲突
func Reload() error {
	if loadedEnvPath != "" {
		if err := godotenv.Overload(loadedEnvPath); err != nil {
			log.Printf("[conf] WARN: reload .env from %s failed: %v (env 不变，仅 yaml/已有 env 生效)", loadedEnvPath, err)
		} else {
			log.Printf("[conf] reloaded .env from %s (overload, .env wins over existing env)", loadedEnvPath)
		}
	}
	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return fmt.Errorf("reload 读 yaml 失败: %w", err)
		}
		// yaml 不存在也 OK，env 仍然能 reload
	}
	return reloadFromViper(true)
}

// reloadFromViper 是 Init/Reload 共用的"viper → Cfg" 解析逻辑。
//
// preserveRuntime=true 时保留 main 启动期填进 Cfg 的运行时字段（User.UserID/Group.GroupID）；
// 首次 Init 没有运行时字段所以传 false。
func reloadFromViper(preserveRuntime bool) error {
	var newCfg Config
	if err := viper.Unmarshal(&newCfg); err != nil {
		return fmt.Errorf("无法解析配置: %w", err)
	}
	if newCfg.Server.Address != "" && !strings.HasSuffix(newCfg.Server.Address, "/") {
		newCfg.Server.Address = newCfg.Server.Address + "/"
	}
	applyDefaults(&newCfg)

	cfgMu.Lock()
	defer cfgMu.Unlock()
	if preserveRuntime {
		if newCfg.User.UserID == nil {
			newCfg.User.UserID = Cfg.User.UserID
		}
		if len(newCfg.Group.GroupID) == 0 {
			newCfg.Group.GroupID = Cfg.Group.GroupID
		}
	}
	Cfg = newCfg
	return nil
}

// Snapshot 返回当前配置的深拷贝快照，需要"瞬时一致" 的调用方用。
//
// 普通调用直接 conf.Cfg.X 即可——string/int/slice 字段单读不会撕裂。
func Snapshot() Config {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	// Config 内嵌结构体都按值传递（slice/map 浅拷贝），对 reload 后的 read-only 使用足够
	return Cfg
}

// WatchAndReload 启动三条热更新通道：
//
//  1. viper.WatchConfig —— 监听已加载的 yaml 文件改动；任何 Write/Create 都触发 Reload
//  2. .env 文件 fsnotify watcher —— 监听 Init 时找到的 .env 改动；vim 等编辑器会
//     用"先 swap 再 rename"的写入方式，watcher 在文件被 rename 后会失效，所以这里
//     在 Remove/Rename 事件后重新 Add 一遍。
//  3. SIGHUP 信号 —— 收到时触发 Reload（Linux 习惯：kill -HUP <pid>；Windows 跳过）
//
// ctx 取消时关闭信号 channel 退出 goroutine；viper 的文件 watcher 由 viper 自管理。
//
// 调用方：main.go 启动后 `go conf.WatchAndReload(ctx)`；不要重复 start——
// viper.WatchConfig 重复调用会启动多个 fsnotify watcher。
func WatchAndReload(ctx context.Context) {
	// yaml 文件 watcher（viper 内置）
	viper.OnConfigChange(func(e fsnotify.Event) {
		if e.Op&(fsnotify.Write|fsnotify.Create) == 0 {
			return
		}
		if err := Reload(); err != nil {
			log.Printf("[conf] reload 失败 (yaml=%s): %v", e.Name, err)
			return
		}
		log.Printf("[conf] 已重载配置（触发文件=%s）", e.Name)
	})
	viper.WatchConfig()

	// .env 文件 watcher——独立 fsnotify 实例
	go watchEnvFile(ctx)

	// SIGHUP 通道——Windows 没有 SIGHUP，跳过即可
	if runtime.GOOS == "windows" {
		<-ctx.Done()
		return
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	log.Printf("[conf] 配置热更新已启用：.env / yaml 文件改动自动 reload；kill -HUP %d 也可强制 reload", os.Getpid())

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
			if err := Reload(); err != nil {
				log.Printf("[conf] SIGHUP reload 失败: %v", err)
				continue
			}
			log.Printf("[conf] 已重载配置（触发=SIGHUP）")
		}
	}
}

// watchEnvFile 监听 Init 时找到的 .env 文件改动，触发 Reload。
//
// 处理 vim 等编辑器的"swap+rename"写入模式：
//   - Write/Create 事件 → 直接 Reload
//   - Remove/Rename 事件 → 老 inode 已经断开，需要 absPath 重新 Add 才能继续监听
//     新文件；尝试失败说明文件还没被重新创建（vim 在过渡阶段），下次事件时再补
//
// 没有 .env 文件（loadedEnvPath==""）时本函数直接 return，不浪费 watcher。
func watchEnvFile(ctx context.Context) {
	if loadedEnvPath == "" {
		return
	}
	absPath, err := filepath.Abs(loadedEnvPath)
	if err != nil {
		log.Printf("[conf] env watch: invalid path %s: %v", loadedEnvPath, err)
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[conf] env watch: failed to create watcher: %v", err)
		return
	}
	defer w.Close()
	if err := w.Add(absPath); err != nil {
		log.Printf("[conf] env watch: failed to watch %s: %v", absPath, err)
		return
	}
	log.Printf("[conf] env watch: watching %s for changes", absPath)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if err := Reload(); err != nil {
					log.Printf("[conf] env reload 失败: %v", err)
				} else {
					log.Printf("[conf] 已重载配置（触发=.env 改动 %s）", ev.Name)
				}
			}
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				// 重新 Add 让 watcher 跟上 vim 的新 inode
				_ = w.Remove(absPath)
				if err := w.Add(absPath); err != nil {
					// 文件可能在重命名过渡中，下次事件时再补
					log.Printf("[conf] env watch: re-add 失败 (vim swap?): %v", err)
				}
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Printf("[conf] env watch error: %v", err)
		}
	}
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
		"group.auto_reply_verbosity",
		"user.user_id",
		"cache.tmp_dir", "cache.pdf_tmp_dir", "cache.max_size", "cache.clear_interval",
		"tools.jmcomic_bin", "tools.img2pdf_bin", "tools.python_bin",
		"gpt.api_key", "gpt.max_context_size", "gpt.max_tool_rounds", "gpt.system_prompt",
		"gpt.model", "gpt.recognize_model", "gpt.quota_cooldown_seconds", "gpt.quota_error_threshold",
		"commands.enabled", "commands.default",
		"internal.port",
		"bot.ops_group_id", "bot.ops_group_ids",
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
	// 模型选型默认值——@bot chat 用 v1-auto（按上下文自动选 8k/32k/128k 省钱），
	// 业务识别用 Kimi K2（中文理解 + 结构化 JSON 输出明显更稳，识别准确度对它最敏感）
	if strings.TrimSpace(c.Gpt.Model) == "" {
		c.Gpt.Model = "moonshot-v1-auto"
	}
	if strings.TrimSpace(c.Gpt.RecognizeModel) == "" {
		c.Gpt.RecognizeModel = "kimi-k2-0905-preview"
	}
	if strings.TrimSpace(c.Gpt.VisionModel) == "" {
		// Moonshot 的视觉模型默认选 32k 上下文足够（OCR 提取的文本通常很短）。
		// 想换成 128k vision 时设 env GPT_VISION_MODEL=moonshot-v1-128k-vision-preview。
		c.Gpt.VisionModel = "moonshot-v1-32k-vision-preview"
	}
	if c.Gpt.QuotaCooldownSeconds <= 0 {
		c.Gpt.QuotaCooldownSeconds = 1800 // 30 分钟
	}
	if c.Gpt.QuotaErrorThreshold <= 0 {
		c.Gpt.QuotaErrorThreshold = 3
	}

	// 自动监听窗口默认值：300 秒沉默触发（5 分钟）、单窗口最多 20 条。
	// 详见 AutoReplyWindowSeconds 字段文档为什么从 60s 改到 300s。
	if c.Group.AutoReplyWindowSeconds <= 0 {
		c.Group.AutoReplyWindowSeconds = 300
	}
	if c.Group.AutoReplyMaxWindowSize <= 0 {
		c.Group.AutoReplyMaxWindowSize = 20
	}

	// 运维通知群默认值：1084352497（项目当前内部运营群）。
	// 优先级：OpsGroupIDs（新）> OpsGroupID（兼容）> 内置默认。
	// 显式配 OpsGroupIDs=[]（空数组）时所有运维通知静默忽略。
	if len(c.Bot.OpsGroupIDs) == 0 {
		if c.Bot.OpsGroupID != 0 {
			c.Bot.OpsGroupIDs = []int64{c.Bot.OpsGroupID}
		} else {
			c.Bot.OpsGroupIDs = []int64{1084352497}
		}
	}
	// 同步 OpsGroupID 到第一项，方便老调用方继续 work（PrimaryOpsGroup 也是这个）
	c.Bot.OpsGroupID = c.Bot.PrimaryOpsGroup()
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}
