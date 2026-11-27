package memory

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Settings 是用户当前的记忆模式；默认自动，对话可单独关闭。
type Settings struct {
	OwnerID         int64  `json:"-" gorm:"primaryKey"`
	Mode            string `json:"mode"`
	ConversationOff bool   `json:"conversation_off" gorm:"-"`
}

// TableName 显式映射设置表。
func (Settings) TableName() string { return "memory_settings" }

type conversationSetting struct {
	OwnerID        int64
	ConversationID int64
}

func (conversationSetting) TableName() string { return "memory_conversation_settings" }

func lockSettings(tx *gorm.DB, ownerID int64) error {
	if ownerID <= 0 {
		return ErrNotFound
	}
	// 同一账号的模式变更、自动提交和手动修改共用行锁；模型调用在锁外。
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Settings{OwnerID: ownerID, Mode: "auto"}).Error; err != nil {
		return err
	}
	var settings Settings
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_id = ?", ownerID).Take(&settings).Error
}

func readSettings(tx *gorm.DB, ownerID, conversationID int64) (Settings, error) {
	settings := Settings{OwnerID: ownerID, Mode: "auto"}
	err := tx.Select("owner_id, mode").Where("owner_id = ?", ownerID).Take(&settings).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return Settings{}, err
	}
	if conversationID > 0 {
		var count int64
		if err := tx.Model(&conversationSetting{}).Where("owner_id = ? AND conversation_id = ?", ownerID, conversationID).Count(&count).Error; err != nil {
			return Settings{}, err
		}
		settings.ConversationOff = count > 0
	}
	return settings, nil
}

// Settings 返回账号模式及可选会话覆写。
func (service *Service) Settings(ctx context.Context, ownerID, conversationID int64) (Settings, error) {
	return readSettings(service.repo.db.WithContext(ctx), ownerID, conversationID)
}

// SetSettings 更新模式；关闭会话记忆时，未完成的后台任务也不能写入。
func (service *Service) SetSettings(ctx context.Context, ownerID, conversationID int64, mode string, off *bool) (Settings, error) {
	if mode != "" && mode != "auto" && mode != "suggest" && mode != "off" || (off != nil && conversationID <= 0) || (mode == "" && off == nil) {
		return Settings{}, ErrInvalid
	}
	var result Settings
	err := service.repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockSettings(tx, ownerID); err != nil {
			return err
		}
		if mode != "" {
			if err := tx.Model(&Settings{}).Where("owner_id = ?", ownerID).Update("mode", mode).Error; err != nil {
				return err
			}
		}
		if off != nil {
			if *off {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&conversationSetting{OwnerID: ownerID, ConversationID: conversationID}).Error; err != nil {
					return lookupError(err)
				}
			} else if err := tx.Where("owner_id = ? AND conversation_id = ?", ownerID, conversationID).Delete(&conversationSetting{}).Error; err != nil {
				return err
			}
		}
		var err error
		result, err = readSettings(tx, ownerID, conversationID)
		return err
	})
	return result, err
}
