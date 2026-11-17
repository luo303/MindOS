package knowledge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
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
// setupKnowledgeDB 建库、跑迁移，并造一个归属用户。
//
// 为什么连用户一起返回：documents.owner_id 是 NOT NULL 外键，
// 每个用例本来就都要一个 owner；让调用方自己建只会重复十几次。
func setupKnowledgeDB(t *testing.T) (*gorm.DB, int64) {
	t.Helper()
	db := testsupport.NewDatabase(t, "knowledge")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrate.New(db, migrations.FS).Up(ctx); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	gdb := testsupport.NewGorm(t, db)
	owner := testsupport.NewUser(t, gdb, fmt.Sprintf("knowledge-%d@test.local", time.Now().UnixNano()))
	return gdb, owner
}

func sampleDocument(owner int64, hash string) *Document {
	path := "documents/" + hash
	return &Document{
		OwnerID:     owner,
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
	db, owner := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	doc := sampleDocument(owner, hash)
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(3)); err != nil {
		t.Fatalf("CreateWithChunks: %v", err)
	}
	if doc.ID == 0 {
		t.Fatal("document id should be assigned by the database")
	}

	got, err := repo.Get(ctx, owner, doc.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != doc.Title || got.ChunkCount != 3 || got.Status != StatusReady {
		t.Fatalf("unexpected document: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("timestamps should be filled by the database")
	}

	chunks, err := repo.ListChunks(ctx, owner, doc.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 3 || chunks[0].Seq != 0 || chunks[2].Seq != 2 {
		t.Fatalf("chunks should be ordered by seq: %+v", chunks)
	}
	if chunks[0].DocumentID != doc.ID {
		t.Fatalf("chunk document_id = %d, want %d", chunks[0].DocumentID, doc.ID)
	}

	total, err := repo.CountChunks(ctx, owner, doc.ID)
	if err != nil || total != 3 {
		t.Fatalf("CountChunks = %d, %v", total, err)
	}

	// 重复内容必须被唯一索引挡住，并翻译成 ErrDuplicate。
	dup := sampleDocument(owner, hash)
	if err := repo.CreateWithChunks(ctx, dup, nil); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate hash err = %v, want ErrDuplicate", err)
	}

	// 级联删除：删文档后分块不应残留（外键 ON DELETE CASCADE 的验证）。
	if err := db.WithContext(ctx).Exec("DELETE FROM documents WHERE id = ?", doc.ID).Error; err != nil {
		t.Fatalf("delete document: %v", err)
	}
	if total, err = repo.CountChunks(ctx, owner, doc.ID); err != nil || total != 0 {
		t.Fatalf("chunks should be cascaded, got %d (%v)", total, err)
	}
}

func TestCreateWithChunksRollsBackIntegration(t *testing.T) {
	db, owner := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	hash := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	doc := sampleDocument(owner, hash)

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

// testVector 构造一条合法的 1024 维向量。
//
// 只让首元素有值：既省事，也能顺便验证 CAST(? AS vector) 走的是文本协议
// 而不是某种"看起来能写、读回来是零向量"的路径。
func testVector(seed float32) []float32 {
	v := make([]float32, EmbeddingDimensions)
	v[0] = seed
	return v
}

// 向量写回与新鲜度闸门，都必须在真实 pgvector 上验证：
// CAST(? AS vector) 的编码方式、vector(1024) 的维度校验、以及
// "分块被替换后旧结果一律作废"这条判断，假仓储都演不出来。
func TestEmbeddingsIntegration(t *testing.T) {
	db, owner := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	hash := "1111111111111111111111111111111111111111111111111111111111111111"
	doc := sampleDocument(owner, hash)
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(2)); err != nil {
		t.Fatalf("CreateWithChunks: %v", err)
	}
	chunks, err := repo.ListChunks(ctx, owner, doc.ID, 10, 0)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("ListChunks = %d, %v", len(chunks), err)
	}

	if err := repo.MarkEmbedRunning(ctx, doc.ID); err != nil {
		t.Fatalf("MarkEmbedRunning: %v", err)
	}

	vectors := []ChunkVector{
		{ID: chunks[0].ID, Vector: testVector(0.5)},
		{ID: chunks[1].ID, Vector: testVector(0.25)},
	}
	if err := repo.SaveEmbeddings(ctx, doc.ID, "bge-m3", EmbeddingDimensions, vectors); err != nil {
		t.Fatalf("SaveEmbeddings: %v", err)
	}

	var stored int64
	if err := db.WithContext(ctx).Model(&Chunk{}).
		Where("document_id = ? AND embedding IS NOT NULL", doc.ID).
		Count(&stored).Error; err != nil {
		t.Fatalf("count embedded chunks: %v", err)
	}
	if stored != 2 {
		t.Fatalf("embedded chunks = %d, want 2", stored)
	}

	done, err := repo.Get(ctx, owner, doc.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if done.EmbedStatus != EmbedStatusDone || done.EmbeddedChunks != 2 {
		t.Fatalf("document embed state = %s/%d, want done/2", done.EmbedStatus, done.EmbeddedChunks)
	}
	if done.EmbedDimensions == nil || *done.EmbedDimensions != EmbeddingDimensions {
		t.Fatalf("embed_dimensions = %v", done.EmbedDimensions)
	}

	// 距离算子可用：这是下一步检索的前提，也顺手证明向量真的进了列。
	var distance float64
	if err := db.WithContext(ctx).
		Raw(`SELECT embedding <=> CAST(? AS vector) FROM document_chunks WHERE id = ?`,
			pgvector.NewVector(testVector(0.5)).String(), chunks[0].ID).
		Scan(&distance).Error; err != nil {
		t.Fatalf("cosine distance query: %v", err)
	}
	if distance > 0.001 {
		t.Errorf("distance to identical vector = %v, want ~0", distance)
	}

	// 重索引换掉全部分块后，旧 id 再也不能写：必须得到 ErrStaleChunks，
	// 且文档状态不能被刷成 done（否则界面说"已完成"，检索却一条都没有）。
	if err := repo.FinishImport(ctx, doc.ID, chunkFixture(3), true); err != nil {
		t.Fatalf("FinishImport: %v", err)
	}
	if err := repo.SaveEmbeddings(ctx, doc.ID, "bge-m3", EmbeddingDimensions, vectors); !errors.Is(err, ErrStaleChunks) {
		t.Fatalf("stale write err = %v, want ErrStaleChunks", err)
	}
	after, err := repo.Get(ctx, owner, doc.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.EmbedStatus == EmbedStatusDone || after.EmbeddedChunks != 0 {
		t.Errorf("stale result must not mark done: %s/%d", after.EmbedStatus, after.EmbeddedChunks)
	}
}

// 切分完成后必须在同一事务里排上向量化任务：
// 少这一步就会出现"文档 ready、状态 pending，但没有任何东西会去做"。
func TestFinishImportQueuesEmbedTaskIntegration(t *testing.T) {
	db, owner := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	hash := "2222222222222222222222222222222222222222222222222222222222222222"
	doc := sampleDocument(owner, hash)
	if err := repo.CreateWithChunks(ctx, doc, nil); err != nil {
		t.Fatalf("CreateWithChunks: %v", err)
	}
	if err := repo.FinishImport(ctx, doc.ID, chunkFixture(2), true); err != nil {
		t.Fatalf("FinishImport: %v", err)
	}

	var tasks []struct {
		Type   string
		Status string
	}
	if err := db.WithContext(ctx).
		Raw(`SELECT type, status FROM tasks WHERE target_type = 'document' AND target_id = ?`, doc.ID).
		Scan(&tasks).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Type != KindDocumentEmbed || tasks[0].Status != "pending" {
		t.Fatalf("tasks = %+v, want one pending %s", tasks, KindDocumentEmbed)
	}

	// 再跑一次（模拟重索引）：旧的 embed 任务被标成 superseded，新的顶上，
	// 不会因为 tasks_active_uniq 让整笔事务失败。
	if err := repo.FinishImport(ctx, doc.ID, chunkFixture(3), true); err != nil {
		t.Fatalf("second FinishImport: %v", err)
	}
	var counts []struct {
		Status string
		Count  int64
	}
	if err := db.WithContext(ctx).
		Raw(`SELECT status, count(*) AS count FROM tasks WHERE target_type = 'document' AND target_id = ? GROUP BY status`, doc.ID).
		Scan(&counts).Error; err != nil {
		t.Fatalf("query task counts: %v", err)
	}
	var cancelled, pending int64
	for _, c := range counts {
		switch c.Status {
		case "cancelled":
			cancelled = c.Count
		case "pending":
			pending = c.Count
		}
	}
	if cancelled != 1 || pending != 1 {
		t.Fatalf("task counts = %+v, want 1 cancelled + 1 pending", counts)
	}
}
