package chat

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/agent"
	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/todo"
)

// ListApprovals 返回当前用户的待审批动作；可按会话过滤。
func (s *Service) ListApprovals(ctx context.Context, ownerID, conversationID int64, status string) ([]approval.Request, error) {
	if s.approvals == nil {
		return nil, nil
	}
	return s.approvals.ListByConversation(ctx, ownerID, conversationID, status)
}

// DecideApproval 批准/拒绝一条 AI 提案。
//
// 有 checkpoint 的提案走 Agent.Resume：模型从工具中断点继续；
// 老的无 checkpoint 提案保留直接执行路径，兼容升级前的数据。
func (s *Service) DecideApproval(ctx context.Context, ownerID, id int64, approve bool) (*approval.Request, error) {
	if s.approvals == nil {
		return nil, approval.ErrNotFound
	}
	req, err := s.approvals.Get(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	if req.Status != approval.StatusPending {
		return nil, approval.ErrNotFound
	}
	decided, err := s.approvals.Decide(ctx, ownerID, id, approve)
	if err != nil {
		return nil, err
	}
	if decided.CheckpointID != nil && *decided.CheckpointID != "" && decided.InterruptID != nil {
		return s.resumeDecision(ctx, ownerID, decided, approve)
	}
	if !approve {
		s.completeApprovalMessage(ctx, ownerID, decided, "已拒绝创建待办。")
		_ = s.approvals.Complete(ctx, ownerID, id, approval.JSON{"approved": false})
		return s.approvals.Get(ctx, ownerID, id)
	}

	result := approval.JSON{}
	switch decided.ToolName {
	case "create_todo":
		todoItem, createErr := s.createTodoFromApproval(ctx, ownerID, decided)
		if createErr != nil {
			_ = s.approvals.Fail(ctx, ownerID, id, approval.JSON{"error": createErr.Error()})
			return nil, createErr
		}
		result["todo_id"] = todoItem.ID
		result["title"] = todoItem.Title
		s.completeApprovalMessage(ctx, ownerID, decided, "已创建待办："+todoItem.Title)
	case "update_todo":
		todoItem, updateErr := s.updateTodoFromApproval(ctx, ownerID, decided)
		if updateErr != nil {
			_ = s.approvals.Fail(ctx, ownerID, id, approval.JSON{"error": updateErr.Error()})
			return nil, updateErr
		}
		result["todo_id"] = todoItem.ID
		s.completeApprovalMessage(ctx, ownerID, decided, "已修改待办："+todoItem.Title)
	case "delete_todo":
		if err := s.deleteTodoFromApproval(ctx, ownerID, decided); err != nil {
			_ = s.approvals.Fail(ctx, ownerID, id, approval.JSON{"error": err.Error()})
			return nil, err
		}
		result["deleted"] = true
		s.completeApprovalMessage(ctx, ownerID, decided, "已删除待办。")
	default:
		_ = s.approvals.Fail(ctx, ownerID, id, approval.JSON{"error": "unsupported tool"})
		return nil, errors.New("unsupported approval tool")
	}
	if err := s.approvals.Complete(ctx, ownerID, id, result); err != nil {
		return nil, err
	}
	return s.approvals.Get(ctx, ownerID, id)
}

func (s *Service) createTodoFromApproval(ctx context.Context, ownerID int64, req *approval.Request) (*todo.Todo, error) {
	if s.todos == nil {
		return nil, errors.New("todo service is not configured")
	}
	title, _ := req.Arguments["title"].(string)
	description, _ := req.Arguments["description"].(string)
	priority := 2
	if v, ok := req.Arguments["priority"].(float64); ok {
		priority = int(v)
	}
	var due *time.Time
	if raw, ok := req.Arguments["due_at"].(string); ok && raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, err
		}
		due = &parsed
	}
	return s.todos.Create(ctx, ownerID, todo.CreateInput{
		Title:       title,
		Description: description,
		Priority:    priority,
		DueAt:       due,
		Source:      todo.SourceAI,
		SourceRef: todo.JSON{
			"approval_id": req.ID,
			"tool":        req.ToolName,
		},
		ApprovalID: &req.ID,
	})
}

