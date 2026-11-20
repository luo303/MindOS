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
	req := &Request{
		OwnerID:   in.OwnerID,
		ToolName:  in.ToolName,
		Arguments: in.Arguments,
		Summary:   strings.TrimSpace(in.Summary),
		Status:    StatusPending,
		ExpiresAt: time.Now().UTC().Add(s.ttl),
		Result:    JSON{},
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
