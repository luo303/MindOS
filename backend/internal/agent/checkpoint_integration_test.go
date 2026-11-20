package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/internal/todo"
	"github.com/luo303/mindos/backend/migrations"
)

type toolCallingModel struct {
	mu    sync.Mutex
	calls int
}

func (m *toolCallingModel) next() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls == 1 {
		idx := 0
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index: &idx, ID: "call-1", Type: "function",
				Function: schema.FunctionCall{Name: "create_todo", Arguments: `{"title":"测试断点待办"}`},
			}},
		}
	}
	return &schema.Message{Role: schema.Assistant, Content: "已创建待办。"}
}

func (m *toolCallingModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return m.next(), nil
}
func (m *toolCallingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}
func (m *toolCallingModel) GetType() string          { return "tool-calling" }
func (m *toolCallingModel) IsCallbacksEnabled() bool { return false }
func (m *toolCallingModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestCreateTodoInterruptsAndResumes(t *testing.T) {
	db := testsupport.NewDatabase(t, "agent_checkpoint")
	if err := migrate.New(db, migrations.FS).Up(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb := testsupport.NewGorm(t, db)
	owner := testsupport.NewUser(t, gdb, "checkpoint@example.com")
	if err := gdb.Exec("INSERT INTO conversations (owner_id, title) VALUES (?, ?)", owner, "断点测试").Error; err != nil {
		t.Fatalf("conversation: %v", err)
	}
	var conversationID int64
	if err := gdb.Raw("SELECT id FROM conversations WHERE owner_id = ? ORDER BY id DESC LIMIT 1", owner).Scan(&conversationID).Error; err != nil {
		t.Fatalf("conversation id: %v", err)
	}

	approvalSvc := approval.NewService(approval.NewRepository(gdb))
	todoSvc := todo.NewService(todo.NewRepository(gdb))
	store := NewCheckpointStore(gdb)
	cpID := "cp-test-1"
	ctx, cancel := context.WithTimeout(WithRunMeta(context.Background(), RunMeta{
		OwnerID: owner, CheckpointID: cpID, ConversationID: conversationID, ChatModelID: 1,
	}), 10*time.Second)
	defer cancel()

	a := New(&toolCallingModel{}, &fakeRetriever{}, nil, todoSvc, approvalSvc, store,
		owner, NewRegistry(), 8, DefaultLimits(), nil, nil)

	result, err := a.Ask(ctx, []HistoryMessage{{Role: "user", Content: "建一个待办"}}, nil)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.Pending == nil {
		t.Fatal("expected pending approval")
	}
	if _, ok, err := store.Get(ctx, cpID); err != nil || !ok {
		t.Fatalf("checkpoint missing: ok=%v err=%v", ok, err)
	}
	req, err := approvalSvc.GetByCheckpoint(ctx, cpID)
	if err != nil {
		t.Fatalf("approval: %v", err)
	}
	if req.Status != approval.StatusPending || req.InterruptID == nil {
		t.Fatalf("approval = %+v", req)
	}
	if _, err := approvalSvc.Decide(ctx, owner, req.ID, true); err != nil {
		t.Fatalf("approve: %v", err)
	}

	resumed, err := a.Resume(ctx, cpID, *req.InterruptID, map[string]any{"approved": true}, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Answer != "已创建待办。" {
		t.Fatalf("answer = %q", resumed.Answer)
	}
	items, err := todoSvc.List(ctx, owner, todo.Filter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("todos = %v, %v", items, err)
	}
	if items[0].ApprovalID == nil || *items[0].ApprovalID != req.ID {
		t.Fatalf("todo approval = %+v", items[0])
	}
}
