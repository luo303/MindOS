// Package agent 封装 Eino ADK：把一次问答跑成"带检索工具的对话"。
//
// 三条边界：
//   - 不认识 gin，不写 HTTP 响应（那是 internal/chat 的事）；
//   - 不 import knowledge：只依赖一个搜两个方法的 Retriever，由 main 注入适配器；
//   - 不决定"该不该答"：阈值闸门在 chat 层，这里只负责跑和收集输出。
package agent

import "sync"

// Hit 是一条检索命中的分块。
//
// 与 knowledge.Hit 字段相同但不共用类型：agent 包是通用能力，
// 不该被某个模块的模型绑住。转换发生在装配层（cmd/server），
// 那段代码只有一行循环，换来的是这个包可以脱离数据库测试。
type Hit struct {
	DocumentID  int64
	ChunkID     int64
	Seq         int
	Title       string
	Heading     string
	Content     string
	StartOffset int
	EndOffset   int
	Similarity  float64
	// RRFScore 只用于"本次依据"面板展示融合分数；模型看不到它。
	RRFScore float64
}

// Numbered 是"带编号的片段"，编号就是模型要引用的 [n]。
type Numbered struct {
	N int
	Hit
}

// Registry 保存本次问答登记过的片段，并负责分配编号。
//
// 为什么编号必须由宿主分配而不是让模型自己数：模型看到的编号是我们给的，
// 它只能引用已存在的号；输出里出现没登记过的号，一律判 ungrounded。
// 这样"编造引用"在结构上过不了校验，而不是靠 prompt 求它别编。
//
// 并发安全：Eino 默认并行执行同一轮的多个工具调用，两个 goroutine 可能
// 同时登记片段。没有锁会出现两个编号相同的片段，引用就对不上了。
type Registry struct {
	mu      sync.Mutex
	order   []Numbered
	byChunk map[int64]int
}

// NewRegistry 创建一个空登记表。一次问答一个，不复用。
func NewRegistry() *Registry {
	return &Registry{byChunk: make(map[int64]int)}
}

// Register 给一批片段分配编号，返回带编号的视图。
//
// 同一个分块再次出现时复用原编号：模型第二次检索命中同一段内容，
// 给它一个新号只会让答案里出现 [3] 和 [7] 指向同一处，用户点进去发现一样。
func (r *Registry) Register(hits []Hit) []Numbered {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Numbered, 0, len(hits))
	for _, hit := range hits {
		if n, ok := r.byChunk[hit.ChunkID]; ok {
			out = append(out, Numbered{N: n, Hit: hit})
			continue
		}
		n := len(r.order) + 1
		entry := Numbered{N: n, Hit: hit}
		r.order = append(r.order, entry)
		r.byChunk[hit.ChunkID] = n
		out = append(out, entry)
	}
	return out
}

// Get 按编号取片段。第二个返回值是"这个编号是否存在"。
func (r *Registry) Get(n int) (Numbered, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n < 1 || n > len(r.order) {
		return Numbered{}, false
	}
	return r.order[n-1], true
}

// All 返回登记过的全部片段（副本），用于落 qa_runs 与前端"本次依据"。
func (r *Registry) All() []Numbered {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Numbered, len(r.order))
	copy(out, r.order)
	return out
}

// Len 返回已登记的片段数。
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.order)
}
