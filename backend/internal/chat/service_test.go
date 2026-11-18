package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/agent"
	"github.com/luo303/mindos/backend/internal/chatmodel"
)

// fakeEmitter 记录事件顺序。断言顺序比断言单条内容更重要：
// verdict 先于 citation 是前端状态机的前提。
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

// fakeRunner 模拟一次 ReAct run：模型可以决定先"检索"（登记一条命中并回调），
// 再流式输出正文。检索 0 次或 1 次都覆盖得到。
type fakeRunner struct {
	answer   string
	err      error
	hit      *agent.Hit
	registry *agent.Registry
	onHits   func([]agent.Numbered)
}

func (f *fakeRunner) Ask(_ context.Context, _ string, onDelta func(string) error) (*agent.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.hit != nil {
		numbered := f.registry.Register([]agent.Hit{*f.hit})
		if f.onHits != nil {
			f.onHits(numbered)
		}
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

func (b *fakeBuilder) Build(_ context.Context, ownerID int64, _ *chatmodel.Resolved, registry *agent.Registry, onHits func([]agent.Numbered)) (agent.Runner, error) {
	b.gotOwner = ownerID
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	b.runner.registry = registry
	b.runner.onHits = onHits
	return b.runner, nil
}

// testDeps 把可选的替身收成一个结构体：调用处只写关心的那几项。
type testDeps struct {
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
	return NewService(opts.models, opts.runs, opts.builder, Settings{Timeout: 10 * time.Second}, nil)
}

func hitFixture() *agent.Hit {
	return &agent.Hit{
		DocumentID: 1, ChunkID: 42, Seq: 3, Title: "EasyV 多端",
		Content: "同步靠 WebSocket 推送", StartOffset: 10, EndOffset: 30,
		Similarity: 0.72, RRFScore: 0.03,
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

func TestAskAnswersWithCitations(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{
		answer: "靠 WebSocket 推送 [1]。",
		hit:    hitFixture(),
	}}
	runs := &fakeRunStore{}
	svc := newTestService(t, testDeps{runs: runs, builder: builder})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "多端怎么同步"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	// 检索是模型在 run 里触发的：先 retrieval，再有正文。
	if !strings.HasPrefix(emit.order(), "retrieval>") {
		t.Fatalf("event order = %s", emit.order())
	}
	if !strings.HasSuffix(emit.order(), "done") {
		t.Fatalf("event order = %s", emit.order())
	}
	// verdict 必须先于 citation。
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
	if string(saved.Retrieved) == "[]" {
		t.Error("retrieved hits should be persisted")
	}
	if saved.PromptTokens != 100 || saved.CompletionTokens != 20 {
		t.Errorf("usage = %d/%d", saved.PromptTokens, saved.CompletionTokens)
	}
}

// 没有引用不再撤回：模型说了什么就展示什么。
func TestAskWithoutCitationsKeepsAnswer(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{
		answer: "多端共享靠 WebSocket 推送。",
		hit:    hitFixture(),
	}}
	runs := &fakeRunStore{}
	svc := newTestService(t, testDeps{runs: runs, builder: builder})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "多端怎么同步"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if emit.verdicts[0] != VerdictAnswered+"|" {
		t.Fatalf("verdict = %v", emit.verdicts)
	}
	for _, e := range emit.events {
		if e == "citation" {
			t.Fatalf("citation must not be emitted without [n]: %s", emit.order())
		}
	}
	saved := runs.runs[0]
	if saved.Answer != "多端共享靠 WebSocket 推送。" {
		t.Errorf("answer was modified: %q", saved.Answer)
	}
}

// 引用了没有登记过的编号：不发这条 citation，正文原样保留。
func TestAskIgnoresUnknownCitation(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{
		answer: "结论见 [7]。",
		hit:    hitFixture(),
	}}
	runs := &fakeRunStore{}
	svc := newTestService(t, testDeps{runs: runs, builder: builder})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "q"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for _, e := range emit.events {
		if e == "citation" {
			t.Fatalf("unknown citation must not be emitted: %s", emit.order())
		}
	}
	if runs.runs[0].Answer != "结论见 [7]。" {
		t.Errorf("answer was modified: %q", runs.runs[0].Answer)
	}
}

// 模型一次工具都没调：没有 retrieval、没有 citation，回答照常展示。
func TestAskWithoutRetrieval(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{answer: "你好，我是 MindOS。"}}
	runs := &fakeRunStore{}
	svc := newTestService(t, testDeps{runs: runs, builder: builder})

	emit := &fakeEmitter{}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "你是谁"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for _, e := range emit.events {
		if e == "retrieval" || e == "citation" {
			t.Fatalf("no tool call should produce no retrieval/citation: %s", emit.order())
		}
	}
	if string(runs.runs[0].Retrieved) != "[]" {
		t.Errorf("retrieved = %s, want []", runs.runs[0].Retrieved)
	}
}

func TestAskRecordsFailure(t *testing.T) {
	builder := &fakeBuilder{runner: &fakeRunner{err: context.DeadlineExceeded}}
	runs := &fakeRunStore{}
	svc := newTestService(t, testDeps{runs: runs, builder: builder})

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
	builder := &fakeBuilder{runner: &fakeRunner{answer: "一段比较长的回答 [1]，会被切成两半推送。", hit: hitFixture()}}
	svc := newTestService(t, testDeps{builder: builder})

	emit := &fakeEmitter{failDelta: true}
	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "q"}, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// 断开后仍然要落一行：那次调用已经产生了成本，账不能因为用户走了就不记。
	if emit.doneCalls != 1 {
		t.Errorf("done was emitted %d times", emit.doneCalls)
	}
}

// builder 在流开始前失败：Ask 返回错误，交给 handler 出 JSON。
func TestAskReturnsBuilderErrorBeforeStream(t *testing.T) {
	boom := errors.New("build failed")
	svc := newTestService(t, testDeps{builder: &fakeBuilder{err: boom}})

	if err := svc.Ask(context.Background(), testOwner, AskInput{Question: "q"}, &fakeEmitter{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want build error", err)
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
