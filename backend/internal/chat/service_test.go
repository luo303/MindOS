package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/agent"
	"github.com/luo303/mindos/backend/internal/chatmodel"
	"github.com/luo303/mindos/backend/internal/knowledge"
)

// fakeEmitter 记录事件顺序。断言顺序比断言单条内容更重要：
// verdict 先于 citation 是前端状态机的前提，错了界面会先渲染出引用再撤回。
// testOwner 是单测里统一的"当前用户"：隔离本身由真库用例验收，
// 单测只关心 owner 是否被一路传下去。
const testOwner int64 = 7

type fakeEmitter struct {
	events []string
	// payloads 按事件类型各留一份，方便查字段。
	verdicts  []string
	citations []CitationItem
	errors    []string
	deltas    strings.Builder
	doneCalls int
	failDelta bool
}

func (e *fakeEmitter) Retrieval(items []RetrievalItem) error {
	e.events = append(e.events, "retrieval")
	return nil
}

func (e *fakeEmitter) Delta(text string) error {
	if e.failDelta {
		return errors.New("client gone")
	}
	e.events = append(e.events, "delta")
	e.deltas.WriteString(text)
	return nil
}

func (e *fakeEmitter) Verdict(status, reason string) error {
	e.events = append(e.events, "verdict")
	e.verdicts = append(e.verdicts, status+"|"+reason)
	return nil
}

func (e *fakeEmitter) Citation(items []CitationItem) error {
	e.events = append(e.events, "citation")
	e.citations = items
	return nil
}

func (e *fakeEmitter) Done(_ DonePayload) error {
	e.events = append(e.events, "done")
	e.doneCalls++
	return nil
}

func (e *fakeEmitter) Error(code, message string, retryable bool) error {
	e.events = append(e.events, "error")
	e.errors = append(e.errors, code)
	return nil
}

func (e *fakeEmitter) order() string { return strings.Join(e.events, ">") }

type fakeSearcher struct {
	result *knowledge.SearchResult
	err    error
	calls  int
	// gotOwner 断言主检索带上了当前用户。
	gotOwner int64
}

func (f *fakeSearcher) Search(_ context.Context, ownerID int64, _ string, _ int, _ knowledge.DocumentFilter) (*knowledge.SearchResult, error) {
	f.gotOwner = ownerID
	f.calls++
	return f.result, f.err
}

type fakeResolver struct {
	resolved *chatmodel.Resolved
	err      error
	touched  int
}

func (f *fakeResolver) Resolve(_ context.Context, _ int64, _ *int64) (*chatmodel.Resolved, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resolved, nil
}

func (f *fakeResolver) Touch(_ context.Context, _, _ int64) error {
	f.touched++
	return nil
}

type fakeRunStore struct {
	runs []*Run
	err  error
}

func (f *fakeRunStore) Create(_ context.Context, run *Run) error {
	if f.err != nil {
		return f.err
	}
	run.ID = int64(len(f.runs) + 1)
	f.runs = append(f.runs, run)
	return nil
}

// fakeRunner 按 answer 推送一到多段 delta，模拟流式。
type fakeRunner struct {
	answer   string
	err      error
	registry *agent.Registry
}

func (f *fakeRunner) Ask(_ context.Context, _ string, onDelta func(string) error) (*agent.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	// 切成两段推送：只推一段测不出"多 delta 累积是否正确"。
	mid := len([]rune(f.answer)) / 2
	runes := []rune(f.answer)
	for _, part := range []string{string(runes[:mid]), string(runes[mid:])} {
		if part == "" {
			continue
		}
		if err := onDelta(part); err != nil {
			return nil, err
		}
	}
	return &agent.Result{
		Answer:           f.answer,
		PromptTokens:     100,
		CompletionTokens: 20,
		Hits:             f.registry.All(),
	}, nil
}

type fakeBuilder struct {
	runner *fakeRunner
	err    error
	calls  int
	// gotOwner 断言问答执行体也带上了当前用户（工具检索要用它）。
	gotOwner int64
}

func (b *fakeBuilder) Build(_ context.Context, ownerID int64, _ *chatmodel.Resolved, registry *agent.Registry) (agent.Runner, error) {
	b.gotOwner = ownerID
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	b.runner.registry = registry
	return b.runner, nil
}

// testDeps 把可选的替身收成一个结构体：调用处只写关心的那几项。
type testDeps struct {
	search  *fakeSearcher
	models  *fakeResolver
	runs    *fakeRunStore
	builder *fakeBuilder
}

