// Package kimi 的 schema_cache.go 维护"PostgreSQL 实时 schema 摘要"——
//
// 背景：ops_sql.go 的 opsSQLSchemaSummary 是写死常量，每次给 schema 增删字段都得手动
// 同步注释，否则 LLM 会用过时列名生成 SQL 进而 PG 报错。本模块通过周期性拉取 hfut
// information_schema.columns 得到"最新表 + 列 + 类型"，注入到 GenerateOpsSQL 的
// system prompt 里，让 LLM 看到的 schema 永远不会偏离生产库。
//
// 设计：
//   - 启动时同步拉一次（失败不阻塞，let GenerateOpsSQL 退化到硬编码 schema）
//   - 后台每 1 小时刷新一次（schema 变化频率远低于这个，1h 缓存够用）
//   - 拉取走 hfut.RunAdminSQL（已存在的接口，不需要额外路由），SELECT 在白名单内
//   - 跟硬编码 opsSQLSchemaSummary **共存**：硬编码保留业务语义注释（"status=1 表示在售"
//     等 information_schema 查不到的语义），实时 schema 追加到 prompt 后面补全列名

package kimi

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	zaplog "qq_bot/utils/zap"
)

// LiveSchemaFetcher 抽象 hfut client 的实时 schema 拉取——避免循环 import，
// 由 main.go 在启动时把 hfut.Client 适配进来。
type LiveSchemaFetcher interface {
	FetchPublicSchema(ctx context.Context) (string, error)
}

// schemaCache 全局缓存：原子读写让 GenerateOpsSQL 调用方无锁拿到当前快照。
//
// value 是格式化好的"表 / 列 / 类型"文本，可以直接拼到 prompt 里。
// 缓存空时（首次拉取尚未完成 / 拉取一直失败）GenerateOpsSQL 走纯硬编码 schema。
var liveSchemaCache atomic.Pointer[string]

// schemaFetcher 由 StartSchemaCacheSync 注入；nil 时 sync 协程直接退出。
var schemaFetcher atomic.Value // LiveSchemaFetcher

// schemaRefreshInterval 后台刷新周期——schema 变化频率以"周/月"为单位，1h 已经过分密了；
// 选 1h 是为了让运维改完 schema 后最多 1h 内 LLM 看到新结构。
const schemaRefreshInterval = 1 * time.Hour

// loadLiveSchema 给 ops_sql.go 调用——读取当前缓存。空字符串表示"未就绪"。
func loadLiveSchema() string {
	p := liveSchemaCache.Load()
	if p == nil {
		return ""
	}
	return *p
}

// StartSchemaCacheSync 注入 fetcher + 启动后台刷新协程。
//
// 调用约定：main.go 在 hfut.Client 初始化好之后立即调用一次，传入 fetcher。
// ctx 取 main 的根 ctx——cancel 时本协程同步退出。
//
// 启动语义：
//   - 同步先拉一次（最多等 30s），让 bot 一启动 LLM 就能拿到最新 schema
//   - 拉取失败 → log warn，缓存留空，由 GenerateOpsSQL 退化到硬编码 schema
//   - 之后 goroutine 每 1h 重拉，刷新失败也只 log warn 不清空旧缓存（旧版本好过没版本）
func StartSchemaCacheSync(ctx context.Context, fetcher LiveSchemaFetcher) {
	if fetcher == nil {
		zaplog.Logger.Warnf("schema_cache: fetcher 为 nil，跳过 schema 同步")
		return
	}
	schemaFetcher.Store(fetcher)

	// 同步首拉
	bootCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := refreshLiveSchema(bootCtx, fetcher); err != nil {
		zaplog.Logger.Warnf("schema_cache: 首次拉取实时 schema 失败（退化到硬编码 schema）: %v", err)
	} else {
		zaplog.Logger.Infof("schema_cache: 首次拉取实时 schema 完成（共 %d 字符）", len(loadLiveSchema()))
	}

	// 后台周期刷新
	go func() {
		ticker := time.NewTicker(schemaRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := refreshLiveSchema(refreshCtx, fetcher); err != nil {
					zaplog.Logger.Warnf("schema_cache: 刷新失败（保留旧缓存）: %v", err)
				}
				refreshCancel()
			}
		}
	}()
}

// refreshLiveSchema 内部刷新动作——拉取 + 原子写入。
func refreshLiveSchema(ctx context.Context, fetcher LiveSchemaFetcher) error {
	body, err := fetcher.FetchPublicSchema(ctx)
	if err != nil {
		return err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("FetchPublicSchema 返回空")
	}
	liveSchemaCache.Store(&body)
	return nil
}
