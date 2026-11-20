package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/todo"
)

const todoInstruction = `
你可以查询待办；创建/修改/删除待办必须分别调用 create_todo / update_todo / delete_todo。
create_todo 只会生成“待审批”提案，不会立即落库。工具返回 pending_approval 时，
如实告诉用户“已提交审批，需要确认”，不要声称已经创建成功。
`

type listTodosInput struct {
	Status string `json:"status" jsonschema:"description=可选：open/in_progress/done/cancelled"`
	Limit  int    `json:"limit" jsonschema:"description=返回条数 1-20，默认 10"`
}

type todoOutput struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	DueAt       string `json:"due_at,omitempty"`
	Source      string `json:"source"`
}

func newListTodosTool(svc *todo.Service, ownerID int64, budget *toolBudget) (tool.InvokableTool, error) {
	invoke := func(ctx context.Context, in listTodosInput) ([]todoOutput, error) {
		if err := budget.take(); err != nil {
			return nil, err
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}
		if limit > 20 {
			limit = 20
		}
		items, err := svc.List(ctx, ownerID, todo.Filter{Status: in.Status, Limit: limit})
		if err != nil {
			return nil, err
		}
		out := make([]todoOutput, 0, len(items))
		for _, t := range items {
			item := todoOutput{
				ID: t.ID, Title: t.Title, Description: t.Description,
				Status: t.Status, Priority: t.Priority, Source: t.Source,
			}
			if t.DueAt != nil {
				item.DueAt = t.DueAt.Format(time.RFC3339)
			}
			out = append(out, item)
		}
		return out, nil
	}
	return utils.InferTool("list_todos", "查询当前用户的待办列表", invoke)
}

type createTodoInput struct {
	Title       string `json:"title" jsonschema:"description=待办标题，必填，不超过 200 字"`
	Description string `json:"description" jsonschema:"description=可选说明"`
	Priority    int    `json:"priority" jsonschema:"description=优先级 1 高 / 2 中 / 3 低，默认 2"`
	DueAt       string `json:"due_at" jsonschema:"description=可选的截止时间，RFC3339，例如 2026-11-25T18:00:00+08:00"`
}

type pendingApprovalOutput struct {
	Status     string `json:"status"`
	ApprovalID int64  `json:"approval_id"`
	Summary    string `json:"summary"`
}

// newCreateTodoTool 只创建审批提案，不直接写 todos。
// 用户批准后由 approval API 执行；模型无法绕过人工确认。
func newCreateTodoTool(svc *todo.Service, approvals *approval.Service, ownerID int64, budget *toolBudget) (tool.InvokableTool, error) {
	invoke := func(ctx context.Context, in createTodoInput) (*pendingApprovalOutput, error) {
		if err := budget.take(); err != nil {
			return nil, err
		}
		if approvals == nil {
			return nil, errors.New("todo approval is not configured")
		}
		meta := RunMetaFrom(ctx)
		if meta.ConversationID <= 0 {
			return nil, errors.New("creating a todo requires an active conversation")
		}
		title := strings.TrimSpace(in.Title)
		if title == "" {
			return nil, errors.New("title is required")
		}
		if in.Priority == 0 {
			in.Priority = 2
		}
		if in.Priority < 1 || in.Priority > 3 {
			return nil, errors.New("priority must be 1, 2 or 3")
		}
		args := approval.JSON{
			"title":       title,
			"description": strings.TrimSpace(in.Description),
			"priority":    in.Priority,
		}
		summary := "创建待办：" + title
		if strings.TrimSpace(in.DueAt) != "" {
			due, err := time.Parse(time.RFC3339, in.DueAt)
			if err != nil {
				return nil, fmt.Errorf("due_at must be RFC3339: %w", err)
			}
			args["due_at"] = due.UTC().Format(time.RFC3339)
			summary += "，截止 " + due.Format("2006-01-02 15:04")
		}
		req, err := approvals.Propose(ctx, approval.ProposeInput{
			OwnerID:        ownerID,
			ConversationID: meta.ConversationID,
			UserMessageID:  meta.UserMessageID,
			ChatModelID:    meta.ChatModelID,
			ToolName:       "create_todo",
			Arguments:      args,
			Summary:        summary,
		})
		if err != nil {
			return nil, err
		}
		return &pendingApprovalOutput{
			Status:     "pending_approval",
			ApprovalID: req.ID,
			Summary:    req.Summary,
		}, nil
	}
	return utils.InferTool("create_todo", "创建待办提案，必须等待用户审批后才会真正落库", invoke)
}

type updateTodoInput struct {
	ID          int64  `json:"id" jsonschema:"description=待办 ID"`
	Title       string `json:"title,omitempty" jsonschema:"description=新标题，空字符串表示不改"`
	Description string `json:"description,omitempty" jsonschema:"description=新描述，空字符串表示不改"`
	Status      string `json:"status,omitempty" jsonschema:"description=新状态 open/in_progress/done/cancelled"`
	Priority    int    `json:"priority,omitempty" jsonschema:"description=新优先级 1-3，0 表示不改"`
}

type deleteTodoInput struct {
	ID int64 `json:"id" jsonschema:"description=待办 ID"`
}

func newUpdateTodoTool(approvals *approval.Service, ownerID int64, budget *toolBudget) (tool.InvokableTool, error) {
	invoke := func(ctx context.Context, in updateTodoInput) (*pendingApprovalOutput, error) {
		if err := budget.take(); err != nil {
			return nil, err
		}
		if in.ID <= 0 {
			return nil, errors.New("id is required")
		}
		args := approval.JSON{"id": in.ID}
		if in.Title != "" {
			args["title"] = in.Title
		}
		if in.Description != "" {
			args["description"] = in.Description
		}
		if in.Status != "" {
			args["status"] = in.Status
		}
		if in.Priority != 0 {
			args["priority"] = in.Priority
		}
		meta := RunMetaFrom(ctx)
		req, err := approvals.Propose(ctx, approval.ProposeInput{
			OwnerID: ownerID, ConversationID: meta.ConversationID,
			UserMessageID: meta.UserMessageID, ChatModelID: meta.ChatModelID,
			ToolName: "update_todo", Arguments: args,
			Summary: "修改待办 #" + fmt.Sprint(in.ID),
		})
		if err != nil {
			return nil, err
		}
		return &pendingApprovalOutput{Status: "pending_approval", ApprovalID: req.ID, Summary: req.Summary}, nil
	}
	return utils.InferTool("update_todo", "修改待办提案，必须等待用户审批", invoke)
}

func newDeleteTodoTool(approvals *approval.Service, ownerID int64, budget *toolBudget) (tool.InvokableTool, error) {
	invoke := func(ctx context.Context, in deleteTodoInput) (*pendingApprovalOutput, error) {
		if err := budget.take(); err != nil {
			return nil, err
		}
		if in.ID <= 0 {
			return nil, errors.New("id is required")
		}
		meta := RunMetaFrom(ctx)
		req, err := approvals.Propose(ctx, approval.ProposeInput{
			OwnerID: ownerID, ConversationID: meta.ConversationID,
			UserMessageID: meta.UserMessageID, ChatModelID: meta.ChatModelID,
			ToolName: "delete_todo", Arguments: approval.JSON{"id": in.ID},
			Summary: "删除待办 #" + fmt.Sprint(in.ID),
		})
		if err != nil {
			return nil, err
		}
		return &pendingApprovalOutput{Status: "pending_approval", ApprovalID: req.ID, Summary: req.Summary}, nil
	}
	return utils.InferTool("delete_todo", "删除待办提案，必须等待用户审批", invoke)
}