func (s *Service) updateTodoFromApproval(ctx context.Context, ownerID int64, req *approval.Request) (*todo.Todo, error) {
	if s.todos == nil {
		return nil, errors.New("todo service is not configured")
	}
	id, _ := req.Arguments["id"].(float64)
	if id <= 0 {
		return nil, errors.New("todo id is required")
	}
	in := todo.UpdateInput{}
	if v, ok := req.Arguments["title"].(string); ok && v != "" {
		in.Title = &v
	}
	if v, ok := req.Arguments["description"].(string); ok && v != "" {
		in.Description = &v
	}
	if v, ok := req.Arguments["status"].(string); ok && v != "" {
		in.Status = &v
	}
	if v, ok := req.Arguments["priority"].(float64); ok && v > 0 {
		priority := int(v)
		in.Priority = &priority
	}
	return s.todos.Update(ctx, ownerID, int64(id), in)
}

func (s *Service) deleteTodoFromApproval(ctx context.Context, ownerID int64, req *approval.Request) error {
	if s.todos == nil {
		return errors.New("todo service is not configured")
	}
	id, _ := req.Arguments["id"].(float64)
	if id <= 0 {
		return errors.New("todo id is required")
	}
	return s.todos.Delete(ctx, ownerID, int64(id))
}

func (s *Service) completeApprovalMessage(ctx context.Context, ownerID int64, req *approval.Request, text string) {
	if req.ConversationID == nil || s.convs == nil {
		return
	}
	if _, err := s.convs.AppendMessage(ctx, ownerID, *req.ConversationID, RoleAssistant, text, RawJSON("[]")); err != nil {
		s.log.Warn("save approval result message failed", zap.Int64("approval_id", req.ID), zap.Error(err))
	}
}

// resumeDecision 从 Eino checkpoint 恢复 Agent。
//
// 这是真正的断点恢复：模型停在 create_todo 的中断点，批准/拒绝后
// 工具重新进入 InvokableRun，再由模型继续生成最终回答。
func (s *Service) resumeDecision(ctx context.Context, ownerID int64, req *approval.Request, approve bool) (*approval.Request, error) {
	if s.builder == nil || s.models == nil {
		return nil, errors.New("resume is not configured")
	}
	runCtx, cancel := context.WithTimeout(ctx, s.settings.normalized().Timeout)
	defer cancel()
	meta := agent.RunMeta{OwnerID: ownerID, CheckpointID: *req.CheckpointID, ChatModelID: 0}
	if req.ConversationID != nil {
		meta.ConversationID = *req.ConversationID
	}
	if req.ChatModelID != nil {
		meta.ChatModelID = *req.ChatModelID
	}
	runCtx = agent.WithRunMeta(runCtx, meta)
	resolved, err := s.models.Resolve(runCtx, ownerID, req.ChatModelID)
	if err != nil {
		_ = s.approvals.Fail(ctx, ownerID, req.ID, approval.JSON{"error": err.Error()})
		return nil, err
	}
	registry := agent.NewRegistry()
	answerer, err := s.builder.Build(runCtx, ownerID, resolved, registry, nil)
	if err != nil {
		_ = s.approvals.Fail(ctx, ownerID, req.ID, approval.JSON{"error": err.Error()})
		return nil, err
	}
	resumer, ok := answerer.(interface {
		Resume(context.Context, string, string, map[string]any, func(string) error) (*agent.Result, error)
	})
	if !ok {
		_ = s.approvals.Fail(ctx, ownerID, req.ID, approval.JSON{"error": "runner does not support resume"})
		return nil, errors.New("runner does not support resume")
	}

	var answer strings.Builder
	result, err := resumer.Resume(runCtx, *req.CheckpointID, *req.InterruptID,
		map[string]any{"approved": approve}, func(delta string) error {
			answer.WriteString(delta)
			return nil
		})
	if err != nil {
		_ = s.approvals.Fail(ctx, ownerID, req.ID, approval.JSON{"error": err.Error()})
		return nil, err
	}
	if result.Pending != nil {
		_ = s.approvals.Fail(ctx, ownerID, req.ID, approval.JSON{"error": "another approval required"})
		return nil, errors.New("another approval is required")
	}
	final := strings.TrimSpace(result.Answer)
	if final == "" {
		final = "审批已处理。"
	}
	if req.ConversationID != nil {
		if _, err := s.convs.AppendMessage(ctx, ownerID, *req.ConversationID, RoleAssistant, final, marshalJSON(nil)); err != nil {
			s.log.Warn("save resumed message failed", zap.Int64("approval_id", req.ID), zap.Error(err))
		}
	}
	if err := s.approvals.Complete(ctx, ownerID, req.ID, approval.JSON{
		"approved": approve,
		"answer":   final,
	}); err != nil {
		return nil, err
	}
	return s.approvals.Get(ctx, ownerID, req.ID)
}
