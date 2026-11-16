package knowledge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/migrations"
)

// 本文件是真实 PostgreSQL 集成测试，覆盖文档仓储的质量门槛：
// 唯一约束冲突、chunk 校验失败时整笔事务回滚、级联删除。
//
// 运行方式（先启动 compose）：
//
//	MINDOS_TEST_DSN="host=localhost port=5432 user=mindos password=mindos_dev_password dbname=postgres sslmode=disable" \
//	go test -race -count=1 -run Integration ./internal/knowledge/
//
// 未设置变量时全部跳过；测试会建独立临时库并在结束时 DROP，不碰开发库。
func setupKnowledgeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testsupport.NewDatabase(t, "knowledge")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrate.New(db, migrations.FS).Up(ctx); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return testsupport.NewGorm(t, db)
}

func sampleDocument(hash string) *Document {
	path := "documents/" + hash
	return &Document{
		Title:       "集成测试文档",
		MIME:        MIMEMarkdown,
		SizeBytes:   12,
		ContentHash: hash,
		SourceType:  SourceFile,
		StoragePath: &path,
		Status:      StatusReady,
	}
}

// chunkFixture 构造 n 个合法分块（偏移与序号都满足约束）。
func chunkFixture(n int) []Chunk {
	chunks := make([]Chunk, 0, n)
	for i := 0; i < n; i++ {
		chunks = append(chunks, Chunk{
			Seq:         i,
			Content:     fmt.Sprintf("第 %d 段正文", i),
			StartOffset: i * 10,
			EndOffset:   i*10 + 5,
			TokenCount:  5,
		})
	}
	return chunks
}

func TestRepositoryIntegration(t *testing.T) {
	db := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	doc := sampleDocument(hash)
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(3)); err != nil {
		t.Fatalf("CreateWithChunks: %v", err)
	}
	if doc.ID == 0 {
		t.Fatal("document id should be assigned by the database")
	}

	got, err := repo.Get(ctx, doc.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != doc.Title || got.ChunkCount != 3 || got.Status != StatusReady {
		t.Fatalf("unexpected document: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("timestamps should be filled by the database")
	}

	chunks, err := repo.ListChunks(ctx, doc.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 3 || chunks[0].Seq != 0 || chunks[2].Seq != 2 {
		t.Fatalf("chunks should be ordered by seq: %+v", chunks)
	}
	if chunks[0].DocumentID != doc.ID {
		t.Fatalf("chunk document_id = %d, want %d", chunks[0].DocumentID, doc.ID)
	}

	total, err := repo.CountChunks(ctx, doc.ID)
	if err != nil || total != 3 {
		t.Fatalf("CountChunks = %d, %v", total, err)
	}

	// 重复内容必须被唯一索引挡住，并翻译成 ErrDuplicate。
	dup := sampleDocument(hash)
	if err := repo.CreateWithChunks(ctx, dup, nil); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate hash err = %v, want ErrDuplicate", err)
	}

	// 级联删除：删文档后分块不应残留（外键 ON DELETE CASCADE 的验证）。
	if err := db.WithContext(ctx).Exec("DELETE FROM documents WHERE id = ?", doc.ID).Error; err != nil {
		t.Fatalf("delete document: %v", err)
	}
	if total, err = repo.CountChunks(ctx, doc.ID); err != nil || total != 0 {
		t.Fatalf("chunks should be cascaded, got %d (%v)", total, err)
	}
}

func TestCreateWithChunksRollsBackIntegration(t *testing.T) {
	db := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	hash := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	doc := sampleDocument(hash)

	// 第二块 seq 为负，违反 document_chunks_seq_check。
	// 它发生在文档插入之后、同一事务内，用来验证"要么都写、要么都不写"。
	chunks := []Chunk{
		{Seq: 0, Content: "合法块", StartOffset: 0, EndOffset: 3, TokenCount: 3},
		{Seq: -1, Content: "非法块", StartOffset: 3, EndOffset: 6, TokenCount: 3},
	}
	if err := repo.CreateWithChunks(ctx, doc, chunks); err == nil {
		t.Fatal("inserting an invalid chunk should fail")
	}

	var count int64
	if err := db.WithContext(ctx).Model(&Document{}).Count(&count).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 0 {
		t.Fatalf("documents table should be empty after rollback, got %d rows", count)
	}
}
