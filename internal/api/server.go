// Package api 实现 bot 暴露给 hfut 的"反向 HTTP API"——仅 internal network 用。
//
// 用途（QQ 绑定流程，详见 SKILL.md "绑定 QQ 流程"段）：
//
//   - hfut 在用户点"绑 QQ"时调本服务的 /internal/qq/check-friend 看 QQ 是不是 bot 好友
//   - 若是，hfut 生成验证码，调本服务的 /internal/qq/send-private 让 bot 发私聊验证码
//   - 用户在 app 输入验证码后 hfut 自己校验，bot 不参与这一步
//   - 后续 P2c 阶段还会用到 /internal/qq/send-group 给孤儿账号的提问做回复转发
//
// 鉴权（跟 bot → hfut 方向对称的 service-to-service JWT）：
//
//   - 共享 HS256 secret = conf.Hfut.APIJWTSecret（env HFUT_API_JWT_SECRET）；
//     这跟 hfut 那边 BOT_SERVICE_JWT_SECRET 是同一个值，2 个调用方向共用 1 个 secret
//   - 调用方每次自签 60s 有效期的 JWT 放 X-Service-Token 头
//   - iss 区分调用方向：本服务只接受 "HFUT-Graduation-Project-hfut"（hfut → bot）；
//     bot → hfut 那边的 token (iss=HFUT-Graduation-Project-bot) 即便用同一 secret 签
//     也会被本端拒绝——避免方向混用。
//   - secret 为空时整个 server 不启动（安全降级）
//
// 安全约定：
//
//   - 这个 server **只听 internal network**（docker compose 不要 ports 映射到宿主机）；
//   - 只接 POST，不接 GET——避免缓存 / 浏览器误点；
//   - 错误信息保守：不回任何 NapCat 内部细节给调用方，只给"成功 / 失败 + 业务原因"。
//
// 不用 gin 是因为 P2a 只 3 个端点，引入 gin 依赖不划算。
// 路由直接 ServeMux + 手写 method/路径分流。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"qq_bot/conf"
	"qq_bot/global"
	"qq_bot/logic"
	"qq_bot/utils/client_pool"
	"qq_bot/utils/metrics"
	zaplog "qq_bot/utils/zap"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// hfutToBotIssuer 本端要校验的 JWT iss——只接 hfut 签发的 token。
//
// 跟 utils/hfut/client.go 里 botServiceTokenIssuer 故意不同：
//   - bot → hfut 用 "HFUT-Graduation-Project-bot"
//   - hfut → bot 用 "HFUT-Graduation-Project-hfut"
//
// 即便共享同一个 HS256 secret，方向也不可混用——签错 iss 的 token 会被拒。
const hfutToBotIssuer = "HFUT-Graduation-Project-hfut"

// serviceTokenHeader hfut 把签好的 JWT 放在这个头里发过来。
//
// 故意没用 "Authorization: Bearer xxx"——前者是开放协议规范，但服务间 JWT
// 用专属 header 更明确意图，避免跟其它中间件 / 日志脱敏规则混淆。
const serviceTokenHeader = "X-Service-Token"

// hfutToBotClaims 本端解析 JWT 时关心的 claims；多余字段忽略。
//
// 跟 hfut 那边 util.BotServiceTokenClaims 字段对齐——只看 service + 标准 RegisteredClaims。
type hfutToBotClaims struct {
	Service string `json:"service"`
	jwt.RegisteredClaims
}

