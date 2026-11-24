package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/luo303/mindos/backend/internal/agent"
	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/chatmodel"
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
	svc := NewService(nil, nil, nil, repo, nil, approvalSvc, todoSvc, nil)

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
	svc := NewService(nil, nil, nil, repo, nil, approvalSvc, todoSvc, nil)
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

func TestApprovalPartialAnswerSurvivesDecision(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()
	conv := &Conversation{OwnerID: owner, Title: "partial"}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	approvalSvc := approval.NewService(approval.NewRepository(gdb))
	todoSvc := todo.NewService(todo.NewRepository(gdb))
	svc := NewService(nil, nil, nil, repo, nil, approvalSvc, todoSvc, nil)

	req, err := approvalSvc.Propose(ctx, approval.ProposeInput{
		OwnerID: owner, ConversationID: conv.ID, ChatModelID: 10,
		ToolName:  "create_todo",
		Arguments: approval.JSON{"title": "带 partial 的待办"},
		Summary:   "创建待办：带 partial 的待办",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if err := approvalSvc.SetPartialAnswer(ctx, owner, req.ID, "我先查一下，准备创建。"); err != nil {
		t.Fatalf("set partial: %v", err)
	}
	if _, err := svc.DecideApproval(ctx, owner, req.ID, true, &fakeEmitter{}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	messages, err := repo.ListMessages(ctx, owner, conv.ID, 10, 0)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages = %v, %v", messages, err)
	}
	want := "我先查一下，准备创建。\n\n已创建待办：带 partial 的待办"
	if messages[0].Content != want {
		t.Fatalf("content = %q, want %q", messages[0].Content, want)
	}
}

func TestAskPersistsPartialAnswerBeforeApproval(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()
	conv := &Conversation{OwnerID: owner, Title: "pending partial"}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	approvalSvc := approval.NewService(approval.NewRepository(gdb))
	req, err := approvalSvc.Propose(ctx, approval.ProposeInput{
		OwnerID: owner, ConversationID: conv.ID, ChatModelID: 10,
		CheckpointID: "cp-partial", ToolName: "create_todo",
		Arguments: approval.JSON{"title": "partial"}, Summary: "创建待办：partial",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	userMsg, err := repo.AppendMessage(ctx, owner, conv.ID, RoleUser, "建个待办", RawJSON("[]"))
	if err != nil {
		t.Fatalf("append user: %v", err)
	}
	runner := &fakeRunner{
		partial: "我先查一下。",
		pending: &agent.PendingApproval{ApprovalID: req.ID, InterruptID: "interrupt-partial"},
	}
	svc := NewService(
		&fakeResolver{resolved: &chatmodel.Resolved{ID: 1, Model: "fake", BaseURL: "https://x/v1"}},
		&fakeRunStore{},
		&fakeBuilder{runner: runner},
		repo,
		nil,
		approvalSvc,
		nil,
		nil,
	)
	resolved := &chatmodel.Resolved{ID: 1, Model: "fake", BaseURL: "https://x/v1"}
	if err := svc.execute(ctx, owner, AskInput{Question: "建个待办"}, resolved, nil, &conv.ID, userMsg.ID, &fakeEmitter{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got, err := approvalSvc.Get(ctx, owner, req.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if got.PartialAnswer != "我先查一下。" {
		t.Fatalf("partial = %q, want 我先查一下。", got.PartialAnswer)
	}
}

func TestApproveDeleteBatch(t *testing.T) {
	repo, gdb, owner := newConversationDB(t)
	ctx := context.Background()
	conv := &Conversation{OwnerID: owner, Title: "批量删除"}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	todoSvc := todo.NewService(todo.NewRepository(gdb))
	first, err := todoSvc.Create(ctx, owner, todo.CreateInput{Title: "一"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := todoSvc.Create(ctx, owner, todo.CreateInput{Title: "二"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	approvalSvc := approval.NewService(approval.NewRepository(gdb))
	req, err := approvalSvc.Propose(ctx, approval.ProposeInput{
		OwnerID: owner, ConversationID: conv.ID, ChatModelID: 10,
		ToolName:  "delete_todo",
		Arguments: approval.JSON{"ids": []any{float64(first.ID), float64(second.ID)}},
		Summary:   "删除待办 #1、#2",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	svc := NewService(nil, nil, nil, repo, nil, approvalSvc, todoSvc, nil)
	if _, err := svc.DecideApproval(ctx, owner, req.ID, true, &fakeEmitter{}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	items, err := todoSvc.List(ctx, owner, todo.Filter{})
	if err != nil || len(items) != 0 {
		t.Fatalf("remaining = %v, %v", items, err)
	}
	messages, err := repo.ListMessages(ctx, owner, conv.ID, 10, 0)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, "已删除 2 条待办") {
		t.Fatalf("messages = %v, %v", messages, err)
	}
}
