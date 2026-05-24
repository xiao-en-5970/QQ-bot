// Package hfut 封装对 hfut 后端 /api/v1/bot/* 的调用——给 auto_reply 路径用。
//
// 鉴权（service-to-service JWT，0 维护数据库 token）：
//   - bot 跟 hfut 共享一个 HS256 secret（env HFUT_API_JWT_SECRET）
//   - 每次请求 bot 用 secret 自签一个 60s 有效期的 JWT 放 X-Bot-Service-Token 头
//   - hfut 那边用同一 secret 验签 + 检 exp + 检 iss，验证通过即放行，**不查任何 DB**
//   - 旋转 secret 即可让旧 token 全部失效；不需要 admin 创建/作废 token 的接口
//
// 错误：
//   - hfut 业务错（自定义 code）会被解出，HTTPStatus + 业务 message 一起塞进 ClientError
//   - "群没配学校"是 bot 路径里的特例（status 404 + 业务错），用 ErrGroupNoSchool 标识，
//     上游识别这个错时应该静默忽略（不在群里给任何提示）
//   - 网络错 / 5xx 走 fmt.Errorf，让上游决定是否重试 / 群里告知用户"系统繁忙"
package hfut

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 服务间互信的请求头名（跟 hfut middleware.botServiceTokenHeader 对齐）
const headerServiceToken = "X-Bot-Service-Token"

// botServiceTokenIssuer service token 专属 iss；跟 hfut 那边 ParseBotServiceToken 校验的值对齐。
//
// 区别于 user 登录的 JWT iss（"HFUT-Graduation-Project"），防止两类 token 误用。
const botServiceTokenIssuer = "HFUT-Graduation-Project-bot"

// botServiceTokenTTL bot 自签 token 的有效期。
//
// 60s 已经够穿越 client→hfut 的任何延迟；又短到即便泄漏也只有 1 分钟攻击窗。
// 时钟漂移容忍：bot 跟 hfut 间 30s 偏差也不会让 token 立刻 invalid。
const botServiceTokenTTL = 60 * time.Second

// botServiceClaims bot 自签 token 的 JWT claims。
//
// 跟 hfut 那边 util.BotServiceTokenClaims 字段对齐——只有 service 名 + 标准 RegisteredClaims，
// 不带 owner_user_id（bot 共享 secret 签发概念上没"owner admin"）。
type botServiceClaims struct {
	Service string `json:"service"`
	jwt.RegisteredClaims
}

// Client 是封装好的 hfut 后端调用客户端。
//
// 单例使用：在 main.go 启动时按 conf 创建一份，整个 bot 共用；
// 内部 http.Client 是连接池友好的（复用 transport），方法本身并发安全。
type Client struct {
	baseURL     string // 例 "https://hfut-api.xiaoen.xyz"，不带 /api/v1（构造时校验）
	jwtSecret   []byte // 共享的 HS256 签名 secret
	serviceName string // 写进 token claims.service 字段方便 hfut 端审计 / log
	httpClient  *http.Client
}

// NewClient 创建 hfut 客户端。baseURL 末尾的 / 会被剥掉；空 URL / secret 会返回错误。
//
// serviceName 留空则默认 "qq-bot"——hfut 端只用作 log 标识，不参与鉴权。
func NewClient(baseURL, jwtSecret, serviceName string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("hfut.NewClient: baseURL 不能为空")
	}
	if strings.TrimSpace(jwtSecret) == "" {
		return nil, errors.New("hfut.NewClient: jwtSecret 不能为空")
	}
	if strings.TrimSpace(serviceName) == "" {
		serviceName = "qq-bot"
	}
	return &Client{
		baseURL:     baseURL,
		jwtSecret:   []byte(jwtSecret),
		serviceName: serviceName,
		httpClient: &http.Client{
			Timeout:   30 * time.Second, // bot 路径的所有调用都该几秒内回，30s 足够兜底
			Transport: newHfutTransport(),
		},
	}, nil
}

