package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestRegistryAllocatesSequentialNumbers(t *testing.T) {
	r := NewRegistry()
	first := r.Register([]Hit{{ChunkID: 10}, {ChunkID: 11}})
	if len(first) != 2 || first[0].N != 1 || first[1].N != 2 {
		t.Fatalf("first batch numbering = %+v", first)
	}

	// 第二轮从 3 开始：编号在全 run 内单调，不复用 1、2。
	second := r.Register([]Hit{{ChunkID: 20}})
	if second[0].N != 3 {
		t.Fatalf("second batch started at %d, want 3", second[0].N)
	}
	if r.Len() != 3 {
		t.Errorf("registry size = %d, want 3", r.Len())
	}
}

func TestRegistryReusesNumberForSameChunk(t *testing.T) {
	r := NewRegistry()
	r.Register([]Hit{{ChunkID: 10}})

	// 模型换关键词又查到同一段：必须复用编号 1。
	// 给新编号会让答案里出现 [1] 和 [2] 指向同一处，用户点进去发现一样。
	again := r.Register([]Hit{{ChunkID: 10}, {ChunkID: 99}})
	if again[0].N != 1 {
		t.Errorf("same chunk got number %d, want 1", again[0].N)
	}
	if again[1].N != 2 {
		t.Errorf("new chunk got number %d, want 2", again[1].N)
	}
	if r.Len() != 2 {
		t.Errorf("registry size = %d, want 2", r.Len())
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	r.Register([]Hit{{ChunkID: 10, Title: "标题"}})

	got, ok := r.Get(1)
	if !ok || got.ChunkID != 10 || got.Title != "标题" {
		t.Fatalf("Get(1) = %+v, %v", got, ok)
	}
	// 0、负数、超出范围都必须判"不存在"：越界编号要能让上层判 ungrounded。
	for _, n := range []int{0, -1, 2, 100} {
		if _, ok := r.Get(n); ok {
			t.Errorf("Get(%d) reported an entry", n)
		}
	}
}

func TestRegistryAllReturnsCopy(t *testing.T) {
	r := NewRegistry()
	r.Register([]Hit{{ChunkID: 10}})

	snapshot := r.All()
	snapshot[0].ChunkID = 999
	// 拿到的是副本：改了它不会污染 registry，否则落库的引用会被人误改。
	again, _ := r.Get(1)
	if again.ChunkID != 10 {
		t.Errorf("registry was mutated through All(): %d", again.ChunkID)
	}
}

func TestTruncateRunes(t *testing.T) {
	// 按码点截断：按字节切会把一个汉字劈成两半，JSON 编码后变成替换字符。
	if got := truncateRunes("一二三四五", 3); got != "一二三…" {
		t.Errorf("truncateRunes = %q", got)
	}
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Errorf("short string changed: %q", got)
	}
	if got := truncateRunes("abc", 0); got != "" {
		t.Errorf("limit 0 = %q, want empty", got)
	}
}

// fakeRetriever 记录收到的参数并返回预设结果。
type fakeRetriever struct {
	hits  []Hit
	err   error
	calls int
	// gotOwner 断言工具检索带上了当前用户：漏传会答到别人的知识库里。
	gotOwner int64
	gotQ     string
	gotN     int
}

func (f *fakeRetriever) Search(_ context.Context, ownerID int64, query string, limit int) ([]Hit, error) {
	f.calls++
	f.gotOwner = ownerID
	f.gotQ = query
	f.gotN = limit
	return f.hits, f.err
}

