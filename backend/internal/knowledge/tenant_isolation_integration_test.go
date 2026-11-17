package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/testsupport"
)

// 本文件是多租户隔离的回归基线：每一条都断言"B 看不见 A 的东西"，
// 且越权一律表现为"不存在"（ErrNotFound），不是"无权限"。
//
// 为什么必须用真库：唯一索引、EXISTS 子查询、HNSW 过滤都是数据库行为；
// 内存替身可以"假装"隔离，但那证明不了 SQL 里真的带了 owner 条件。

func newSecondOwner(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	return testsupport.NewUser(t, db, fmt.Sprintf("other-%d@test.local", time.Now().UnixNano()))
}

func TestDocumentIsolationIntegration(t *testing.T) {
	db, owner := setupKnowledgeDB(t)
	other := newSecondOwner(t, db)
	repo := NewRepository(db)
	ctx := context.Background()

	doc := sampleDocument(owner, strings.Repeat("a", 64))
	doc.Collection = strPtr("工作")
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(3)); err != nil {
		t.Fatalf("create document: %v", err)
	}
	foreign := sampleDocument(other, strings.Repeat("b", 64))
	if err := repo.CreateWithChunks(ctx, foreign, chunkFixture(2)); err != nil {
		t.Fatalf("create other document: %v", err)
	}

	t.Run("read by id is owner scoped", func(t *testing.T) {
		if _, err := repo.Get(ctx, other, doc.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign get err = %v, want ErrNotFound", err)
		}
		if got, err := repo.Get(ctx, owner, doc.ID); err != nil || got.ID != doc.ID {
			t.Fatalf("own get = %+v, %v", got, err)
		}
	})

	t.Run("list only returns own documents", func(t *testing.T) {
		docs, err := repo.List(ctx, owner, DocumentFilter{}, 50, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(docs) != 1 || docs[0].ID != doc.ID {
			t.Fatalf("owner list = %+v, want only %d", docs, doc.ID)
		}
		total, err := repo.Count(ctx, owner, DocumentFilter{})
		if err != nil || total != 1 {
			t.Fatalf("owner count = %d, %v; want 1", total, err)
		}
		otherDocs, err := repo.List(ctx, other, DocumentFilter{}, 50, 0)
		if err != nil || len(otherDocs) != 1 || otherDocs[0].ID != foreign.ID {
			t.Fatalf("other list = %+v, %v", otherDocs, err)
		}
	})

	t.Run("update and delete cannot touch another owners row", func(t *testing.T) {
		title := "被人改了"
		if err := repo.Update(ctx, other, doc.ID, DocumentPatch{Title: &title}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign update err = %v, want ErrNotFound", err)
		}
		got, err := repo.Get(ctx, owner, doc.ID)
		if err != nil || got.Title == title {
			t.Fatalf("document was modified by another owner: %+v, %v", got, err)
		}
		if err := repo.Delete(ctx, other, doc.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign delete err = %v, want ErrNotFound", err)
		}
		if _, err := repo.Get(ctx, owner, doc.ID); err != nil {
			t.Fatalf("document disappeared after a foreign delete: %v", err)
		}
	})

	t.Run("chunks are only visible through an owned document", func(t *testing.T) {
		chunks, err := repo.ListChunks(ctx, other, doc.ID, 10, 0)
		if err != nil {
			t.Fatalf("list chunks: %v", err)
		}
		if len(chunks) != 0 {
			t.Fatalf("foreign chunks = %d, want 0", len(chunks))
		}
		total, err := repo.CountChunks(ctx, other, doc.ID)
		if err != nil || total != 0 {
			t.Fatalf("foreign count = %d, %v; want 0", total, err)
		}
		own, err := repo.ListChunks(ctx, owner, doc.ID, 10, 0)
		if err != nil || len(own) != 3 {
			t.Fatalf("own chunks = %d, %v", len(own), err)
		}
	})
}