// newHfutTransport 给 hfut.Client 用的 http.Transport，针对"长跑、跨公网、间歇调用"
// 场景调优过：
//
//   - **MaxIdleConnsPerHost=16**：默认 http.DefaultTransport 只给 2，bot 同时并发
//     的上架 / 识别 / poll / 检索调用一旦超过 2，就会反复建立新 TCP（每次 TLS 握手
//     ~200-1000ms），引起莫名其妙的"几秒延迟"。
//
//   - **IdleConnTimeout=45s**：默认 90s，但跨 NAT / Cloudflare / 中间防火墙时，对方
//     的 TCP 表项通常在 60s 左右过期。bot 这边以为连接还活着继续发包，对方早就清
//     掉了连接表 —— 包石沉大海，直到客户端 ctx 超时（这是真实日志里 "context
//     deadline exceeded" 的常见原因）。设成 45s 比对端表项早过期，让 bot 主动关掉
//     久不用的连接、下次重新握手。
//
//   - **ForceAttemptHTTP2=true**：HTTP/2 在单条 TCP 上多路复用所有请求，对 bot 这种
//     并发量小、连接复用率要求高的场景特别合适。
//
//   - **DialContext 5s + KeepAlive 30s**：建连快速失败，避免请求级 ctx 被 dial 吃掉
//     大半。
//
//   - **TLSHandshakeTimeout=5s**：握手卡死不至于拖到整个 30s 兜底超时。
func newHfutTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       45 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}