func newTestService(t *testing.T, opts testDeps) *Service {
	t.Helper()
	if opts.models == nil {
		opts.models = &fakeResolver{resolved: &chatmodel.Resolved{ID: 1, Model: "spark-x2.6", BaseURL: "https://x/v2"}}
	}
	if opts.runs == nil {
		opts.runs = &fakeRunStore{}
	}
	return NewService(opts.search, opts.models, opts.runs, opts.builder, Settings{TopK: 5, MinSimilarity: 0.35, Timeout: 10 * time.Second}, nil)
}

func hitsFixture(similarity float64) *knowledge.SearchResult {
	return &knowledge.SearchResult{
		Hits: []knowledge.Hit{{
			DocumentID: 1, ChunkID: 42, Seq: 3, Title: "EasyV 多端",
			Content: "同步靠 WebSocket 推送", StartOffset: 10, EndOffset: 30,
			Similarity: similarity, RRFScore: 0.03,
		}},
		VectorHits:    1,
		MaxSimilarity: similarity,
	}
}

func TestAskRejectsEmptyQuestion(t *testing.T) {
	svc := newTestService(t, testDeps{})
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "   "}, &fakeEmitter{}); !errors.Is(err, ErrEmptyQuestion) {
		t.Fatalf("err = %v, want ErrEmptyQuestion", err)
	}
}

func TestAskRejectsLongQuestion(t *testing.T) {
	svc := newTestService(t, testDeps{})
	long := strings.Repeat("问", maxQuestionRunes+1)
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: long}, &fakeEmitter{}); !errors.Is(err, ErrQuestionTooLong) {
		t.Fatalf("err = %v, want ErrQuestionTooLong", err)
	}
}

// 闸门最重要的性质：低相似度时**模型一次都不会被调用**。
// 断言 builder.calls == 0 就是在验证这条，而不是只看返回的文案。
func TestAskRefusesWithoutCallingModel(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{}}
	svc := newTestService(t, testDeps{
		search:  &fakeSearcher{result: hitsFixture(0.2)},
		builder: builder,
	})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "多端怎么同步"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if builder.calls != 0 {
		t.Errorf("model was called %d times below the threshold", builder.calls)
	}
	if got := emit.order(); got != "retrieval>verdict>done" {
		t.Errorf("event order = %s", got)
	}
	if emit.verdicts[0] != VerdictRefused+"|"+ReasonNoEvidence {
		t.Errorf("verdict = %v", emit.verdicts)
	}
}

func TestAskAnswersWithCitations(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{answer: "靠 WebSocket 推送 [1]。"}}
	runs := &fakeRunStore{}
	svc := newTestService(t, testDeps{
		search:  &fakeSearcher{result: hitsFixture(0.72)},
		runs:    runs,
		builder: builder,
	})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "多端怎么同步"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	// 正文按 delta 到达，最后一定要有 done。
	if !strings.Contains(emit.order(), "delta") || !strings.HasSuffix(emit.order(), "done") {
		t.Fatalf("event order = %s", emit.order())
	}
	// verdict 必须先于 citation：否则前端会先渲染出引用，再被裁决撤掉。
	order := emit.order()
	if strings.Index(order, "verdict") > strings.Index(order, "citation") {
		t.Fatalf("citation arrived before verdict: %s", order)
	}
	if emit.verdicts[0] != VerdictAnswered+"|" {
		t.Errorf("verdict = %v", emit.verdicts)
	}
	if len(emit.citations) != 1 || emit.citations[0].ChunkID != 42 {
		t.Fatalf("citations = %+v", emit.citations)
	}
	if emit.citations[0].StartOffset != 10 {
		t.Errorf("offset not carried through: %+v", emit.citations[0])
	}

	if len(runs.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs.runs))
	}
	saved := runs.runs[0]
	if saved.Verdict != VerdictAnswered || !strings.Contains(saved.Answer, "[1]") {
		t.Errorf("saved run = %+v", saved)
	}
	if string(saved.Citations) == "[]" {
		t.Error("citations should be persisted")
	}
	if saved.PromptTokens != 100 || saved.CompletionTokens != 20 {
		t.Errorf("usage = %d/%d", saved.PromptTokens, saved.CompletionTokens)
	}
}

func TestAskWithdrawsAnswerWithoutCitation(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{answer: "多端共享靠 WebSocket 推送。"}}
	runs := &fakeRunStore{}
	svc := newTestService(t, testDeps{
		search:  &fakeSearcher{result: hitsFixture(0.72)},
		runs:    runs,
		builder: builder,
	})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "多端怎么同步"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if emit.verdicts[0] != VerdictUngrounded+"|"+ReasonNoCitation {
		t.Fatalf("verdict = %v", emit.verdicts)
	}
	// 没有引用就不能给 citation 事件：否则界面上引用区是空的，正文却还在。
	for _, e := range emit.events {
		if e == "citation" {
			t.Fatalf("citation must not be emitted for ungrounded answers: %s", emit.order())
		}
	}
	// 落库的 answer 是撤回后的拒答文案，raw_answer 仍保留模型原话。
	saved := runs.runs[0]
	if saved.Answer != refusalAnswer {
		t.Errorf("answer was not withdrawn: %q", saved.Answer)
	}
	if saved.RawAnswer == nil || !strings.Contains(*saved.RawAnswer, "WebSocket") {
		t.Errorf("raw_answer should keep the model output: %v", saved.RawAnswer)
	}
}

