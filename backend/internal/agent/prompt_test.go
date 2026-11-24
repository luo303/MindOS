package agent

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/todo"
)

type loreModelCapture struct {
	fakeChatModel
	mu     sync.Mutex
	tools  []*schema.ToolInfo
	prompt string
}

func (c *loreModelCapture) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools = slices.Clone(model.GetCommonOptions(nil, opts...).Tools)
	for _, message := range messages {
		if message.Role == schema.System {
			c.prompt += message.Content + "\n"
		}
	}
	return c.fakeChatModel.Stream(ctx, messages, opts...)
}

type unusedCheckpointStore struct{}

func (unusedCheckpointStore) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}

func (unusedCheckpointStore) Set(context.Context, string, []byte) error { return nil }

func TestLorePromptMatchesToolsSentToModel(t *testing.T) {
	conversation := RunMeta{CheckpointID: "checkpoint", ConversationID: 1}
	for _, test := range []struct {
		name      string
		meta      RunMeta
		todos     bool
		sql       bool
		approvals bool
		store     bool
		wantTools []string
	}{
		{name: "仅知识检索", wantTools: []string{"search_knowledge"}},
		{name: "单轮不能写", meta: RunMeta{CheckpointID: "checkpoint"}, todos: true, approvals: true, store: true, wantTools: []string{"search_knowledge", "list_todos"}},
		{name: "没有审批只读", meta: conversation, todos: true, store: true, wantTools: []string{"search_knowledge", "list_todos"}},
		{name: "没有断点存储只读", meta: conversation, todos: true, approvals: true, wantTools: []string{"search_knowledge", "list_todos"}},
		{name: "会话可审批和统计", meta: conversation, todos: true, approvals: true, store: true, sql: true,
			wantTools: []string{"search_knowledge", "query_knowledge", "list_todos", "create_todo", "update_todo", "delete_todo"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(WithRunMeta(t.Context(), test.meta), 5*time.Second)
			defer cancel()
			capture := &loreModelCapture{fakeChatModel: fakeChatModel{reply: "你好，我是 Lore。"}}
			var todos *todo.Service
			var approvals *approval.Service
			var sqlRunner SQLRunner
			var store adk.CheckPointStore
			if test.todos {
				todos = todo.NewService(nil)
			}
			if test.approvals {
				approvals = approval.NewService(nil)
			}
			if test.sql {
				sqlRunner = NewSQLExecutor(nil)
			}
			if test.store {
				store = unusedCheckpointStore{}
			}
			agent := New(capture, &fakeRetriever{}, sqlRunner, todos, approvals, store, 1, NewRegistry(), 5, DefaultLimits(), nil, nil)
			if _, err := agent.Ask(ctx, []HistoryMessage{{Role: "user", Content: "你是谁？"}}, nil); err != nil {
				t.Fatalf("Ask: %v", err)
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if !strings.Contains(capture.prompt, "你是 Lore，MindOS 的个人知识与行动助手") {
				t.Errorf("主提示词未传入：%q", capture.prompt)
			}
			wantReadOnly := !slices.Contains(test.wantTools, "create_todo")
			if gotReadOnly := strings.Contains(capture.prompt, "当前没有可用的待办写工具"); gotReadOnly != wantReadOnly {
				t.Errorf("待办写入提示与工具不匹配：readOnly=%v，应为 %v", gotReadOnly, wantReadOnly)
			}
			if len(capture.tools) != len(test.wantTools) {
				t.Fatalf("注册了 %d 个工具，应为 %v", len(capture.tools), test.wantTools)
			}
			for _, name := range []string{"search_knowledge", "query_knowledge", "list_todos", "create_todo", "update_todo", "delete_todo"} {
				want := slices.Contains(test.wantTools, name)
				bound := slices.ContainsFunc(capture.tools, func(info *schema.ToolInfo) bool { return info.Name == name })
				if mentioned := strings.Contains(capture.prompt, name); bound != want || mentioned != want {
					t.Errorf("%s：已注册=%v，提示词提及=%v，应启用=%v", name, bound, mentioned, want)
				}
			}
			for _, info := range capture.tools {
				assertLoreToolSchema(t, info)
			}
		})
	}
}

func assertLoreToolSchema(t *testing.T, info *schema.ToolInfo) {
	t.Helper()
	var required []string
	switch info.Name {
	case "search_knowledge":
		required = []string{"query"}
	case "query_knowledge":
		required = []string{"sql"}
	case "create_todo":
		required = []string{"title"}
	case "update_todo", "delete_todo":
		required = []string{"id"}
	}
	params, err := info.ToJSONSchema()
	if err != nil {
		t.Fatalf("%s schema: %v", info.Name, err)
	}
	if !slices.Equal(params.Required, required) {
		t.Errorf("%s 必填项为 %v，应为 %v", info.Name, params.Required, required)
	}
}
