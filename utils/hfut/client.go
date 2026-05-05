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
	"net/http"
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
			Timeout: 30 * time.Second, // bot 路径的所有调用都该几秒内回，30s 足够兜底
		},
	}, nil
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
type PublishGoodReq struct {
	UserID     uint     `json:"user_id"`
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Category   int16    `json:"category"`   // 1=二手 2=有偿求助/AA活动
	Negotiable bool     `json:"negotiable"` // true 时 Price 被忽略，前端展示"面议"
	Price      int      `json:"price"`      // 单位：分
	Location   string   `json:"location"`
	Images     []string `json:"images"`
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

// UpsertQQChild idempotent 创建/复用 QQ 旗下账号。
//
// hfut 端没配 group → 学校映射时返回 ErrGroupNoSchool。
func (c *Client) UpsertQQChild(ctx context.Context, qqNumber string, groupID int64, nickname string) (*UpsertQQChildResp, error) {
	body := map[string]interface{}{
		"qq_number": qqNumber,
		"group_id":  groupID,
		"nickname":  nickname,
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