// envelope 内部 API 的统一信封——code/message/data 跟 hfut 那边对齐方便调用方统一解析。
type envelope struct {
	Code    int         `json:"code"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// reqCheckFriend 入参：要查询的 QQ 号 + 是否强制刷新缓存
type reqCheckFriend struct {
	QQ      int64 `json:"qq_number"`
	NoCache bool  `json:"no_cache,omitempty"`
}

// reqSendPrivate 入参：目标 QQ + 文本内容
type reqSendPrivate struct {
	QQ   int64  `json:"qq_number"`
	Text string `json:"text"`
}

// reqSendGroup 入参：群号 + 可选 @ 的用户 + 文本内容
//
// QQ 为 0 时退化为不 @ 任何人的纯文本群消息（孤儿账号的"群通告"场景）。
type reqSendGroup struct {
	GroupID int64  `json:"group_id"`
	QQ      int64  `json:"qq_number,omitempty"`
	Text    string `json:"text"`
}

// Start 启动 internal API 服务；阻塞直到 ctx 取消或 server 出错。
//
// 调用约定：必须由 main 在 `go Start(ctx)` 之前 global.Wg.Add(1)，
// 跟其它后台协程同源（避免 wg counter race）。
//
// HFUT_API_JWT_SECRET 为空 / Port <= 0：不启动 server，goroutine 立即返回——log 一行 warn 就行。
func Start(ctx context.Context) {
	defer global.Wg.Done()

	port := conf.Cfg.Internal.Port
	if port <= 0 {
		port = 8090
	}
	secret := strings.TrimSpace(conf.Cfg.Hfut.APIJWTSecret)
	if secret == "" {
		zaplog.Logger.Warnf("协程InternalAPI 不启动：HFUT_API_JWT_SECRET 未配置（hfut 反向调用全部失效，QQ 绑定流程不可用）")
		return
	}
	secretBytes := []byte(secret)

	mux := http.NewServeMux()
	mux.HandleFunc("/internal/qq/check-friend", withJWTAuth(secretBytes, handleCheckFriend))
	mux.HandleFunc("/internal/qq/send-private", withJWTAuth(secretBytes, handleSendPrivate))
	mux.HandleFunc("/internal/qq/send-group", withJWTAuth(secretBytes, handleSendGroup))
	// 运维指标快照——给 hfut /api/v1/admin/metrics 拉取展示
	mux.HandleFunc("/internal/metrics", withJWTAuth(secretBytes, handleMetrics))
	// 健康检查不需要 token，便于 docker compose / hfut 起来后探活
	mux.HandleFunc("/internal/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeOK(w, map[string]string{"status": "ok"})
	})

	addr := fmt.Sprintf(":%d", port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // get_friend_list 偶尔慢点
		IdleTimeout:       120 * time.Second,
	}

	zaplog.Logger.Debugf("协程InternalAPI启动")
	defer zaplog.Logger.Debugf("协程InternalAPI退出")
	zaplog.Logger.Infof("internal API server 已启动 addr=%s（仅 internal network 可达）", addr)

	// 起一个 watcher 监听 ctx，触发 graceful shutdown
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			zaplog.Logger.Warnf("internal API server shutdown 失败: %v", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		zaplog.Logger.Errorf("internal API server ListenAndServe 错误: %v", err)
	}
}

// withJWTAuth 服务间 JWT 中间件——验签 + 检 iss + 检 exp/nbf。
//
// 跟 bot → hfut 方向对称（hfut 那边的 BotServiceAuth 中间件做同样的事）。
//
// 拒绝条件（任一命中 → 401）：
//   - 头缺失 / 格式不对
//   - 签名验证失败（secret 不对）
//   - iss 不是 "HFUT-Graduation-Project-hfut"（防止 bot → hfut 方向的 token 反向使用）
//   - exp 已过 / nbf 未到
func withJWTAuth(secret []byte, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "internal API 仅支持 POST")
			return
		}
		got := strings.TrimSpace(r.Header.Get(serviceTokenHeader))
		if got == "" {
			writeErr(w, http.StatusUnauthorized, "缺少 "+serviceTokenHeader+" 头")
			return
		}

		var claims hfutToBotClaims
		token, err := jwt.ParseWithClaims(got, &claims, func(t *jwt.Token) (interface{}, error) {
			// 强制校验 alg=HS256，避免 alg=none 之类的攻击
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("意外的签名算法 %v", t.Header["alg"])
			}
			return secret, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || token == nil || !token.Valid {
			zaplog.Logger.Warnf("internal API JWT 验签失败 ip=%s ua=%s err=%v",
				r.RemoteAddr, r.UserAgent(), err)
			writeErr(w, http.StatusUnauthorized, "无效的 service token")
			return
		}

		// iss 必须匹配——故意跟 bot → hfut 方向 (HFUT-Graduation-Project-bot) 不同，
		// 防止把 bot 那边自签的 token 拿来反向调用本服务（虽然 secret 共享，但 iss 不同）
		if claims.Issuer != hfutToBotIssuer {
			zaplog.Logger.Warnf("internal API JWT iss 不匹配 got=%q want=%q service=%q",
				claims.Issuer, hfutToBotIssuer, claims.Service)
			writeErr(w, http.StatusUnauthorized, "iss 不匹配")
			return
		}

		next(w, r)
	}
}

// handleCheckFriend POST /internal/qq/check-friend
//
// Body：reqCheckFriend
// 返回：{ is_friend: bool }
func handleCheckFriend(w http.ResponseWriter, r *http.Request) {
	var req reqCheckFriend
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if req.QQ <= 0 {
		writeErr(w, http.StatusBadRequest, "qq_number 必填且为正整数")
		return
	}
	cli := client_pool.NewClientPool()
	isFriend, err := logic.CheckFriend(cli, req.QQ, req.NoCache)
	if err != nil {
		zaplog.Logger.Errorf("internal check-friend 失败 qq=%d: %v", req.QQ, err)
		writeErr(w, http.StatusBadGateway, "查询好友列表失败")
		return
	}
	writeOK(w, map[string]bool{"is_friend": isFriend})
}

// handleSendPrivate POST /internal/qq/send-private
//
// Body：reqSendPrivate
// 返回：{ message_id }
//
// 错误：404=对方不是好友（NapCat retcode 100，转 404 让 hfut 区分对待）；502=NapCat 不通
func handleSendPrivate(w http.ResponseWriter, r *http.Request) {
	var req reqSendPrivate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if req.QQ <= 0 {
		writeErr(w, http.StatusBadRequest, "qq_number 必填且为正整数")
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, "text 不能为空")
		return
	}
	cli := client_pool.NewClientPool()
	mid, err := logic.SendPrivateText(cli, req.QQ, req.Text)
	if err != nil {
		// NapCat 拒发（如 retcode 100 = 不是好友 / 临时会话不允许）→ 404 语义
		// 其它 NapCat 不可达 → 502
		// 这里简单把所有错都当 502 让 hfut 那边重试 / 提示用户加好友；
		// 后期需要细分时可以解析 err 字符串拿 retcode。
		zaplog.Logger.Errorf("internal send-private 失败 qq=%d: %v", req.QQ, err)
		writeErr(w, http.StatusBadGateway, "发送私聊失败（请检查 bot 与目标 QQ 是否好友）")
		return
	}
	writeOK(w, map[string]int64{"message_id": mid})
}

// handleSendGroup POST /internal/qq/send-group
//
// Body：reqSendGroup
//
// QQ != 0 时发"@user + text"组合；QQ == 0 时发纯文本群消息。
func handleSendGroup(w http.ResponseWriter, r *http.Request) {
	var req reqSendGroup
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if req.GroupID <= 0 {
		writeErr(w, http.StatusBadRequest, "group_id 必填且为正整数")
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, "text 不能为空")
		return
	}
	cli := client_pool.NewClientPool()

	var err error
	if req.QQ > 0 {
		err = logic.SendGroupAtText(cli, req.GroupID, req.QQ, req.Text)
	} else {
		err = logic.SendGroupText(cli, req.GroupID, req.Text)
	}
	if err != nil {
		zaplog.Logger.Errorf("internal send-group 失败 group=%d qq=%d: %v", req.GroupID, req.QQ, err)
		writeErr(w, http.StatusBadGateway, "发送群消息失败")
		return
	}
	writeOK(w, map[string]string{"status": "ok"})
}

// handleMetrics POST /internal/metrics —— 返回 bot 进程级运行指标。
//
// 内部网络可达；hfut 端 admin /api/v1/admin/metrics 拉一次后合并展示。
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeOK(w, metrics.Snapshot())
}

// writeOK 200 + 信封 code=200。
func writeOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(envelope{Code: 200, Message: "ok", Data: data})
}

// writeErr 非 2xx + 信封 code=httpStatus。
//
// 信封 code 跟 HTTP status 对齐方便调用方解析；message 给人类看，不暴露内部细节。
func writeErr(w http.ResponseWriter, httpStatus int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(envelope{Code: httpStatus, Message: msg})
}
