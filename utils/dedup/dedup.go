// Package dedup 提供线程安全、容量上限固定的 LRU 集合。
//
// 用途：跟踪已经处理过的 NapCat message_id，绝对避免下面这些场景导致重复回复：
//   - NapCat /get_group_msg_history 偶发把同一条消息的 message_seq 改了再返回一遍
//   - WS 断线重连后偶发把同一条事件再推一次（NapCat 行为偶尔抖动）
//   - 部署/调试时不小心同时跑了两个 bot 实例（兜底，但不能彻底防）
//
// LRU 而不是单纯 set：消息 id 是单调增长的，旧的可以安全淘汰，避免内存无限增长。
package dedup

import (
	"container/list"
	"sync"
)

// LRUSet 容量上限的 int64 LRU 集合。
type LRUSet struct {
	mu    sync.Mutex
	cap   int
	set   map[int64]*list.Element
	order *list.List // front=最旧 / back=最新
}

// NewLRUSet 构造一个容量上限为 capacity 的集合。
//
// 选 capacity 时按 群数 × 单群每分钟最大消息数 × 期望追溯分钟数 估算；
// 18 群、每群每分钟 5 条、追溯 30 分钟 ≈ 2700，建议 2000~5000，太小会误放过。
func NewLRUSet(capacity int) *LRUSet {
	if capacity <= 0 {
		capacity = 1024
	}
	return &LRUSet{
		cap:   capacity,
		set:   make(map[int64]*list.Element, capacity),
		order: list.New(),
	}
}

// Has 判断 id 是否在集合里（不会改动 LRU 顺序）。
func (s *LRUSet) Has(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.set[id]
	return ok
}

// Add 把 id 写入集合。
//
// 返回值：
//   - true  : 这个 id 之前没有（首次写入），调用方可以继续后续处理
//   - false : 这个 id 已经在集合里，调用方应当 skip（防止重复处理）
//
// 内部如果集合满了，会淘汰最旧的一条。
// 这是一个原子的 "检查并标记" 操作，不会有 TOCTOU 竞态。
func (s *LRUSet) Add(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.set[id]; ok {
		// 已存在：把它提到最新端，避免被过早淘汰
		s.order.MoveToBack(e)
		return false
	}
	if len(s.set) >= s.cap {
		oldest := s.order.Front()
		if oldest != nil {
			s.order.Remove(oldest)
			delete(s.set, oldest.Value.(int64))
		}
	}
	e := s.order.PushBack(id)
	s.set[id] = e
	return true
}

// Len 返回当前集合大小，仅 debug 用。
func (s *LRUSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.set)
}
