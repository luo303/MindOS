package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrInvalid 表示不支持的类型、值或操作。
	ErrInvalid = errors.New("invalid preference")
	// ErrNotFound 对越权与不存在返回同一结果。
	ErrNotFound = errors.New("preference not found")
	// ErrConflict 表示当前值已变化或候选已处理。
	ErrConflict = errors.New("preference changed")
)

// Service 实施偏好和候选的业务规则。
type Service struct{ repo *Repository }

// NewService 创建长期记忆服务。
func NewService(repo *Repository) *Service { return &Service{repo: repo} }

// List 返回账号的当前值，含归档项。
func (service *Service) List(ctx context.Context, ownerID int64) ([]Memory, error) {
	return service.repo.list(ctx, ownerID)
}

// Context 将最多三个已生效项转为固定模板，不把原文作为指令注入。
func (service *Service) Context(ctx context.Context, ownerID int64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	items, err := service.List(ctx, ownerID)
	if err != nil {
		return "", fmt.Errorf("load preference context: %w", err)
	}
	return preferenceContext(items), nil
}

// Create 手动创建尚不存在的偏好键。
func (service *Service) Create(ctx context.Context, ownerID int64, key, value string) (Memory, error) {
	if aliases(key, value) == nil {
		return Memory{}, ErrInvalid
	}
	return service.repo.manual(ctx, ownerID, mutation{key: key, value: &value})
}

// Update 在给定版本上编辑、归档或恢复，不允许改键。
func (service *Service) Update(ctx context.Context, ownerID, id int64, expected int, value *string, status string) (Memory, error) {
	if id <= 0 || expected <= 0 || (status == "" && value == nil) || (status != "" && status != archived && status != active) {
		return Memory{}, ErrInvalid
	}
	return service.repo.manual(ctx, ownerID, mutation{id: id, value: value, status: status, expected: expected})
}

// Versions 分页返回修订快照，before=0 时从最新开始。
func (service *Service) Versions(ctx context.Context, ownerID, id int64, before int) ([]Version, error) {
	return service.repo.versions(ctx, ownerID, id, before)
}

// Candidates 返回待确认候选；未指定会话时返回整个账号的收件箱。
func (service *Service) Candidates(ctx context.Context, ownerID, conversationID int64) ([]Candidate, error) {
	return service.repo.candidates(ctx, ownerID, conversationID)
}

// Decide 处理候选；value 可为空，表示接受原值。
func (service *Service) Decide(ctx context.Context, ownerID, id int64, decision string, value *string) error {
	if decision != "accept" && decision != "reject" {
		return ErrInvalid
	}
	return service.repo.accept(ctx, ownerID, id, decision == "accept", value)
}

// StoreProposal 在校验模型结果后仅保存候选；无候选时不写任何数据。
func (service *Service) StoreProposal(ctx context.Context, event ExtractEvent, sourceAt time.Time, source, output string) error {
	result := decodeProposal(source, output)
	if result == nil {
		return nil
	}
	return service.repo.addCandidate(ctx, Candidate{
		OwnerID: event.OwnerID, ConversationID: event.ConversationID, MessageID: event.MessageID,
		Key: result.Key, Value: result.Value, Evidence: result.Evidence, Status: pending,
	}, sourceAt)
}
