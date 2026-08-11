package reconcile

import (
	"container/heap"
	"sync"
	"time"
)

// TTLHeapEntry 仅携带 timer 身份所需的 sandbox ID 和预期绝对到期时间。
// 它不保存 revision 或完整领域对象，避免无关状态更新让有效 timer 失效。
type TTLHeapEntry struct {
	// SandboxID 是 Store 中 sandbox 的稳定标识。
	SandboxID string
	// ExpectedExpiresAt 是创建或最近一次续期观察到的 UTC 租约时间。
	ExpectedExpiresAt time.Time
}

type ttlHeapItem struct {
	entry TTLHeapEntry
	index int
}

type ttlHeapItems []*ttlHeapItem

func (items ttlHeapItems) Len() int { return len(items) }
func (items ttlHeapItems) Less(left, right int) bool {
	leftExpiry := items[left].entry.ExpectedExpiresAt
	rightExpiry := items[right].entry.ExpectedExpiresAt
	if leftExpiry.Equal(rightExpiry) {
		return items[left].entry.SandboxID < items[right].entry.SandboxID
	}
	return leftExpiry.Before(rightExpiry)
}
func (items ttlHeapItems) Swap(left, right int) {
	items[left], items[right] = items[right], items[left]
	items[left].index, items[right].index = left, right
}
func (items *ttlHeapItems) Push(value any) {
	item := value.(*ttlHeapItem)
	item.index = len(*items)
	*items = append(*items, item)
}
func (items *ttlHeapItems) Pop() any {
	old := *items
	last := len(old) - 1
	item := old[last]
	old[last] = nil
	item.index = -1
	*items = old[:last]
	return item
}

// TTLHeap 以到期时间和 sandbox ID 稳定排序，并为每个 ID 只保存最新 entry。
type TTLHeap struct {
	mu      sync.Mutex
	items   ttlHeapItems
	indexes map[string]*ttlHeapItem
}

// NewTTLHeap 创建空的并发安全 TTL 最小堆。
func NewTTLHeap() *TTLHeap {
	return &TTLHeap{indexes: make(map[string]*ttlHeapItem)}
}

// Upsert 插入或替换 ID 的 expiry；完全相同的时间返回 false 且不改动堆。
func (h *TTLHeap) Upsert(entry TTLHeapEntry) bool {
	if entry.SandboxID == "" || entry.ExpectedExpiresAt.IsZero() {
		return false
	}
	entry.ExpectedExpiresAt = entry.ExpectedExpiresAt.UTC()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.indexes == nil {
		h.indexes = make(map[string]*ttlHeapItem)
	}
	if item := h.indexes[entry.SandboxID]; item != nil {
		if item.entry.ExpectedExpiresAt.Equal(entry.ExpectedExpiresAt) {
			return false
		}
		item.entry = entry
		heap.Fix(&h.items, item.index)
		return true
	}
	item := &ttlHeapItem{entry: entry}
	heap.Push(&h.items, item)
	h.indexes[entry.SandboxID] = item
	return true
}

// Remove 幂等删除指定 ID；实际删除时返回 true。
func (h *TTLHeap) Remove(sandboxID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	item := h.indexes[sandboxID]
	if item == nil {
		return false
	}
	heap.Remove(&h.items, item.index)
	delete(h.indexes, sandboxID)
	return true
}

// Peek 返回最早到期 entry 的值副本，不从堆中删除。
func (h *TTLHeap) Peek() (TTLHeapEntry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.items) == 0 {
		return TTLHeapEntry{}, false
	}
	return h.items[0].entry, true
}

// Len 返回当前唯一 sandbox ID 数量。
func (h *TTLHeap) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.items)
}
