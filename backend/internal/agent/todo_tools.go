package agent

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/todo"
)

type listTodosInput struct {
	Status string `json:"status,omitempty" jsonschema:"description=可选状态：open 待处理 / in_progress 进行中 / done 已完成 / cancelled 已取消。省略则不按状态筛选"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=可选返回条数，默认 10，最多 20"`
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
	return utils.InferTool("list_todos", "查看当前用户的待办，可按状态筛选，返回 ID 和进度。无标题搜索或分页；有限列表不代表全量。", invoke)
}

type createTodoInput struct {
	Title       string `json:"title" jsonschema:"description=待办标题，必填，不超过 200 字"`
	Description string `json:"description,omitempty" jsonschema:"description=可选说明，按用户给出的要求补充背景或验收条件"`
	Priority    int    `json:"priority,omitempty" jsonschema:"description=可选优先级 1 高 / 2 中 / 3 低，省略则默认 2"`
	DueAt       string `json:"due_at,omitempty" jsonschema:"description=可选截止时间，用 RFC3339 且带时区，例如 2026-11-25T18:00:00+08:00。没有明确时间或时区时不要猜"`
}

type pendingApprovalOutput struct {
	Status     string `json:"status"`
	ApprovalID int64  `json:"approval_id"`
	Summary    string `json:"summary"`
}

func init() {
	gob.Register(todoApprovalState{})
	gob.Register(todoApprovalInfo{})
}

type todoApprovalState struct {
	ApprovalID int64
}

type todoApprovalInfo struct {
	ApprovalID int64  `json:"approval_id"`
	Summary    string `json:"summary"`
}

// newCreateTodoTool 第一次调用 StatefulInterrupt 暂停 Agent；审批后
// Resume 会再次进入这里，只有 approved 才真正写 todos。
func newCreateTodoTool(svc *todo.Service, approvals *approval.Service, ownerID int64, budget *toolBudget) (tool.InvokableTool, error) {
	invoke := func(ctx context.Context, in createTodoInput) (string, error) {
		if err := budget.take(); err != nil {
			return "", err
		}
		if approvals == nil {
			return "", errors.New("todo approval is not configured")
		}
		meta := RunMetaFrom(ctx)
		if meta.CheckpointID == "" {
			return "", errors.New("todo write requires a checkpoint")
		}
		if meta.ConversationID <= 0 {
			return "", errors.New("creating a todo requires an active conversation")
		}

		wasInterrupted, _, state := tool.GetInterruptState[todoApprovalState](ctx)
		if !wasInterrupted {
			title := strings.TrimSpace(in.Title)
			if title == "" {
				return "", errors.New("title is required")
			}
			if in.Priority == 0 {
				in.Priority = 2
			}
			if in.Priority < 1 || in.Priority > 3 {
				return "", errors.New("priority must be 1, 2 or 3")
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
					return "", fmt.Errorf("due_at must be RFC3339: %w", err)
				}
				args["due_at"] = due.UTC().Format(time.RFC3339)
				summary += "，截止 " + due.Format("2006-01-02 15:04")
			}
			req, err := approvals.Propose(ctx, approval.ProposeInput{
				OwnerID: ownerID, CheckpointID: meta.CheckpointID,
				ConversationID: meta.ConversationID, UserMessageID: meta.UserMessageID,
				ChatModelID: meta.ChatModelID, ToolName: "create_todo",
				Arguments: args, Summary: summary,
			})
			if errors.Is(err, approval.ErrPending) {
				// 同一个 assistant message 里可能有重复工具调用；第一次已经创建
				// pending，第二次不能把整个 ToolsNode 抛成 NodeRunError。
				return "当前会话已有待审批操作，请等待用户处理后继续。", nil
			}
			if err != nil {
				return "", err
			}
			return "", tool.StatefulInterrupt(ctx,
				todoApprovalInfo{ApprovalID: req.ID, Summary: req.Summary},
				todoApprovalState{ApprovalID: req.ID})
		}

		isTarget, hasData, decision := tool.GetResumeContext[map[string]any](ctx)
		if !isTarget {
			return "", tool.StatefulInterrupt(ctx, todoApprovalInfo{ApprovalID: state.ApprovalID}, state)
		}
		approved := hasData && decision["approved"] == true
		if !approved {
			return "用户拒绝创建待办。", nil
		}
		req, err := approvals.Get(ctx, ownerID, state.ApprovalID)
		if err != nil {
			return "", err
		}
		if req.Status != approval.StatusApproved {
			return "", fmt.Errorf("approval %d is not approved", req.ID)
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
				return "", err
			}
			due = &parsed
		}
		created, err := svc.Create(ctx, ownerID, todo.CreateInput{
			Title: title, Description: description, Priority: priority, DueAt: due,
			Source:     todo.SourceAI,
			SourceRef:  todo.JSON{"approval_id": req.ID, "tool": req.ToolName},
			ApprovalID: &req.ID,
		})
		if err != nil {
			return "", err
		}
		return "已创建待办：" + created.Title, nil
	}
	return utils.InferTool("create_todo", "提交新增一条待办的审批；只要计划或草稿时不用此工具。", invoke)
}

