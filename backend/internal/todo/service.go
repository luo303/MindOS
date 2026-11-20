package todo

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxTitleRunes       = 200
	maxDescriptionRunes = 5000
)

var (
	ErrInvalidTitle       = errors.New("todo title is required")
	ErrTitleTooLong       = errors.New("todo title is too long")
	ErrDescriptionTooLong = errors.New("todo description is too long")
	ErrInvalidPriority    = errors.New("todo priority must be 1, 2 or 3")
	ErrInvalidStatus      = errors.New("todo status is invalid")
)

// Service 是待办业务层。手动 API 与 Agent 工具都调它。
type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

type CreateInput struct {
	Title       string
	Description string
	Priority    int
	DueAt       *time.Time
	Source      string
	SourceRef   JSON
	ApprovalID  *int64
}

func (s *Service) Create(ctx context.Context, ownerID int64, in CreateInput) (*Todo, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, ErrInvalidTitle
	}
	if utf8.RuneCountInString(title) > maxTitleRunes {
		return nil, ErrTitleTooLong
	}
	if utf8.RuneCountInString(in.Description) > maxDescriptionRunes {
		return nil, ErrDescriptionTooLong
	}
	if in.Priority == 0 {
		in.Priority = 2
	}
	if in.Priority < 1 || in.Priority > 3 {
		return nil, ErrInvalidPriority
	}
	source := in.Source
	if source == "" {
		source = SourceManual
	}
	if source != SourceManual && source != SourceAI {
		return nil, errors.New("todo source is invalid")
	}
	if in.SourceRef == nil {
		in.SourceRef = JSON{}
	}
	t := &Todo{
		OwnerID:     ownerID,
		Title:       title,
		Description: strings.TrimSpace(in.Description),
		Status:      StatusOpen,
		Priority:    in.Priority,
		DueAt:       in.DueAt,
		Source:      source,
		SourceRef:   in.SourceRef,
		ApprovalID:  in.ApprovalID,
	}
	if err := s.repo.Create(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

type UpdateInput struct {
	Title       *string
	Description *string
	Status      *string
	Priority    *int
	DueAt       *time.Time
	ClearDue    bool
}

func (s *Service) Update(ctx context.Context, ownerID, id int64, in UpdateInput) (*Todo, error) {
	updates := map[string]any{}
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			return nil, ErrInvalidTitle
		}
		if utf8.RuneCountInString(title) > maxTitleRunes {
			return nil, ErrTitleTooLong
		}
		updates["title"] = title
	}
	if in.Description != nil {
		if utf8.RuneCountInString(*in.Description) > maxDescriptionRunes {
			return nil, ErrDescriptionTooLong
		}
		updates["description"] = strings.TrimSpace(*in.Description)
	}
	if in.Priority != nil {
		if *in.Priority < 1 || *in.Priority > 3 {
			return nil, ErrInvalidPriority
		}
		updates["priority"] = *in.Priority
	}
	if in.Status != nil {
		switch *in.Status {
		case StatusOpen, StatusInProgress, StatusDone, StatusCancelled:
		default:
			return nil, ErrInvalidStatus
		}
		updates["status"] = *in.Status
		if *in.Status == StatusDone {
			updates["completed_at"] = time.Now().UTC()
		} else {
			updates["completed_at"] = nil
		}
	}
	if in.ClearDue {
		updates["due_at"] = nil
	} else if in.DueAt != nil {
		updates["due_at"] = *in.DueAt
	}
	return s.repo.Update(ctx, ownerID, id, updates)
}

func (s *Service) Get(ctx context.Context, ownerID, id int64) (*Todo, error) {
	return s.repo.Get(ctx, ownerID, id)
}

func (s *Service) List(ctx context.Context, ownerID int64, f Filter) ([]Todo, error) {
	if f.Status != "" {
		switch f.Status {
		case StatusOpen, StatusInProgress, StatusDone, StatusCancelled:
		default:
			return nil, ErrInvalidStatus
		}
	}
	return s.repo.List(ctx, ownerID, f)
}

func (s *Service) Delete(ctx context.Context, ownerID, id int64) error {
	return s.repo.SoftDelete(ctx, ownerID, id)
}
