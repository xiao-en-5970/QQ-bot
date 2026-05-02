package dedup

import (
	"sync"
	"testing"
)

func TestLRUSet_Add_FirstAndDuplicate(t *testing.T) {
	s := NewLRUSet(10)
	if !s.Add(100) {
		t.Fatalf("第一次 Add 应该返回 true")
	}
	if s.Add(100) {
		t.Fatalf("第二次 Add 同 id 应该返回 false")
	}
	if !s.Has(100) {
		t.Fatalf("Has(100) 应该 true")
	}
	if s.Has(101) {
		t.Fatalf("Has(101) 应该 false")
	}
}

func TestLRUSet_Eviction(t *testing.T) {
	s := NewLRUSet(3)
	s.Add(1)
	s.Add(2)
	s.Add(3)
	// 1 被淘汰，再 add 4
	s.Add(4)
	if s.Has(1) {
		t.Fatalf("最旧的 1 应该已被淘汰")
	}
	if !s.Has(2) || !s.Has(3) || !s.Has(4) {
		t.Fatalf("2/3/4 应该都在")
	}
}

func TestLRUSet_AccessRefreshesLRU(t *testing.T) {
	s := NewLRUSet(3)
	s.Add(1)
	s.Add(2)
	s.Add(3)
	// 重新 Add 1 把它提到最新端
	if s.Add(1) {
		t.Fatalf("已存在的 id 再 Add 应该返回 false")
	}
	// 现在淘汰顺序：2 最旧
	s.Add(4)
	if !s.Has(1) {
		t.Fatalf("1 因为最近访问应该还在")
	}
	if s.Has(2) {
		t.Fatalf("2 是最旧的，应该被淘汰")
	}
}

func TestLRUSet_Concurrent(t *testing.T) {
	s := NewLRUSet(1000)
	const N = 10
	const M = 1000
	var wg sync.WaitGroup
	wg.Add(N)
	for w := 0; w < N; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < M; i++ {
				id := int64(w*M + i)
				if !s.Add(id) {
					t.Errorf("worker %d id %d 应该首次添加", w, id)
				}
				if s.Add(id) {
					t.Errorf("worker %d id %d 第二次应该返回 false", w, id)
				}
			}
		}()
	}
	wg.Wait()
}
