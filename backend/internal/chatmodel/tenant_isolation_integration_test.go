package chatmodel

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/testsupport"
)

// 对话模型配置是"每个人的 Key"：B 不能看到、不能改、不能借 A 的配置花钱。
// 用真库是因为唯一索引（owner 前缀）与 AAD 绑定都必须落在地上验证。
func TestChatModelIsolationIntegration(t *testing.T) {
	db, owner := setupChatModelDB(t)
	other := testsupport.NewUser(t, db, fmt.Sprintf("other-%d@test.local", time.Now().UnixNano()))
	cipher, err := NewCipher(testKey(5))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	svc := NewService(NewRepository(db), cipher, nil)
	ctx := context.Background()

	view, err := svc.Create(ctx, owner, CreateInput{
		Name: "默认", BaseURL: "https://a/v1", Model: "m", APIKey: "sk-owner-secret",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !view.IsDefault {
		t.Fatal("the first config should be the default")
	}

	// B 的视角：一条都没有。
	items, err := svc.List(ctx, other)
	if err != nil || len(items) != 0 {
		t.Fatalf("other list = %v, %v; want empty", items, err)
	}
	if _, err := svc.Resolve(ctx, other, nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("other resolve err = %v, want ErrNotConfigured", err)
	}

	// 直接拿 id 访问也是"不存在"：不能让 B 通过 id 探测出 A 有几条配置。
	if _, err := svc.Resolve(ctx, other, &view.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other resolve by id err = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, other, view.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other delete err = %v, want ErrNotFound", err)
	}
	if _, err := svc.SetDefault(ctx, other, view.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other set default err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Update(ctx, other, view.ID, UpdateInput{
		Name: "改名", BaseURL: "https://b/v1", Model: "m",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other update err = %v, want ErrNotFound", err)
	}
	// 确认 A 的配置确实没被动过。
	if got, err := svc.Resolve(ctx, owner, &view.ID); err != nil || got.Name != "默认" {
		t.Fatalf("owner config changed: %+v, %v", got, err)
	}

	// 同名配置：唯一索引带 owner，两个人可以各有一条"默认"。
	if _, err := svc.Create(ctx, other, CreateInput{
		Name: "默认", BaseURL: "https://b/v1", Model: "m", APIKey: "sk-other-secret",
	}); err != nil {
		t.Fatalf("same name for another owner should be allowed: %v", err)
	}
	otherItems, err := svc.List(ctx, other)
	if err != nil || len(otherItems) != 1 || !otherItems[0].IsDefault {
		t.Fatalf("other list = %+v, %v", otherItems, err)
	}
}

// 密文与归属绑定：把 A 的密文搬到 B 的行上，B 得到的是"解不开"，
// 而不是悄悄用上 A 的 Key。这是 AAD 唯一能守住、查询条件守不住的事故。
func TestCiphertextCannotBeMovedBetweenOwnersIntegration(t *testing.T) {
	db, owner := setupChatModelDB(t)
	other := testsupport.NewUser(t, db, fmt.Sprintf("moved-%d@test.local", time.Now().UnixNano()))
	cipher, err := NewCipher(testKey(5))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	svc := NewService(NewRepository(db), cipher, nil)
	ctx := context.Background()

	created, err := svc.Create(ctx, owner, CreateInput{
		Name: "原配置", BaseURL: "https://a/v1", Model: "m", APIKey: "sk-owner-secret",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var cipherRow struct {
		APIKeyCipher []byte
	}
	if err := db.Raw("SELECT api_key_cipher FROM chat_models WHERE id = ?", created.ID).Scan(&cipherRow).Error; err != nil {
		t.Fatalf("read cipher: %v", err)
	}
	cipherBytes := cipherRow.APIKeyCipher
	// 模拟运维手工把这一行改成 B 的：owner_id 变了，密文没变。
	var movedID int64
	if err := db.Raw(
		`INSERT INTO chat_models (owner_id, name, base_url, model, api_key_cipher, api_key_hint)
		 VALUES (?, '搬来的配置', 'https://a/v1', 'm', ?, 'cret') RETURNING id`,
		other, cipherBytes).Scan(&movedID).Error; err != nil {
		t.Fatalf("insert moved row: %v", err)
	}

	if _, err := svc.Resolve(ctx, other, &movedID); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("moved ciphertext err = %v, want ErrDecryptFailed", err)
	}
	// 原主人的行仍然能解开。
	if resolved, err := svc.Resolve(ctx, owner, &created.ID); err != nil || resolved.APIKey != "sk-owner-secret" {
		t.Fatalf("owner resolve = %+v, %v", resolved, err)
	}
}
