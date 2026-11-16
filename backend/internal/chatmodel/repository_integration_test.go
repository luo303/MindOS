package chatmodel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/migrations"
)

// 对话模型配置的规则有一半落在数据库上：唯一索引、CHECK 约束、bytea 往返。
// 这些假仓储一个都演不出来，必须对着真库跑。
//
// 运行方式与 knowledge 的集成测试一致：
//
//	MINDOS_TEST_DSN="host=localhost port=5432 user=mindos password=mindos_dev_password dbname=postgres sslmode=disable" \
//	go test -count=1 -run Integration ./internal/chatmodel/
func setupChatModelDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testsupport.NewDatabase(t, "chatmodel")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrate.New(db, migrations.FS).Up(ctx); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return testsupport.NewGorm(t, db)
}

func newTestService(t *testing.T, db *gorm.DB) *Service {
	t.Helper()
	cipher, err := NewCipher(testKey(5))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return NewService(NewRepository(db), cipher, nil)
}

func TestChatModelIntegration(t *testing.T) {
	db := setupChatModelDB(t)
	svc := newTestService(t, db)
	ctx := context.Background()

	t.Run("first config becomes the default automatically", func(t *testing.T) {
		view, err := svc.Create(ctx, CreateInput{
			Name:    "讯飞 Spark",
			BaseURL: "https://maas-api.cn-huabei-1.xf-yun.com/v2/",
			Model:   "spark-x2.6",
			APIKey:  "ak-1234567890abcdef",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !view.IsDefault {
			t.Error("the first config should become the default")
		}
		// 尾部斜杠被去掉：否则"看起来一样的两条配置"在界面上难以区分。
		if view.BaseURL != "https://maas-api.cn-huabei-1.xf-yun.com/v2" {
			t.Errorf("base_url = %q", view.BaseURL)
		}
		if view.APIKeyHint != "cdef" {
			t.Errorf("api_key_hint = %q, want the last 4 chars", view.APIKeyHint)
		}
		if !view.APIKeyConfigured {
			t.Error("api_key_configured should be true")
		}

		// 密文落库：直接读原始行确认库里没有明文。
		raw, err := NewRepository(db).Get(ctx, view.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if bytes.Contains(raw.APIKeyCipher, []byte("1234567890")) {
			t.Fatal("plaintext key found in the database column")
		}
		if len(raw.APIKeyCipher) == 0 {
			t.Fatal("ciphertext should not be empty")
		}
	})

	t.Run("name conflict is reported, not silently overwritten", func(t *testing.T) {
		_, err := svc.Create(ctx, CreateInput{
			Name:    "讯飞 Spark",
			BaseURL: "https://a/v1",
			Model:   "m",
		})
		if !errors.Is(err, ErrNameConflict) {
			t.Fatalf("err = %v, want ErrNameConflict", err)
		}
	})

	t.Run("invalid base url is rejected before touching the database", func(t *testing.T) {
		_, err := svc.Create(ctx, CreateInput{Name: "x", BaseURL: "api.example.com/v1", Model: "m"})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})

	var secondID int64
	t.Run("second config does not steal the default", func(t *testing.T) {
		view, err := svc.Create(ctx, CreateInput{
			Name:    "本地 Ollama",
			BaseURL: "http://localhost:11434/v1",
			Model:   "qwen2.5",
			APIKey:  "ollama",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if view.IsDefault {
			t.Error("a later config must not become the default automatically")
		}
		secondID = view.ID
	})

	t.Run("resolve picks the default", func(t *testing.T) {
		resolved, err := svc.Resolve(ctx, nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.Name != "讯飞 Spark" {
			t.Errorf("resolved %q, want the default", resolved.Name)
		}
		// 明文在这一层出现是设计内的：它只用来填 Authorization 头。
		if resolved.APIKey != "ak-1234567890abcdef" {
			t.Errorf("decrypted key = %q", resolved.APIKey)
		}
	})

	t.Run("set default switches atomically", func(t *testing.T) {
		if _, err := svc.SetDefault(ctx, secondID); err != nil {
			t.Fatalf("SetDefault: %v", err)
		}
		items, err := svc.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		defaults := 0
		for _, item := range items {
			if item.IsDefault {
				defaults++
				if item.ID != secondID {
					t.Errorf("default is %d, want %d", item.ID, secondID)
				}
			}
		}
		// 部分唯一索引保证最多一条；这条断言确认"清旧+置新"没有把两条都留下。
		if defaults != 1 {
			t.Fatalf("defaults = %d, want exactly 1", defaults)
		}
		// 默认项必须排在最前，下拉框才能"打开就能点发送"。
		if !items[0].IsDefault {
			t.Error("the default config should sort first")
		}
	})

	t.Run("explicit id wins over the default", func(t *testing.T) {
		items, err := svc.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var nonDefault int64
		for _, item := range items {
			if !item.IsDefault {
				nonDefault = item.ID
			}
		}
		resolved, err := svc.Resolve(ctx, &nonDefault)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.ID != nonDefault {
			t.Errorf("resolved %d, want %d", resolved.ID, nonDefault)
		}
	})

	t.Run("api key three-state update", func(t *testing.T) {
		repo := NewRepository(db)
		before, err := repo.Get(ctx, secondID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		cipherBefore := append([]byte(nil), before.APIKeyCipher...)

		// 1. 不传 APIKey：密文与 hint 都必须原样保留。
		if _, err := svc.Update(ctx, secondID, UpdateInput{
			Name: "本地 Ollama", BaseURL: "http://localhost:11434/v1", Model: "qwen2.5",
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		after, err := repo.Get(ctx, secondID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !bytes.Equal(cipherBefore, after.APIKeyCipher) {
			t.Error("omitting api_key must keep the stored ciphertext")
		}
		// "ollama" 只有 6 位，按 hintOf 的规则不显示尾号：短 Key 的末四位
		// 已经占了信息量的一大半，显示出来反而帮了倒忙。
		if after.APIKeyHint != "" {
			t.Errorf("hint = %q, want empty for a short key", after.APIKeyHint)
		}

		// 2. 传空串：清除 Key，hint 一起清掉。
		empty := ""
		view, err := svc.Update(ctx, secondID, UpdateInput{
			Name: "本地 Ollama", BaseURL: "http://localhost:11434/v1", Model: "qwen2.5", APIKey: &empty,
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if view.APIKeyConfigured || view.APIKeyHint != "" {
			t.Errorf("clearing api_key left state behind: %+v", view)
		}

		// 3. 传新值：重新加密。
		fresh := "sk-brand-new-9876"
		view, err = svc.Update(ctx, secondID, UpdateInput{
			Name: "本地 Ollama", BaseURL: "http://localhost:11434/v1", Model: "qwen2.5", APIKey: &fresh,
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if !view.APIKeyConfigured || view.APIKeyHint != "9876" {
			t.Errorf("replacing api_key failed: %+v", view)
		}
	})

	t.Run("multiple configs without a default require a choice", func(t *testing.T) {
		// 先加到三条：删掉默认之后要留下两条，才构成"多条且无默认"。
		// 只剩一条时按规则会用那一条，测不到 ErrNotSelected。
		if _, err := svc.Create(ctx, CreateInput{
			Name: "临时配置", BaseURL: "https://c.example.com/v1", Model: "m",
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		items, err := svc.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var defaultID int64
		for _, item := range items {
			if item.IsDefault {
				defaultID = item.ID
			}
		}
		if err := svc.Delete(ctx, defaultID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		// 这里绝不允许"随便挑一条"：静默选错模型，用户会以为是回答质量问题。
		if _, err := svc.Resolve(ctx, nil); !errors.Is(err, ErrNotSelected) {
			t.Fatalf("err = %v, want ErrNotSelected", err)
		}
	})

	t.Run("single config resolves without an explicit default", func(t *testing.T) {
		items, err := svc.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, item := range items[1:] {
			if err := svc.Delete(ctx, item.ID); err != nil {
				t.Fatalf("Delete: %v", err)
			}
		}
		resolved, err := svc.Resolve(ctx, nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.ID != items[0].ID {
			t.Errorf("resolved %d, want the only remaining config %d", resolved.ID, items[0].ID)
		}

		if err := svc.Delete(ctx, items[0].ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := svc.Resolve(ctx, nil); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("err = %v, want ErrNotConfigured", err)
		}
	})

	t.Run("decrypting with a different master key fails loudly", func(t *testing.T) {
		// 重新建一条配置，然后用另一把主密钥读它——模拟"换机器、丢了 master.key"。
		if _, err := svc.Create(ctx, CreateInput{
			Name: "被换了密钥的配置", BaseURL: "https://a/v1", Model: "m", APIKey: "sk-secret-value",
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		otherCipher, err := NewCipher(testKey(6))
		if err != nil {
			t.Fatalf("NewCipher: %v", err)
		}
		broken := NewService(NewRepository(db), otherCipher, nil)
		_, err = broken.Resolve(ctx, nil)
		if !errors.Is(err, ErrDecryptFailed) {
			t.Fatalf("err = %v, want ErrDecryptFailed", err)
		}
	})
}

// 库层面的约束必须单独验证：service 的校验能拦住绝大多数情况，
// 但真正保证"库里不会出现非法数据"的是 CHECK 与唯一索引。
func TestChatModelConstraintsIntegration(t *testing.T) {
	db := setupChatModelDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// 绕过 service 直接插入非法 base_url：CHECK 必须拒绝。
	err := repo.Create(ctx, &ChatModel{Name: "bad", BaseURL: "not-a-url", Model: "m"})
	if err == nil {
		t.Fatal("database accepted a base_url without scheme")
	}
	if !strings.Contains(err.Error(), "chat_models_base_url_check") {
		t.Logf("note: constraint error text = %v", err)
	}

	// 两条同时为默认：部分唯一索引必须拒绝第二条。
	first := &ChatModel{Name: "a", BaseURL: "https://a/v1", Model: "m", IsDefault: true}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("Create: %v", err)
	}
	second := &ChatModel{Name: "b", BaseURL: "https://b/v1", Model: "m", IsDefault: true}
	if err := repo.Create(ctx, second); !errors.Is(err, errDuplicate) {
		t.Fatalf("err = %v, want the duplicate marker", err)
	}
}