// 同一份内容被两个用户各导入一次：两条记录、各自可读，互不覆盖。
// 这条守的是"content_hash 唯一索引必须带 owner"这个改动。
func TestSameContentDifferentOwnersIntegration(t *testing.T) {
	db, owner := setupKnowledgeDB(t)
	other := newSecondOwner(t, db)
	repo := NewRepository(db)
	ctx := context.Background()

	hash := strings.Repeat("c", 64)
	first := sampleDocument(owner, hash)
	second := sampleDocument(other, hash)
	if err := repo.CreateWithChunks(ctx, first, chunkFixture(1)); err != nil {
		t.Fatalf("first owner create: %v", err)
	}
	if err := repo.CreateWithChunks(ctx, second, chunkFixture(1)); err != nil {
		t.Fatalf("second owner create with the same content: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("two owners share one document row")
	}

	// 幂等查询也必须按人收窄：否则 A 上传后 B 会"命中" A 的文档。
	got, err := repo.GetByContentHash(ctx, other, hash)
	if err != nil || got.ID != second.ID {
		t.Fatalf("other lookup = %+v, %v; want %d", got, err, second.ID)
	}

	// 原文对象共享：两条记录都指向同一个 storage_path，所以删一条不能删对象。
	refs, err := repo.StoragePathRefs(ctx, *first.StoragePath)
	if err != nil || refs != 2 {
		t.Fatalf("storage refs = %d, %v; want 2", refs, err)
	}
	if err := repo.Delete(ctx, owner, first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	refs, err = repo.StoragePathRefs(ctx, *second.StoragePath)
	if err != nil || refs != 1 {
		t.Fatalf("storage refs after delete = %d, %v; want 1", refs, err)
	}
}

func TestSearchIsolationIntegration(t *testing.T) {
	db, owner := setupKnowledgeDB(t)
	other := newSecondOwner(t, db)
	repo := NewRepository(db)
	ctx := context.Background()

	// 两篇内容完全不同：全文路用专有词区分，向量路用正交向量区分。
	doc := sampleDocument(owner, strings.Repeat("d", 64))
	if err := repo.CreateWithChunks(ctx, doc, []Chunk{
		{Seq: 0, Content: "redisfailover 的选举过程", StartOffset: 0, EndOffset: 20, TokenCount: 8},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	foreign := sampleDocument(other, strings.Repeat("e", 64))
	if err := repo.CreateWithChunks(ctx, foreign, []Chunk{
		{Seq: 0, Content: "番茄炒蛋的做法", StartOffset: 0, EndOffset: 10, TokenCount: 4},
	}); err != nil {
		t.Fatalf("create other: %v", err)
	}

	// 向量：owner 的块是 [1,0,...]，other 的块是 [0,1,...]，查询贴近 owner。
	// 若 SQL 漏了 owner 条件，other 会看到 owner 的那条（相似度 1）。
	ownerVec := make([]float32, EmbeddingDimensions)
	ownerVec[0] = 1
	otherVec := make([]float32, EmbeddingDimensions)
	otherVec[1] = 1

	chunks, err := repo.ListChunks(ctx, owner, doc.ID, 10, 0)
	if err != nil || len(chunks) != 1 {
		t.Fatalf("owner chunks: %d, %v", len(chunks), err)
	}
	foreignChunks, err := repo.ListChunks(ctx, other, foreign.ID, 10, 0)
	if err != nil || len(foreignChunks) != 1 {
		t.Fatalf("other chunks: %d, %v", len(foreignChunks), err)
	}
	if err := repo.SaveEmbeddings(ctx, doc.ID, "fake-embed", EmbeddingDimensions,
		[]ChunkVector{{ID: chunks[0].ID, Vector: ownerVec}}); err != nil {
		t.Fatalf("save owner embeddings: %v", err)
	}
	if err := repo.SaveEmbeddings(ctx, foreign.ID, "fake-embed", EmbeddingDimensions,
		[]ChunkVector{{ID: foreignChunks[0].ID, Vector: otherVec}}); err != nil {
		t.Fatalf("save other embeddings: %v", err)
	}

	hits, err := repo.SearchVector(ctx, owner, ownerVec, "fake-embed", 10, DocumentFilter{})
	if err != nil || len(hits) != 1 || hits[0].DocumentID != doc.ID {
		t.Fatalf("owner vector hits = %+v, %v", hits, err)
	}
	foreignHits, err := repo.SearchVector(ctx, other, ownerVec, "fake-embed", 10, DocumentFilter{})
	if err != nil {
		t.Fatalf("other vector search: %v", err)
	}
	if len(foreignHits) != 1 || foreignHits[0].DocumentID != foreign.ID {
		t.Fatalf("other vector hits = %+v, want only its own document", foreignHits)
	}

	textHits, err := repo.SearchText(ctx, other, "redisfailover", 10, DocumentFilter{})
	if err != nil {
		t.Fatalf("other text search: %v", err)
	}
	if len(textHits) != 0 {
		t.Fatalf("other text hits = %+v, want none", textHits)
	}
	ownText, err := repo.SearchText(ctx, owner, "redisfailover", 10, DocumentFilter{})
	if err != nil || len(ownText) != 1 {
		t.Fatalf("owner text hits = %+v, %v", ownText, err)
	}
}

func TestEmbedCandidatesAreOwnerScopedIntegration(t *testing.T) {
	db, owner := setupKnowledgeDB(t)
	other := newSecondOwner(t, db)
	repo := NewRepository(db)
	ctx := context.Background()

	doc := sampleDocument(owner, strings.Repeat("f", 64))
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(2)); err != nil {
		t.Fatalf("create: %v", err)
	}

	ids, err := repo.ListEmbedCandidates(ctx, owner, "fake-embed", 10)
	if err != nil || len(ids) != 1 || ids[0] != doc.ID {
		t.Fatalf("owner candidates = %v, %v", ids, err)
	}
	total, err := repo.CountEmbedCandidates(ctx, owner, "fake-embed")
	if err != nil || total != 1 {
		t.Fatalf("owner candidates count = %d, %v", total, err)
	}

	foreignIDs, err := repo.ListEmbedCandidates(ctx, other, "fake-embed", 10)
	if err != nil {
		t.Fatalf("other candidates: %v", err)
	}
	if len(foreignIDs) != 0 {
		t.Fatalf("other candidates = %v, want none", foreignIDs)
	}
	foreignTotal, err := repo.CountEmbedCandidates(ctx, other, "fake-embed")
	if err != nil || foreignTotal != 0 {
		t.Fatalf("other candidates count = %d, %v", foreignTotal, err)
	}
}