// signToken 临时签一个 60s 有效期的 service JWT。
//
// 每次请求都重新签——HS256 + 短 claims 几个微秒，远比"维护缓存 + 处理过期"简单。
func (c *Client) signToken() (string, error) {
	now := time.Now()
	jti, err := newJTI()
	if err != nil {
		return "", fmt.Errorf("生成 jti 失败: %w", err)
	}
	claims := botServiceClaims{
		Service: c.serviceName,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Issuer:    botServiceTokenIssuer,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(botServiceTokenTTL)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(c.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("签名 service JWT 失败: %w", err)
	}
	return signed, nil
}

// newJTI 16 字节随机 hex（128bit 熵），让 hfut 那边即便 log jti 也能区分每次调用。
func newJTI() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ClientError 表达"hfut 返回了非 2xx 业务错"——区别于网络错。
type ClientError struct {
	HTTPStatus int             // HTTP 状态码
	BizCode    int             // hfut 信封里的 code
	Message    string          // hfut 信封里的 message
	RawData    json.RawMessage // 业务错误时 hfut 回的 data 字段（如 409 重复时含 existing_id）；上层按需解析
}

func (e *ClientError) Error() string {
	return fmt.Sprintf("hfut: HTTP %d / code=%d / %s", e.HTTPStatus, e.BizCode, e.Message)
}

// ErrGroupNoSchool hfut 那边返回 404 + "群没配学校"时给上游的 sentinel 错误。
//
// auto_reply 看到这个错应该完全静默——本群压根没在 schools.qq_groups 注册过，
// 不是 bot 该处理的群。
var ErrGroupNoSchool = errors.New("hfut: 当前群未配置学校，bot 应忽略本群")

// DuplicateGoodInfo 触发去重保护时 hfut 返回的"已存在商品"信息。
//
// PublishGood 命中去重时 err 被包成 *DuplicateGoodInfo（也实现 error 接口），
// 上层用 errors.As 识别后可以拿 ExistingID / ExistingTitle 给用户友好提示。
type DuplicateGoodInfo struct {
	ExistingID    uint
	ExistingTitle string
}

func (e *DuplicateGoodInfo) Error() string {
	return fmt.Sprintf("hfut: 检测到 7 天内已有同款商品 (id=%d, title=%q)", e.ExistingID, e.ExistingTitle)
}

// =============================================================================
// 请求 / 响应类型（跟 hfut 那边的 service.Bot* 结构对齐）
// =============================================================================

// hfutEnvelope 是 hfut 后端统一的响应信封。
//
// 字段都用 json.RawMessage 保留 data，让具体接口自己反序列化。
type hfutEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// UpsertQQChildResp upsert 旗下账号的返回。
type UpsertQQChildResp struct {
	UserID   uint   `json:"user_id"`
	Created  bool   `json:"created"`
	SchoolID uint   `json:"school_id"`
	Username string `json:"username"`
	Nickname string `json:"nickname,omitempty"`
}

// PublishGoodReq 上架商品入参。
//
// GroupID：bot 触发本次上架时来源 QQ 群号；后端持久化到 goods.created_in_group_id，
// 后续孤儿商品 "请求下架" 优先用它定位 "在哪个群 @ 卖家"。
// <=0 时不传给后端（兼容非 bot 路径，落库为 NULL）。
//
// Stock：库存数量；<=0 时 hfut 后端按 1 兜底。bot 识别到用户明说"出 N 个"才填，
// 没明说时不传（即 0）。
type PublishGoodReq struct {
	UserID     uint     `json:"user_id"`
	GroupID    int64    `json:"group_id,omitempty"` // bot 上架时来源 QQ 群号；<=0 不填
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Category   int16    `json:"category"`        // 1=二手 2=有偿求助/AA活动
	Negotiable bool     `json:"negotiable"`      // true 时 Price 被忽略，前端展示"面议"
	Bargain    bool     `json:"bargain"`         // 可刀
	Price      int      `json:"price"`           // 单位：分
	Stock      int      `json:"stock,omitempty"` // 库存数量；<=0 时后端按 1 兜底
	Location   string   `json:"location"`
	Images     []string `json:"images"`
	// BotMessageIDs 本次上架涉及的全部 QQ message_id 合集（外层消息 ID + Kimi 给出的
	// image_message_ids / source_message_ids），hfut 落到 goods.bot_message_ids；后续
	// 用户 reply 自己之前的上架消息说"已出"时，bot 用 reply.id 调
	// LookupActiveGoodByMessageID 直接定位 good，跳过模糊匹配 + 消歧反问。
	BotMessageIDs []int64 `json:"bot_message_ids,omitempty"`
	// IsBatch=true 表示这是"合并聊天记录批量上架"的商品：title 是多个商品名用
	// 逗号串联、价格统一面议、所有图片都属于这个商品。hfut 落到 goods.is_batch
	// 字段，app 前端按此显示"批量上架"tag。
	IsBatch bool `json:"is_batch,omitempty"`
	// Force=true 时后端跳过 title 重复检查。给 bot 反问"重复上架"路径用——
	// 用户在群里选择"1 重复上架"后，bot 用 Force=true 重发原 request 强制创建。
	Force bool `json:"force,omitempty"`
}

// PublishGoodResp 上架商品返回。
type PublishGoodResp struct {
	GoodID uint `json:"good_id"`
}

// ActiveGood 在售商品的精简表示。
type ActiveGood struct {
	ID         uint   `json:"id"`
	Title      string `json:"title"`
	Negotiable bool   `json:"negotiable"`
	Price      int    `json:"price"`
	Category   int16  `json:"category"`
	CreatedAt  string `json:"created_at"`
}

// PublishArticleReq 创建提问/回答入参。
type PublishArticleReq struct {
	UserID   uint     `json:"user_id"`
	Type     int      `json:"type"` // 2=提问 3=回答
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	ParentID *int     `json:"parent_id"`
	Images   []string `json:"images,omitempty"`
}

// PublishArticleResp 创建文章返回。
type PublishArticleResp struct {
	ArticleID uint `json:"article_id"`
}

// OpenQuestion 群内开放提问的精简表示。
type OpenQuestion struct {
	ID        uint   `json:"id"`
	UserID    uint   `json:"user_id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

// =============================================================================
// 业务方法（跟 hfut /api/v1/bot/* 路由 1:1 对应）
// =============================================================================

// QQChildEntry 旗下号同步任务遍历列表里单条用户的字段集。
type QQChildEntry struct {
	UserID           uint   `json:"user_id"`
	QQNumber         string `json:"qq_number"`
	Nickname         string `json:"nickname,omitempty"`
	QQAvatarURL      string `json:"qq_avatar_url,omitempty"`
	CreatedInGroupID int64  `json:"created_in_group_id,omitempty"`
}

// ListQQChildrenResp 分页响应。NextCursor == 0 表示已经到末尾。
type ListQQChildrenResp struct {
	List       []QQChildEntry `json:"list"`
	NextCursor uint           `json:"next_cursor"`
	Limit      int            `json:"limit"`
}

// ListQQChildren 按 user_id ASC 分页拉所有旗下号——给 nickname 同步 scheduler 用。
//
// 用法：
//
//	cursor := uint(0)
//	for {
//	    resp, err := cli.ListQQChildren(ctx, cursor, 200)
//	    ...
//	    if resp.NextCursor == 0 { break }
//	    cursor = resp.NextCursor
//	}
func (c *Client) ListQQChildren(ctx context.Context, cursor uint, limit int) (*ListQQChildrenResp, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	path := "/api/v1/bot/users/qq-children?cursor=" + strconv.FormatUint(uint64(cursor), 10) +
		"&limit=" + strconv.Itoa(limit)
	var out ListQQChildrenResp
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QQSyncForceAtResp /api/v1/bot/qq-sync/pending 的返回。
type QQSyncForceAtResp struct {
	ForceAt int64 `json:"force_at"` // admin "立即同步"按钮按下时的 unix-ms 时间戳；0 = 未按过
}

// GetQQSyncForceAt 拉 admin 触发的"立即同步"信号位时间戳。
//
// bot scheduler 自己存 last_seen；poll 出 force_at 比 last_seen 新 → 立即跑一轮。
func (c *Client) GetQQSyncForceAt(ctx context.Context) (int64, error) {
	var out QQSyncForceAtResp
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/bot/qq-sync/pending", nil, &out); err != nil {
		return 0, err
	}
	return out.ForceAt, nil
}

// GetRuntimeConfig 拉 hfut 后端维护的"bot 运行时配置"——KV 形态返回。
//
// 已知 key（详见 hfut dao/bot_runtime_config.go）：
//   - auto_reply_whitelist  jsonb int64 数组
//   - ops_group_ids         jsonb int64 数组
//   - silent_mode           jsonb bool
//
// 未配置的 key 不会出现在返回 map 里。调用方负责按 key 解码 + 兜底 env 默认值。
//
// 失败时返回 (nil, err)；调用方应保留上次的内存配置（典型实现见 logic/runtime_config_sync.go）。
func (c *Client) GetRuntimeConfig(ctx context.Context) (map[string]json.RawMessage, error) {
	// hfut 的 doJSON 已经剥掉 envelope，把 data 字段直接 Unmarshal 到 out
	out := make(map[string]json.RawMessage)
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/bot/runtime-config", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpsertQQChild idempotent 创建/复用 QQ 旗下账号。
//
// hfut 端没配 group → 学校映射时返回 ErrGroupNoSchool。
//
// nickname / avatarURL 都是"展示信息"——bot 从 QQ 群消息 sender / get_group_member_info 拿到
// 后透传过来，hfut 端按"最新覆盖"写到 users.nickname / users.qq_avatar_url。空字符串 = 不动。
func (c *Client) UpsertQQChild(ctx context.Context, qqNumber string, groupID int64, nickname, avatarURL string) (*UpsertQQChildResp, error) {
	body := map[string]interface{}{
		"qq_number":  qqNumber,
		"group_id":   groupID,
		"nickname":   nickname,
		"avatar_url": avatarURL,
	}
	var out UpsertQQChildResp
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/bot/users/qq-child", body, &out)
	if err != nil {
		// "群没配学校"在 hfut 用 status 404 + 特定 message 表达，转成 sentinel 让上游单独处理
		var ce *ClientError
		if errors.As(err, &ce) && ce.HTTPStatus == http.StatusNotFound {
			return nil, ErrGroupNoSchool
		}
		return nil, err
	}
	return &out, nil
}

// PublishGood 上架商品。返回 good_id。
//
// 触发去重保护时返回 *DuplicateGoodInfo（实现了 error 接口）——上层用 errors.As 识别，
// 可以拿到已存在商品的 id + title 做友好提示。
func (c *Client) PublishGood(ctx context.Context, req PublishGoodReq) (*PublishGoodResp, error) {
	var out PublishGoodResp
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/bot/goods", req, &out)
	if err != nil {
		// hfut 返 409 + biz code 4090 表示重复，data 里夹 existing_id / existing_title
		var ce *ClientError
		if errors.As(err, &ce) && ce.HTTPStatus == http.StatusConflict && ce.BizCode == 4090 {
			info := &DuplicateGoodInfo{}
			if len(ce.RawData) > 0 {
				var raw struct {
					ExistingID    uint   `json:"existing_id"`
					ExistingTitle string `json:"existing_title"`
				}
				_ = json.Unmarshal(ce.RawData, &raw)
				info.ExistingID = raw.ExistingID
				info.ExistingTitle = raw.ExistingTitle
			}
			return nil, info
		}
		return nil, err
	}
	return &out, nil
}

// OffShelfGood 下架商品；callerUserID 必须是 owner 或 owner 的主账号。
func (c *Client) OffShelfGood(ctx context.Context, goodID uint, callerUserID uint) error {
	body := map[string]interface{}{"user_id": callerUserID}
	return c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/bot/goods/%d/off-shelf", goodID), body, nil)
}

// ListActiveGoods 列出某用户当前在售商品（消歧 / 去重用）。
func (c *Client) ListActiveGoods(ctx context.Context, userID uint, limit int) ([]*ActiveGood, error) {
	if limit <= 0 {
		limit = 20
	}
	path := fmt.Sprintf("/api/v1/bot/users/%d/goods/active?limit=%d", userID, limit)
	var out struct {
		List  []*ActiveGood `json:"list"`
		Total int           `json:"total"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// SeekGoodMatch 「收××」检索单条（与 hfut BotSeekGoodMatch 对齐）。
//
// 字段含义：
//   - SellerQQ 卖家的可联系 QQ 号；hfut 端 botContactQQForUser 按"卖家是 QQ 旗下号
//     → 用 child.qq_number；卖家是 app 主账号且已绑 QQ → 用挂在其名下的 child
//     的 qq_number；都没有 → 空"的规则填充。bot 端非空时直接给"加 QQ xxx"提示。
//   - OrphanSeller 标识卖家是否孤儿 QQ 旗下账号（保留作为元信息，**不再**决定
//     是否给 QQ —— 由 SellerQQ 非空判断）。
//   - Images 是 hfut OSS 永久 URL；bot 命中后会用 send_group_forward_msg 把图片
//     连同商品文字 + 联系方式打包成"聊天记录"发到群里（不直接发 QQ 临时图片，
//     因 QQ 聊天记录会过期，OSS 持久）
type SeekGoodMatch struct {
	ID           uint     `json:"id"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	Images       []string `json:"images,omitempty"`
	Location     string   `json:"location,omitempty"`
	CreatedAt    string   `json:"created_at"`
	Price        int      `json:"price"`
	Negotiable   bool     `json:"negotiable"`
	SellerQQ     string   `json:"seller_qq,omitempty"`
	OrphanSeller bool     `json:"orphan_seller"`
}

// SeekerMatch 「出××」反查求购方的检索单条（与 hfut BotSeekerMatch 对齐）。
//
// 跟 SeekGoodMatch 对称——用户在群里"出 X"上架成功后，bot 用本结构反查谁在求 X，
// 把求购者列表打包成"聊天记录"发到群里告诉卖家"以下人可能需要"。
//
// 求物品场景没有 Images / Location（用户求购一般不上传图），用 Price>0 表"愿付酬劳"。
type SeekerMatch struct {
	ID           uint   `json:"id"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	CreatedAt    string `json:"created_at"`
	Price        int    `json:"price"`
	Negotiable   bool   `json:"negotiable"`
	SeekerQQ     string `json:"seeker_qq,omitempty"`
	OrphanSeeker bool   `json:"orphan_seeker"`
}

// SearchGoodsSeek GET /api/v1/bot/groups/:group_id/goods/seek
// LookupActiveGoodByMessageID 用 QQ message_id 反查"该用户名下、bot_message_ids 数组
// 含此 ID 且仍在售"的商品；典型用例是用户 reply 自己之前的上架消息说"已出"。
//
// 命中返回 *ActiveGood；未命中返回 (nil, nil)——hfut 端为了让 bot 平滑降级，未命中
// 用 HTTP 200 + data.good=null 表达，**不**当错误返回。所以这里也以 (nil, nil) 兜底。
func (c *Client) LookupActiveGoodByMessageID(ctx context.Context, userID uint, msgID int64) (*ActiveGood, error) {
	if msgID == 0 {
		return nil, nil
	}
	path := "/api/v1/bot/users/" + strconv.FormatUint(uint64(userID), 10) +
		"/goods/by-msg/" + strconv.FormatInt(msgID, 10)
	var out struct {
		Good *ActiveGood `json:"good"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Good, nil
}

// SearchActiveSeeks 反查"谁在求 X" —— GET /api/v1/bot/groups/:group_id/seekers/by-keyword
//
// 跟 SearchGoodsSeek 对称：用户在群里"出 X" 上架成功后调，拿到最多 N 条求物品记录
// （goods_category=2、status=正常、good_status=在售）。命中即把列表打包成合并转发
// 卡片发到群里。
func (c *Client) SearchActiveSeeks(ctx context.Context, groupID int64, q string, limit int) ([]SeekerMatch, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}
	path := fmt.Sprintf("/api/v1/bot/groups/%d/seekers/by-keyword?q=%s&limit=%d",
		groupID, url.QueryEscape(strings.TrimSpace(q)), limit)
	var out struct {
		List  []SeekerMatch `json:"list"`
		Total int           `json:"total"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		var ce *ClientError
		if errors.As(err, &ce) && ce.HTTPStatus == http.StatusNotFound {
			return nil, ErrGroupNoSchool
		}
		return nil, err
	}
	return out.List, nil
}

func (c *Client) SearchGoodsSeek(ctx context.Context, groupID int64, q string, limit int) ([]SeekGoodMatch, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}
	path := fmt.Sprintf("/api/v1/bot/groups/%d/goods/seek?q=%s&limit=%d",
		groupID, url.QueryEscape(strings.TrimSpace(q)), limit)
	var out struct {
		List  []SeekGoodMatch `json:"list"`
		Total int             `json:"total"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		var ce *ClientError
		if errors.As(err, &ce) && ce.HTTPStatus == http.StatusNotFound {
			return nil, ErrGroupNoSchool
		}
		return nil, err
	}
	return out.List, nil
}

// PublishArticle 创建提问/回答。
func (c *Client) PublishArticle(ctx context.Context, req PublishArticleReq) (*PublishArticleResp, error) {
	var out PublishArticleResp
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/bot/articles", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CloseArticle 关闭提问；callerUserID 必须是作者或其主账号。
func (c *Client) CloseArticle(ctx context.Context, articleID uint, callerUserID uint) error {
	body := map[string]interface{}{"user_id": callerUserID}
	return c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/bot/articles/%d/close", articleID), body, nil)
}

// AdminSQLResp /api/v1/bot/admin/sql 的 data 字段反序列化。
//
// Rows 是 row × col 二维数组；每个 cell 是 driver scan 后再 normalize 过的 JSON 友好值
// （time.Time → RFC3339 字符串 / []byte → 字符串 / 原始数值类型保留）。
//
// ColumnTypes 是 PG driver 给的 DatabaseTypeName，比如 "INT8" / "VARCHAR" / "TIMESTAMPTZ"，
// 仅作元信息——bot 那边把结果格式化给运维群时只用 Columns + Rows 就够了。
type AdminSQLResp struct {
	Columns     []string `json:"columns"`
	ColumnTypes []string `json:"column_types"`
	Rows        [][]any  `json:"rows"`
	RowCount    int      `json:"row_count"`
	ElapsedMs   int64    `json:"elapsed_ms"`
}

// RunAdminSQL 在 hfut 后端跑一段只读 SQL（仅 select / with / explain / show / values）。
//
// limit ≤ 0 时由后端兜底为 200；最大 1000。SQL 静态检查 + PG 事务 READ ONLY 双保险，
// 详见 hfut controller/bot_admin_sql.go。
//
// 这个方法**只该被运维查询路径**（logic/ops_query.go）调用——给 LLM 工具用，
// 调用方需要先确认上下文是 ops 群消息，再把结果发回群里。普通业务路径不能用。
func (c *Client) RunAdminSQL(ctx context.Context, sqlStr string, limit int) (*AdminSQLResp, error) {
	body := map[string]any{"sql": sqlStr}
	if limit > 0 {
		body["limit"] = limit
	}
	var out AdminSQLResp
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/bot/admin/sql", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UploadImageResp 转存图片成功后 hfut 返回的永久 URL。
type UploadImageResp struct {
	URL string `json:"url"`
}

// UploadImage 把一张图（NapCat 临时 URL 下载后的二进制）上传到 hfut OSS，返回永久 URL。
//
// 参数：
//
//	userID    图归属的用户 id（旗下账号 / 主账号）；hfut 会存到 user/{userID}/bot/...
//	data      图片二进制
//	filename  扩展名靠它推断；NapCat 不一定给得出像样的 filename，调用方可以
//	          fallback "img.jpg"——hfut 端只看扩展名，jpg/jpeg/png/gif/webp 才接受
//
// 错误：
//   - hfut 拒收（4xx）→ ClientError，上层 log 后跳过这张图，不重试
//   - 网络错 → 通用 error，同上
//   - 任何错误都不应该让上层 panic，上层 mirror helper 应当 skip 这张图继续处理下一张
func (c *Client) UploadImage(ctx context.Context, userID uint, data []byte, filename string) (*UploadImageResp, error) {
	if len(data) == 0 {
		return nil, errors.New("hfut.UploadImage: data 不能为空")
	}
	if filename == "" {
		filename = "img.jpg"
	}

	// 构造 multipart body
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("user_id", strconv.FormatUint(uint64(userID), 10)); err != nil {
		return nil, fmt.Errorf("hfut.UploadImage: 写 user_id 字段失败: %w", err)
	}
	fileWriter, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("hfut.UploadImage: 创建 file 字段失败: %w", err)
	}
	if _, err := fileWriter.Write(data); err != nil {
		return nil, fmt.Errorf("hfut.UploadImage: 写图片二进制失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("hfut.UploadImage: 关闭 multipart 失败: %w", err)
	}

	url := c.baseURL + "/api/v1/bot/images"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, fmt.Errorf("hfut.UploadImage: 创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	token, err := c.signToken()
	if err != nil {
		return nil, fmt.Errorf("hfut.UploadImage: 签 service token 失败: %w", err)
	}
	req.Header.Set(headerServiceToken, token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hfut.UploadImage: 请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hfut.UploadImage: 读响应失败: %w", err)
	}

	var env hfutEnvelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("hfut.UploadImage: HTTP %d 且信封解析失败: %s",
				resp.StatusCode, truncate(string(respBody), 200))
		}
		return nil, fmt.Errorf("hfut.UploadImage: 信封解析失败: %w (raw=%s)",
			err, truncate(string(respBody), 200))
	}
	if env.Code != 200 || resp.StatusCode/100 != 2 {
		return nil, &ClientError{
			HTTPStatus: resp.StatusCode,
			BizCode:    env.Code,
			Message:    env.Message,
			RawData:    env.Data,
		}
	}

	var out UploadImageResp
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &out); err != nil {
			return nil, fmt.Errorf("hfut.UploadImage: data 解析失败: %w", err)
		}
	}
	if out.URL == "" {
		return nil, errors.New("hfut.UploadImage: hfut 返回的 url 为空")
	}
	return &out, nil
}

// ListOpenQuestions 列群内开放提问（answer 时定 parent_id 用）。
func (c *Client) ListOpenQuestions(ctx context.Context, groupID int64, limit int) ([]*OpenQuestion, error) {
	if limit <= 0 {
		limit = 20
	}
	path := fmt.Sprintf("/api/v1/bot/groups/%d/articles/open?limit=%d", groupID, limit)
	var out struct {
		List  []*OpenQuestion `json:"list"`
		Total int             `json:"total"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// =============================================================================
// 内部 HTTP helpers
// =============================================================================

// doJSON 发送 JSON 请求并解析 hfut 信封；out 为 nil 时只校验信封 code 不解 data。
//
// 失败语义：
//   - HTTP 非 2xx + 信封解出 → ClientError（HTTP/biz code/msg 全带）
//   - HTTP 非 2xx + 信封解不出 → 通用 error
//   - HTTP 2xx 但 信封 code != 200 → ClientError（HTTPStatus 仍写 200 方便日志）
//   - 网络错 → 通用 error
func (c *Client) doJSON(ctx context.Context, method, path string, reqBody interface{}, out interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("hfut: 请求 body 序列化失败: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("hfut: 创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 每次请求自签 60s JWT 当 service token；hfut 端共享 secret 验签即放行
	token, err := c.signToken()
	if err != nil {
		return fmt.Errorf("hfut: 签 service token 失败: %w", err)
	}
	req.Header.Set(headerServiceToken, token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("hfut: 请求失败 (%s %s): %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("hfut: 读响应失败: %w", err)
	}

	var env hfutEnvelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		// 信封解不出，但 HTTP 又不 OK——把 raw body 截断 200 字带出去方便排查
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("hfut: HTTP %d 且信封解析失败: %s", resp.StatusCode, truncate(string(respBody), 200))
		}
		return fmt.Errorf("hfut: 信封解析失败: %w (raw=%s)", err, truncate(string(respBody), 200))
	}

	// 业务码不为 200 视作业务错；HTTP 非 2xx 同样视作错。
	// 错误情况下也把 data 字段一起带回去——某些业务错会在 data 里夹元信息（如 409 去重时
	// 的 existing_id / existing_title），上层按 BizCode 自己决定要不要解析。
	if env.Code != 200 || resp.StatusCode/100 != 2 {
		return &ClientError{
			HTTPStatus: resp.StatusCode,
			BizCode:    env.Code,
			Message:    env.Message,
			RawData:    env.Data,
		}
	}

	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("hfut: data 解析失败: %w (raw=%s)", err, truncate(string(env.Data), 200))
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
