package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/todo"
)

func TestApproveTodoCreatesTodoAndMessage(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()
	conv := &Conversation{OwnerID: owner, Title: "审批测试"}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	approvalSvc := approval.NewService(approval.NewRepository(gdb))
	todoSvc := todo.NewService(todo.NewRepository(gdb))
	svc := NewService(nil, nil, nil, repo, nil, approvalSvc, todoSvc, Settings{Timeout: time.Second}, nil)

	req, err := approvalSvc.Propose(ctx, approval.ProposeInput{
		OwnerID: owner, ConversationID: conv.ID, ChatModelID: 10,
		ToolName: "create_todo",
		Arguments: approval.JSON{
			"title": "整理 Redis 笔记", "description": "补充集群章节", "priority": float64(1),
		},
		Summary: "创建待办：整理 Redis 笔记",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	decided, err := svc.DecideApproval(ctx, owner, req.ID, true, &fakeEmitter{})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if decided.Status != approval.StatusCompleted {
		t.Fatalf("status = %s, want completed", decided.Status)
	}
	items, err := todoSvc.List(ctx, owner, todo.Filter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("todos = %v, %v", items, err)
	}
	if items[0].Source != todo.SourceAI || items[0].ApprovalID == nil || *items[0].ApprovalID != req.ID {
		t.Fatalf("todo source/approval = %+v", items[0])
	}
	messages, err := repo.ListMessages(ctx, owner, conv.ID, 10, 0)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages = %v, %v", messages, err)
	}
	if messages[0].Role != RoleAssistant {
		t.Errorf("message role = %s", messages[0].Role)
	}
	all, err := svc.ListApprovals(ctx, owner, conv.ID, "all")
	if err != nil || len(all) != 1 || all[0].Status != approval.StatusCompleted {
		t.Fatalf("conversation approvals = %v, %v", all, err)
	}
	pending, err := svc.ListApprovals(ctx, owner, conv.ID, "pending")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending approvals = %v, %v", pending, err)
	}
}

func TestAskBlockedByPendingApproval(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()
	conv := &Conversation{OwnerID: owner, Title: "pending"}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	approvalSvc := approval.NewService(approval.NewRepository(gdb))
	todoSvc := todo.NewService(todo.NewRepository(gdb))
	svc := NewService(nil, nil, nil, repo, nil, approvalSvc, todoSvc, Settings{Timeout: time.Second}, nil)
	if _, err := approvalSvc.Propose(ctx, approval.ProposeInput{
		OwnerID: owner, ConversationID: conv.ID, ToolName: "create_todo",
		Arguments: approval.JSON{"title": "x"}, Summary: "创建待办：x",
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	err := svc.AskInConversation(ctx, owner, conv.ID, AskInput{Question: "继续"}, &fakeEmitter{})
	if !errors.Is(err, ErrApprovalPending) {
		t.Fatalf("ask = %v, want ErrApprovalPending", err)
	}
}