type updateTodoInput struct {
	ID          int64  `json:"id" jsonschema:"description=已确认的待办 ID。不确定时先用 list_todos 定位"`
	Title       string `json:"title,omitempty" jsonschema:"description=新标题，省略则不变"`
	Description string `json:"description,omitempty" jsonschema:"description=新说明，省略或传空字符串都不变，不支持清空"`
	Status      string `json:"status,omitempty" jsonschema:"description=新状态 open 待处理 / in_progress 进行中 / done 已完成 / cancelled 已取消"`
	Priority    int    `json:"priority,omitempty" jsonschema:"description=新优先级 1 高 / 2 中 / 3 低，省略则不变"`
}

type deleteTodoInput struct {
	IDs []int64 `json:"ids" jsonschema:"description=要删除的待办 ID 列表，支持一次删除多条；不确定时先用 list_todos 定位"`
	// ID 保留给模型/历史调用只传单条的情况。
	ID int64 `json:"id,omitempty" jsonschema:"description=兼容单条删除；新调用优先使用 ids"`
}

func newUpdateTodoTool(svc *todo.Service, approvals *approval.Service, ownerID int64, budget *toolBudget) (tool.InvokableTool, error) {
	invoke := func(ctx context.Context, in updateTodoInput) (string, error) {
		if err := budget.take(); err != nil {
			return "", err
		}
		if approvals == nil {
			return "", errors.New("todo approval is not configured")
		}
		meta := RunMetaFrom(ctx)
		if meta.CheckpointID == "" || meta.ConversationID <= 0 {
			return "", errors.New("todo write requires an active conversation checkpoint")
		}
		wasInterrupted, _, state := tool.GetInterruptState[todoApprovalState](ctx)
		if !wasInterrupted {
			if in.ID <= 0 {
				return "", errors.New("id is required")
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
			req, err := approvals.Propose(ctx, approval.ProposeInput{
				OwnerID: ownerID, CheckpointID: meta.CheckpointID,
				ConversationID: meta.ConversationID, UserMessageID: meta.UserMessageID,
				ChatModelID: meta.ChatModelID, ToolName: "update_todo",
				Arguments: args, Summary: "修改待办 #" + fmt.Sprint(in.ID),
			})
			if errors.Is(err, approval.ErrPending) {
				// 同一个 assistant message 里可能有重复工具调用；第一次已经创建
				// pending，第二次不能把整个 ToolsNode 抛成 NodeRunError。
				return "当前会话已有待审批操作，请等待用户处理后继续。", nil
			}
			if err != nil {
				return "", err
			}
			return "", tool.StatefulInterrupt(ctx,
				todoApprovalInfo{ApprovalID: req.ID, Summary: req.Summary},
				todoApprovalState{ApprovalID: req.ID})
		}
		isTarget, hasData, decision := tool.GetResumeContext[map[string]any](ctx)
		if !isTarget {
			return "", tool.StatefulInterrupt(ctx, todoApprovalInfo{ApprovalID: state.ApprovalID}, state)
		}
		if !(hasData && decision["approved"] == true) {
			return "用户拒绝修改待办。", nil
		}
		req, err := approvals.Get(ctx, ownerID, state.ApprovalID)
		if err != nil {
			return "", err
		}
		if req.Status != approval.StatusApproved {
			return "", fmt.Errorf("approval %d is not approved", req.ID)
		}
		id, _ := req.Arguments["id"].(float64)
		inUpdate := todo.UpdateInput{}
		if v, ok := req.Arguments["title"].(string); ok && v != "" {
			inUpdate.Title = &v
		}
		if v, ok := req.Arguments["description"].(string); ok && v != "" {
			inUpdate.Description = &v
		}
		if v, ok := req.Arguments["status"].(string); ok && v != "" {
			inUpdate.Status = &v
		}
		if v, ok := req.Arguments["priority"].(float64); ok && v > 0 {
			p := int(v)
			inUpdate.Priority = &p
		}
		updated, err := svc.Update(ctx, ownerID, int64(id), inUpdate)
		if err != nil {
			return "", err
		}
		return "已修改待办：" + updated.Title, nil
	}
	return utils.InferTool("update_todo", "提交一条待办的修改审批。仅传要改的字段；标记完成用 status=done，不支持改截止时间。", invoke)
}

func newDeleteTodoTool(svc *todo.Service, approvals *approval.Service, ownerID int64, budget *toolBudget) (tool.InvokableTool, error) {
	invoke := func(ctx context.Context, in deleteTodoInput) (string, error) {
		if err := budget.take(); err != nil {
			return "", err
		}
		if approvals == nil {
			return "", errors.New("todo approval is not configured")
		}
		meta := RunMetaFrom(ctx)
		if meta.CheckpointID == "" || meta.ConversationID <= 0 {
			return "", errors.New("todo write requires an active conversation checkpoint")
		}
		ids := normalizeTodoIDs(in.IDs, in.ID)
		if len(ids) == 0 {
			return "", errors.New("ids are required")
		}
		wasInterrupted, _, state := tool.GetInterruptState[todoApprovalState](ctx)
		if !wasInterrupted {
			args := approval.JSON{"ids": ids}
			req, err := approvals.Propose(ctx, approval.ProposeInput{
				OwnerID: ownerID, CheckpointID: meta.CheckpointID,
				ConversationID: meta.ConversationID, UserMessageID: meta.UserMessageID,
				ChatModelID: meta.ChatModelID, ToolName: "delete_todo",
				Arguments: args, Summary: deleteSummary(ids),
			})
			if errors.Is(err, approval.ErrPending) {
				// 同一个 assistant message 里可能有重复工具调用；第一次已经创建
				// pending，第二次不能把整个 ToolsNode 抛成 NodeRunError。
				return "当前会话已有待审批操作，请等待用户处理后继续。", nil
			}
			if err != nil {
				return "", err
			}
			return "", tool.StatefulInterrupt(ctx,
				todoApprovalInfo{ApprovalID: req.ID, Summary: req.Summary},
				todoApprovalState{ApprovalID: req.ID})
		}
		isTarget, hasData, decision := tool.GetResumeContext[map[string]any](ctx)
		if !isTarget {
			return "", tool.StatefulInterrupt(ctx, todoApprovalInfo{ApprovalID: state.ApprovalID}, state)
		}
		if !(hasData && decision["approved"] == true) {
			return "用户拒绝删除待办。", nil
		}
		req, err := approvals.Get(ctx, ownerID, state.ApprovalID)
		if err != nil {
			return "", err
		}
		if req.Status != approval.StatusApproved {
			return "", fmt.Errorf("approval %d is not approved", req.ID)
		}
		approvedIDs := approvalTodoIDs(req.Arguments)
		if len(approvedIDs) == 0 {
			return "", errors.New("approval has no todo ids")
		}
		deleted, err := svc.DeleteMany(ctx, ownerID, approvedIDs)
		if err != nil {
			return "", err
		}
		if deleted == 0 {
			return "没有可删除的待办。", nil
		}
		return fmt.Sprintf("已删除 %d 条待办。", deleted), nil
	}
	return utils.InferTool("delete_todo", "提交删除一条或多条待办的审批。参数用 ids 数组；完成或取消应改状态，不删除。", invoke)
}

func normalizeTodoIDs(ids []int64, legacy int64) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(ids)+1)
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 && legacy > 0 {
		out = append(out, legacy)
	}
	return out
}

// approvalTodoIDs 兼容旧的 {"id": 1} 和新的 {"ids": [1,2]}。
func approvalTodoIDs(args approval.JSON) []int64 {
	out := make([]int64, 0, 4)
	if raw, ok := args["ids"].([]any); ok {
		for _, item := range raw {
			if v, ok := item.(float64); ok && v > 0 {
				out = append(out, int64(v))
			}
		}
	}
	if len(out) == 0 {
		if v, ok := args["id"].(float64); ok && v > 0 {
			out = append(out, int64(v))
		}
	}
	return out
}

func deleteSummary(ids []int64) string {
	if len(ids) == 1 {
		return "删除待办 #" + fmt.Sprint(ids[0])
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, "#"+fmt.Sprint(id))
	}
	summary := "删除待办 " + strings.Join(parts, "、")
	if len([]rune(summary)) > 160 {
		return "删除 " + fmt.Sprint(len(ids)) + " 条待办"
	}
	return summary
}