func TestSearchToolRegistersHits(t *testing.T) {
	retriever := &fakeRetriever{hits: []Hit{{ChunkID: 7, Title: "文档", Seq: 3, Content: "正文"}}}
	registry := NewRegistry()
	var pushed []Numbered
	searchTool, err := newSearchTool(retriever, 42, registry, 8, newToolBudget(3), func(items []Numbered) {
		pushed = append(pushed, items...)
	})
	if err != nil {
		t.Fatalf("newSearchTool: %v", err)
	}

	out, err := searchTool.InvokableRun(context.Background(), `{"query":"  Redis 集群 ","limit":5}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	// 查询词要被 trim 再传给检索：带着空格去做向量化，召回质量会莫名其妙地差。
	if retriever.gotOwner != 42 {
		t.Errorf("owner = %d, want 42", retriever.gotOwner)
	}
	if retriever.gotQ != "Redis 集群" {
		t.Errorf("query = %q, want trimmed", retriever.gotQ)
	}
	if retriever.gotN != 5 {
		t.Errorf("limit = %d, want 5", retriever.gotN)
	}
	if registry.Len() != 1 {
		t.Fatalf("registry size = %d, want 1", registry.Len())
	}
	// 宿主靠这个回调发 retrieval 事件：漏了它，前端就永远看不到"本次依据"。
	if len(pushed) != 1 || pushed[0].N != 1 || pushed[0].ChunkID != 7 {
		t.Fatalf("onHits = %+v, want one numbered hit", pushed)
	}

	var parsed []searchOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("tool output is not JSON: %v (%s)", err, out)
	}
	if len(parsed) != 1 || parsed[0].N != 1 {
		t.Fatalf("parsed output = %+v", parsed)
	}
	if parsed[0].Content != "正文" || parsed[0].Section != 3 {
		t.Errorf("output fields = %+v", parsed[0])
	}
}

func TestSearchToolRejectsEmptyQuery(t *testing.T) {
	retriever := &fakeRetriever{}
	searchTool, err := newSearchTool(retriever, 42, NewRegistry(), 8, newToolBudget(3), nil)
	if err != nil {
		t.Fatalf("newSearchTool: %v", err)
	}
	if _, err := searchTool.InvokableRun(context.Background(), `{"query":"   "}`); err == nil {
		t.Fatal("empty query should fail")
	}
	// 空查询不该浪费一次向量化 + 两路 SQL。
	if retriever.calls != 0 {
		t.Errorf("retriever called %d times for an empty query", retriever.calls)
	}
}

func TestSearchToolEnforcesCallLimit(t *testing.T) {
	retriever := &fakeRetriever{}
	searchTool, err := newSearchTool(retriever, 42, NewRegistry(), 8, newToolBudget(2), nil)
	if err != nil {
		t.Fatalf("newSearchTool: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := searchTool.InvokableRun(context.Background(), `{"query":"q"}`); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	_, err = searchTool.InvokableRun(context.Background(), `{"query":"q"}`)
	if !errors.Is(err, ErrToolCallLimit) {
		t.Fatalf("third call err = %v, want ErrToolCallLimit", err)
	}
	// 超限之后不能再调检索：否则限流只是"报错但照样烧钱"。
	if retriever.calls != 2 {
		t.Errorf("retriever called %d times, want 2", retriever.calls)
	}
}

func TestSearchToolEmptyResultIsNotAnError(t *testing.T) {
	searchTool, err := newSearchTool(&fakeRetriever{}, 42, NewRegistry(), 8, newToolBudget(3), nil)
	if err != nil {
		t.Fatalf("newSearchTool: %v", err)
	}
	out, err := searchTool.InvokableRun(context.Background(), `{"query":"找不到"}`)
	if err != nil {
		t.Fatalf("empty result should not be an error: %v", err)
	}
	// 返回空数组而不是错误：报错会让模型以为工具坏了，转而编内容填空。
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want []", out)
	}
}

// fakeChatModel 是最小的 ChatModel 实现：固定回一句、可选 usage。
//
// 必须实现 WithTools——ChatModelAgent 在有工具时会做类型断言，
// 少了它构造阶段就失败，测不到 Ask 的流程。
type fakeChatModel struct {
	reply string
	usage *schema.TokenUsage
	err   error
}

func (f *fakeChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &schema.Message{
		Role:         schema.Assistant,
		Content:      f.reply,
		ResponseMeta: &schema.ResponseMeta{Usage: f.usage},
	}, nil
}

func (f *fakeChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if f.err != nil {
		return nil, f.err
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:         schema.Assistant,
		Content:      f.reply,
		ResponseMeta: &schema.ResponseMeta{Usage: f.usage},
	}}), nil
}

func (f *fakeChatModel) GetType() string          { return "fake" }
func (f *fakeChatModel) IsCallbacksEnabled() bool { return false }
func (f *fakeChatModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return f, nil
}

var _ model.ToolCallingChatModel = (*fakeChatModel)(nil)

func TestAskCollectsAnswerAndUsage(t *testing.T) {
	registry := NewRegistry()
	// 预置一条片段：模拟"宿主已经做过预检索并把编号给了模型"。
	registry.Register([]Hit{{ChunkID: 5, Title: "文档", Content: "正文"}})

	bot := &fakeChatModel{
		reply: "多端共享靠 WebSocket 推送 [1]。",
		usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 20},
	}
	a := New(bot, &fakeRetriever{}, nil, 42, registry, 8, DefaultLimits(), nil, nil)

	var streamed strings.Builder
	result, err := a.Ask(context.Background(), []HistoryMessage{{Role: "user", Content: "多端共享怎么做的"}}, func(delta string) error {
		streamed.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.Answer == "" {
		t.Fatal("answer is empty")
	}
	if streamed.String() != result.Answer {
		t.Errorf("onDelta received %q, answer is %q", streamed.String(), result.Answer)
	}
	if result.PromptTokens != 100 || result.CompletionTokens != 20 {
		t.Errorf("usage = %d/%d", result.PromptTokens, result.CompletionTokens)
	}
	if len(result.Hits) != 1 || result.Hits[0].N != 1 {
		t.Errorf("hits = %+v", result.Hits)
	}
}

func TestAskPropagatesModelError(t *testing.T) {
	bot := &fakeChatModel{err: errors.New("upstream exploded")}
	a := New(bot, &fakeRetriever{}, nil, 42, NewRegistry(), 8, DefaultLimits(), nil, nil)
	if _, err := a.Ask(context.Background(), []HistoryMessage{{Role: "user", Content: "q"}}, nil); err == nil {
		t.Fatal("model error should be returned")
	}
}

func TestAskStopsWhenDeltaHandlerFails(t *testing.T) {
	bot := &fakeChatModel{reply: "答案 [1]"}
	a := New(bot, &fakeRetriever{}, nil, 42, NewRegistry(), 8, DefaultLimits(), nil, nil)

	wantErr := errors.New("client gone")
	_, err := a.Ask(context.Background(), []HistoryMessage{{Role: "user", Content: "q"}}, func(string) error { return wantErr })
	// 客户端断开时 onDelta 返回错误，Ask 必须立刻停下并把错误带出去，
	// 否则会继续读到模型说完，白白烧掉剩下的 token。
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the handler error", err)
	}
}

func TestAskReportsBudgetExceeded(t *testing.T) {
	bot := &fakeChatModel{
		reply: "答案",
		usage: &schema.TokenUsage{PromptTokens: 40000, CompletionTokens: 30000},
	}
	limits := DefaultLimits()
	limits.MaxTokens = 60000
	a := New(bot, &fakeRetriever{}, nil, 42, NewRegistry(), 8, limits, nil, nil)

	if _, err := a.Ask(context.Background(), []HistoryMessage{{Role: "user", Content: "q"}}, nil); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
}

// 工具在 registry 里登记的编号与 agent 返回的 Hits 必须是同一批：
// 否则 chat 层拼引用时会指向别的片段。
func TestToolAndResultShareRegistry(t *testing.T) {
	retriever := &fakeRetriever{hits: []Hit{{ChunkID: 3, Title: "t", Content: "c"}}}
	registry := NewRegistry()
	searchTool, err := newSearchTool(retriever, 42, registry, 8, newToolBudget(3), nil)
	if err != nil {
		t.Fatalf("newSearchTool: %v", err)
	}
	if _, err := searchTool.InvokableRun(context.Background(), `{"query":"q"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	hits := registry.All()
	if len(hits) != 1 || hits[0].ChunkID != 3 {
		t.Fatalf("registry hits = %+v", hits)
	}
	var _ tool.InvokableTool = searchTool
}