func TestAskRejectsUnknownCitation(t *testing.T) {
	// 答案引用了 [7]，但 registry 里只有 [1]：这是编造引用。
	builder := &fakeBuilder{runner: &fakeRunner{answer: "结论见 [7]。"}}
	svc := newTestService(t, testDeps{
		search:  &fakeSearcher{result: hitsFixture(0.72)},
		builder: builder,
	})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "q"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if emit.verdicts[0] != VerdictUngrounded+"|"+ReasonUnknownCitation {
		t.Fatalf("verdict = %v", emit.verdicts)
	}
}

func TestAskKeepsModelRefusalAsRefused(t *testing.T) {
	// 模型老实说"无法回答"：这是正常结果，不是系统缺陷。
	builder := &fakeBuilder{runner: &fakeRunner{answer: agent.RefusalSentence}}
	svc := newTestService(t, testDeps{
		search:  &fakeSearcher{result: hitsFixture(0.72)},
		builder: builder,
	})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "q"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if emit.verdicts[0] != VerdictRefused+"|"+ReasonModelRefused {
		t.Fatalf("verdict = %v", emit.verdicts)
	}
}

func TestAskRecordsFailure(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{err: context.DeadlineExceeded}}
	runs := &fakeRunStore{}
	svc := newTestService(t, testDeps{
		search:  &fakeSearcher{result: hitsFixture(0.72)},
		runs:    runs,
		builder: builder,
	})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "q"}, emit); err != nil {
		// 已经开始推事件，Ask 必须返回 nil：状态码已经发出去了。
		t.Fatalf("Ask should swallow post-stream errors, got %v", err)
	}
	if len(emit.errors) != 1 || emit.errors[0] != ReasonTimeout {
		t.Fatalf("errors = %v", emit.errors)
	}
	if len(runs.runs) != 1 || runs.runs[0].Verdict != VerdictFailed {
		t.Fatalf("failed run not recorded: %+v", runs.runs)
	}
	if runs.runs[0].ErrorCode == nil || *runs.runs[0].ErrorCode != ReasonTimeout {
		t.Errorf("error_code = %v", runs.runs[0].ErrorCode)
	}
}

func TestAskStopsWhenClientDisconnects(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{answer: "一段比较长的回答 [1]，会被切成两半推送。"}}
	svc := newTestService(t, testDeps{
		search:  &fakeSearcher{result: hitsFixture(0.72)},
		builder: builder,
	})

	emit := &fakeEmitter{failDelta: true}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "q"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// 断开后仍然要落一行：那次调用已经产生了成本，账不能因为用户走了就不记。
	if emit.doneCalls != 1 {
		t.Errorf("done was emitted %d times", emit.doneCalls)
	}
}

func TestAskWithoutEmbeddingPropagates(t *testing.T) {
	// 未配置向量模型时检索层返回 ErrEmbeddingNotConfigured，handler 翻成 503。
	builder := &fakeBuilder{runner: &fakeRunner{}}
	svc := newTestService(t, testDeps{
		search:  &fakeSearcher{err: knowledge.ErrEmbeddingNotConfigured},
		builder: builder,
	})

	err := svc.Ask(context.Background(), testOwner, AskInput{Question: "q"}, &fakeEmitter{})
	if !errors.Is(err, knowledge.ErrEmbeddingNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if builder.calls != 0 {
		t.Error("model must not be built when retrieval is unavailable")
	}
}

func TestParseCitations(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"无引用", nil},
		{"结论 [1]。", []int{1}},
		{"先 [3] 再 [1]，又 [3]。", []int{1, 3}},
		{"多个来源 [1][3][2]", []int{1, 2, 3}},
		// 超过三位数字不当成引用：那是代码下标之类的东西。
		{"数组下标 a[1234]", nil},
		{"[0] 不是合法编号", nil},
	}
	for _, tc := range cases {
		got := parseCitations(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseCitations(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseCitations(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestBuildPromptIncludesContext(t *testing.T) {
	prompt := buildPrompt("怎么同步", []agent.Numbered{{
		N:   1,
		Hit: agent.Hit{Title: "EasyV 多端", Seq: 2, Content: "WebSocket 推送", Similarity: 0.71},
	}})
	for _, want := range []string{"<context>", "[1]", "EasyV 多端", "第 3 段", "0.71", "怎么同步"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
