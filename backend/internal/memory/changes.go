package memory

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func sameMetadata(first, second Metadata) bool {
	return first.Category == second.Category && first.Scope == second.Scope && first.Pinned == second.Pinned && ((first.ExpiresAt == nil && second.ExpiresAt == nil) || (first.ExpiresAt != nil && second.ExpiresAt != nil && first.ExpiresAt.Equal(*second.ExpiresAt)))
}

type extractionReceipt struct {
	OwnerID   int64
	MessageID int64
}

func (extractionReceipt) TableName() string { return "memory_extractions" }

func (repo *Repository) extracted(ctx context.Context, event ExtractEvent) (bool, error) {
	var count int64
	err := repo.db.WithContext(ctx).Model(&extractionReceipt{}).Where("owner_id = ? AND message_id = ?", event.OwnerID, event.MessageID).Count(&count).Error
	return count > 0, err
}

func (repo *Repository) related(ctx context.Context, ownerID int64, query string, limit int) ([]Memory, error) {
	var items []Memory
	err := repo.db.WithContext(ctx).Select(memoryColumns).Where("owner_id = ? AND status = ? AND (expires_at IS NULL OR expires_at > now())", ownerID, active).Order("pinned DESC, updated_at DESC, id DESC").Limit(300).Find(&items).Error
	if err != nil {
		return nil, err
	}
	return relatedMemories(items, query, time.Now().UTC(), limit), nil
}

func applyCandidate(tx *gorm.DB, candidate Candidate, value string, automatic bool) error {
	var id int64
	if candidate.TargetID != nil {
		id = *candidate.TargetID
	}
	if candidate.Action == "reinforce" {
		var current Memory
		if err := tx.Select(memoryColumns).Where("owner_id = ? AND id = ?", candidate.OwnerID, id).Take(&current).Error; err != nil {
			return lookupError(err)
		}
		if current.Version != candidate.BaseVersion || !usable(current, time.Now().UTC()) {
			return ErrConflict
		}
		return addEvidence(tx, current, candidate)
	}
	origin := Version{Source: "chat", MessageID: &candidate.MessageID, ConversationID: &candidate.ConversationID, Automatic: automatic}
	change := mutation{id: id, key: candidate.Key, value: &value, status: active, expected: candidate.BaseVersion, meta: &candidate.Metadata}
	item, err := changeMemory(tx, candidate.OwnerID, change, origin)
	if err != nil {
		return err
	}
	return addEvidence(tx, item, candidate)
}

func addEvidence(tx *gorm.DB, item Memory, candidate Candidate) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Evidence{OwnerID: item.OwnerID, MemoryID: item.ID, Version: item.Version, MessageID: candidate.MessageID, ConversationID: candidate.ConversationID, Excerpt: candidate.Evidence}).Error
}

func (repo *Repository) storeProposals(ctx context.Context, event ExtractEvent, sourceAt time.Time, proposals []openProposal, related []Memory) error {
	// 模型可指向的目标限于实际提供过的 ID/版本，不信任模型生成的 ID。
	allowed := make(map[int64]int, len(related))
	for _, item := range related {
		allowed[item.ID] = item.Version
	}
	return repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockSettings(tx, event.OwnerID); err != nil {
			return err
		}
		settings, err := readSettings(tx, event.OwnerID, event.ConversationID)
		if err != nil {
			return err
		}
		if settings.Mode == "off" || settings.ConversationOff {
			return nil
		}
		receipt := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&extractionReceipt{OwnerID: event.OwnerID, MessageID: event.MessageID})
		if receipt.Error != nil {
			return receipt.Error
		}
		if receipt.RowsAffected == 0 {
			return nil
		}
		for _, proposal := range proposals {
			candidate := proposal.Candidate
			candidate.OwnerID, candidate.MessageID, candidate.ConversationID = event.OwnerID, event.MessageID, event.ConversationID
			candidate.Status = pending
			query := tx.Select(memoryColumns).Where("owner_id = ?", event.OwnerID)
			if candidate.TargetID != nil {
				if allowed[*candidate.TargetID] != candidate.BaseVersion {
					continue
				}
				query = query.Where("id = ?", *candidate.TargetID)
			} else {
				query = query.Where("key = ? OR (lower(value) = lower(?) AND scope = ?)", candidate.Key, candidate.Value, candidate.Scope)
			}
			var current Memory
			err := query.Take(&current).Error
			if err == nil {
				if !usable(current, time.Now().UTC()) || (candidate.TargetID != nil && current.Version != candidate.BaseVersion) || current.UpdatedAt.After(sourceAt) {
					continue
				}
				candidate.Key = current.Key
				candidate.TargetID = &current.ID
				candidate.BaseVersion = current.Version
				if candidate.Action == "reinforce" || (normalize(current.Value) == normalize(candidate.Value) && current.Scope == candidate.Scope) {
					if proposal.Certainty == "explicit" && normalize(current.Value) == normalize(candidate.Value) {
						if err := addEvidence(tx, current, candidate); err != nil {
							return err
						}
					}
					continue
				}
				candidate.Action = "update"
				candidate.Pinned = current.Pinned && candidate.Scope == ""
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			} else if candidate.TargetID != nil {
				continue
			}
			var rejected int64
			if err := tx.Model(&Candidate{}).Where("owner_id = ? AND key = ? AND value = ? AND base_version = ? AND status = 'rejected'", event.OwnerID, candidate.Key, candidate.Value, candidate.BaseVersion).Count(&rejected).Error; err != nil {
				return err
			}
			if rejected > 0 {
				continue
			}
			if settings.Mode == "auto" && candidate.Action == "create" && proposal.autoEligible() && !possibleOverlap(candidate, related) {
				candidate.Status = "accepted"
			}
			insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if insert.Error != nil {
				return insert.Error
			}
			if insert.RowsAffected == 0 {
				continue
			}
			if candidate.Status == "accepted" {
				if err := applyCandidate(tx, candidate, candidate.Value, true); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// possibleOverlap 是保守的冲突保护：相同类别和范围的高度重合陈述交给用户确认，
// 不凭字面相似度自动改写或合并已有事实。
func possibleOverlap(candidate Candidate, related []Memory) bool {
	terms := searchTerms(candidate.Value)
	for _, item := range related {
		if item.Category != candidate.Category || normalize(item.Scope) != normalize(candidate.Scope) {
			continue
		}
		other := searchTerms(item.Value)
		shared := 0
		for term := range terms {
			if _, ok := other[term]; ok {
				shared++
			}
		}
		if shared >= 2 && shared*2 >= min(len(terms), len(other)) {
			return true
		}
	}
	return false
}

// Evidence 返回指定条目的当前和历史来源；删除来源消息后不再显示摘录。
func (service *Service) Evidence(ctx context.Context, ownerID, id int64) ([]Evidence, error) {
	rows := make([]Evidence, 0)
	err := service.repo.db.WithContext(ctx).Select("memory_evidence.memory_id, memory_evidence.version, memory_evidence.message_id, memory_evidence.conversation_id, memory_evidence.excerpt").Joins("JOIN memories ON memories.id = memory_evidence.memory_id AND memories.owner_id = memory_evidence.owner_id").Where("memory_evidence.owner_id = ? AND memory_evidence.memory_id = ?", ownerID, id).Order("version DESC, message_id DESC").Limit(100).Find(&rows).Error
	return rows, err
}
