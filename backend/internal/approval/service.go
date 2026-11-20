package approval

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DefaultTTL 是待审批动作的保留期；用户隔天回来仍能审批。
const DefaultTTL = 7 * 24 * time.Hour

type Service struct {
	repo *Repository
	ttl  time.Duration
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo, ttl: DefaultTTL}
}

type ProposeInput struct {
	OwnerID        int64
	ConversationID int64
	UserMessageID  int64
	ChatModelID    int64
	CheckpointID   string
	ToolName       string
	Arguments      JSON
	Summary        string
}

func (s *Service) Propose(ctx context.Context, in ProposeInput) (*Request, error) {
	if strings.TrimSpace(in.ToolName) == "" {
		return nil, fmt.Errorf("approval tool name is required")
	}
	if strings.TrimSpace(in.Summary) == "" {
		return nil, fmt.Errorf("approval summary is required")
	}
	if in.Arguments == nil {
		in.Arguments = JSON{}
	}
	if in.ConversationID <= 0 {
		return nil, fmt.Errorf("approval requires a conversation")
	}
	pending, err := s.repo.HasPending(ctx, in.OwnerID, in.ConversationID)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, ErrPending
	}
	req := &Request{
		OwnerID:   in.OwnerID,
		ToolName:  in.ToolName,
		Arguments: in.Arguments,
		Summary:   strings.TrimSpace(in.Summary),
		Status:    StatusPending,
		ExpiresAt: time.Now().UTC().Add(s.ttl),
		Result:    JSON{},
	}
	if in.CheckpointID != "" {
		req.CheckpointID = &in.CheckpointID
	}
	if in.ConversationID > 0 {
		req.ConversationID = &in.ConversationID
	}
	if in.UserMessageID > 0 {
		req.UserMessageID = &in.UserMessageID
	}
	if in.ChatModelID > 0 {
		req.ChatModelID = &in.ChatModelID
	}
	if err := s.repo.Create(ctx, req); err != nil {
		return nil, err
	}
	return req, nil
}

func (s *Service) SetInterrupt(ctx context.Context, checkpointID, interruptID string) error {
	return s.repo.SetInterrupt(ctx, checkpointID, interruptID)
}

func (s *Service) HasPending(ctx context.Context, ownerID, conversationID int64) (bool, error) {
	return s.repo.HasPending(ctx, ownerID, conversationID)
}

func (s *Service) GetByCheckpoint(ctx context.Context, checkpointID string) (*Request, error) {
	return s.repo.GetByCheckpoint(ctx, checkpointID)
}

func (s *Service) ListByConversation(ctx context.Context, ownerID, conversationID int64, status string) ([]Request, error) {
	return s.repo.ListByConversation(ctx, ownerID, conversationID, status)
}

func (s *Service) ListPending(ctx context.Context, ownerID, conversationID int64) ([]Request, error) {
	return s.repo.ListPending(ctx, ownerID, conversationID)
}

func (s *Service) Get(ctx context.Context, ownerID, id int64) (*Request, error) {
	return s.repo.Get(ctx, ownerID, id)
}

func (s *Service) Decide(ctx context.Context, ownerID, id int64, approve bool) (*Request, error) {
	status := StatusRejected
	if approve {
		status = StatusApproved
	}
	return s.repo.Decide(ctx, ownerID, id, status)
}

func (s *Service) Complete(ctx context.Context, ownerID, id int64, result JSON) error {
	return s.repo.Complete(ctx, ownerID, id, StatusCompleted, result)
}

func (s *Service) Fail(ctx context.Context, ownerID, id int64, result JSON) error {
	return s.repo.Complete(ctx, ownerID, id, StatusFailed, result)
}
