package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/luo303/mindos/backend/internal/platform"
)

// fakeEmbedder 是向量模型的替身。可以指定返回、抛错，以及在"调用进行中"
// 执行一个钩子——竞态用例需要它（读分块与写向量之间替换分块）。
type fakeEmbedder struct {
	model   string
	dim     int
	err     error
	vectors [][]float32
	calls   int
	onEmbed func()
}

func newFakeEmbedder() *fakeEmbedder {
	return &fakeEmbedder{model: "fake-embed", dim: EmbeddingDimensions}
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, int64, error) {
	f.calls++
	if f.onEmbed != nil {
		f.onEmbed()
	}
	if f.err != nil {
		return nil, 0, f.err
	}
	if f.vectors != nil {
		return f.vectors, int64(len(texts) * 10), nil
	}
	// 默认按批内下标生成可辨认的向量，方便断言"第几条拿到哪个"。
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, f.dim)
		v[0] = float32(i)
		out[i] = v
	}
	return out, int64(len(texts) * 10), nil
}

func (f *fakeEmbedder) Model() string   { return f.model }
func (f *fakeEmbedder) Dimensions() int { return f.dim }

func newTestServiceWithEmbedder(e Embedder) (*Service, *fakeRepo, *fakeStore) {
	repo, store := newFakeRepo(), newFakeStore()
	return NewService(repo, store, repo.tasks, e, 1<<20, nil), repo, store
}

// seedImportedNote 受理并执行一次笔记导入，返回文档。导入段只切分、不调模型。
func seedImportedNote(t *testing.T, svc *Service) *Document {
	t.Helper()
	ctx := context.Background()
	res, err := svc.StartNoteImport(ctx, NoteInput{
		Title:   "并发笔记",
		Content: "# 第一节\n\n正文一。\n\n## 第二节\n\n正文二。\n",
	})
	if err != nil {
		t.Fatalf("start note import: %v", err)
	}
	if err := svc.ProcessImport(ctx, res.Document.ID); err != nil {
		t.Fatalf("process import: %v", err)
	}
	doc, err := svc.Get(ctx, res.Document.ID)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	return doc
}

// 配了模型：切分完成后文档 ready、向量状态 pending，并排了一条向量化任务。
// 这正是"两段式"的核心断言——分块已可用，向量还没做，且有人会去做。
func TestImportQueuesEmbedTaskWhenConfigured(t *testing.T) {
	svc, repo, _ := newTestServiceWithEmbedder(newFakeEmbedder())
	doc := seedImportedNote(t, svc)

	if doc.Status != StatusReady {
		t.Errorf("status = %s, want ready", doc.Status)
	}
	if doc.EmbedStatus != EmbedStatusPending {
		t.Errorf("embed_status = %s, want pending", doc.EmbedStatus)
	}
	var embedTasks int
	for _, task := range repo.tasks.items {
		if task.Type == KindDocumentEmbed && task.TargetID == doc.ID {
			embedTasks++
		}
	}
	if embedTasks != 1 {
		t.Errorf("embed tasks = %d, want 1", embedTasks)
	}
	// 导入段不该调模型：外部调用不进事务，调了就把切分绑到模型上。
	if fake := svc.embedder.(*fakeEmbedder); fake.calls != 0 {
		t.Errorf("embedder called %d times during import, want 0", fake.calls)
	}
}

// 没配模型：导入照常成功，向量状态如实标 skipped，且不排任务。
func TestImportMarksSkippedWithoutEmbedder(t *testing.T) {
	svc, repo, _ := newTestService(1 << 20)
	doc := seedImportedNote(t, svc)

	if doc.Status != StatusReady {
		t.Errorf("status = %s, want ready", doc.Status)
	}
	if doc.EmbedStatus != EmbedStatusSkipped {
		t.Errorf("embed_status = %s, want skipped", doc.EmbedStatus)
	}
	if doc.EmbedErrorCode == nil || *doc.EmbedErrorCode != EmbedNotConfiguredCode {
		t.Errorf("embed_error_code = %v, want %s", doc.EmbedErrorCode, EmbedNotConfiguredCode)
	}
	for _, task := range repo.tasks.items {
		if task.Type == KindDocumentEmbed {
			t.Fatal("should not queue an embed task when no model is configured")
		}
	}
}

// 第二段任务：写回向量与 done 状态。
func TestProcessEmbedWritesVectorsAndStatus(t *testing.T) {
	svc, _, _ := newTestServiceWithEmbedder(newFakeEmbedder())
	doc := seedImportedNote(t, svc)

	if err := svc.ProcessEmbed(context.Background(), doc.ID); err != nil {
		t.Fatalf("process embed: %v", err)
	}
	done, err := svc.Get(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if done.EmbedStatus != EmbedStatusDone {
		t.Errorf("embed_status = %s, want done", done.EmbedStatus)
	}
	if done.EmbeddedChunks != done.ChunkCount || done.ChunkCount == 0 {
		t.Errorf("embedded_chunks = %d, chunk_count = %d", done.EmbeddedChunks, done.ChunkCount)
	}
	if done.EmbedModel == nil || *done.EmbedModel != "fake-embed" {
		t.Errorf("embed_model = %v", done.EmbedModel)
	}
	if done.EmbedDimensions == nil || *done.EmbedDimensions != EmbeddingDimensions {
		t.Errorf("embed_dimensions = %v", done.EmbedDimensions)
	}
	// 向量化不该动文档状态：分块本来就是好的。
	if done.Status != StatusReady {
		t.Errorf("status = %s, want ready to be preserved", done.Status)
	}
}

func TestProcessEmbedWithoutModel(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	doc := seedImportedNote(t, svc)

	err := svc.ProcessEmbed(context.Background(), doc.ID)
	if !errors.Is(err, ErrEmbeddingNotConfigured) {
		t.Fatalf("err = %v, want ErrEmbeddingNotConfigured", err)
	}
}

// 凭据错误是永久错误：不该重试，也不该变成"暂时不可用"。
func TestProcessEmbedMapsAuthError(t *testing.T) {
	fake := newFakeEmbedder()
	fake.err = platform.ErrEmbeddingUnauthorized
	svc, _, _ := newTestServiceWithEmbedder(fake)
	doc := seedImportedNote(t, svc)

	err := svc.ProcessEmbed(context.Background(), doc.ID)
	if !errors.Is(err, ErrEmbeddingAuth) {
		t.Fatalf("err = %v, want ErrEmbeddingAuth", err)
	}
	if !IsPermanent(err) {
		t.Error("auth failure should be permanent")
	}
	if ErrorCode(err) != EmbedAuthCode {
		t.Errorf("error code = %s, want %s", ErrorCode(err), EmbedAuthCode)
	}
}

func TestProcessEmbedMapsDimensionMismatch(t *testing.T) {
	fake := newFakeEmbedder()
	fake.err = platform.ErrEmbeddingDimension
	svc, _, _ := newTestServiceWithEmbedder(fake)
	doc := seedImportedNote(t, svc)

	err := svc.ProcessEmbed(context.Background(), doc.ID)
	if !errors.Is(err, ErrEmbeddingDimensionMismatch) {
		t.Fatalf("err = %v, want ErrEmbeddingDimensionMismatch", err)
	}
	if !IsPermanent(err) {
		t.Error("dimension mismatch should be permanent")
	}
}

func TestProcessEmbedMapsUnavailableAsRetryable(t *testing.T) {
	fake := newFakeEmbedder()
	fake.err = platform.ErrEmbeddingUnavailable
	svc, _, _ := newTestServiceWithEmbedder(fake)
	doc := seedImportedNote(t, svc)

	err := svc.ProcessEmbed(context.Background(), doc.ID)
	if !errors.Is(err, ErrEmbeddingUnavailable) {
		t.Fatalf("err = %v, want ErrEmbeddingUnavailable", err)
	}
	if IsPermanent(err) {
		t.Error("temporary provider failure must stay retryable")
	}
}

// 与重索引赛跑：向量算到一半，分块被整体替换。此刻必须返回 ErrStaleChunks，
// 而不是把旧结果写进去、并把状态刷成 done（那会让界面显示"已完成"却没有向量）。
func TestProcessEmbedDiscardsResultWhenChunksReplaced(t *testing.T) {
	fake := newFakeEmbedder()
	svc, repo, _ := newTestServiceWithEmbedder(fake)
	doc := seedImportedNote(t, svc)

	ctx := context.Background()
	fake.onEmbed = func() {
		// 模拟并发重索引：删旧分块、插新分块（id 全变），并把状态置回 pending。
		if err := repo.FinishImport(ctx, doc.ID, chunkFixture(2), true); err != nil {
			t.Errorf("replace chunks during embed: %v", err)
		}
	}

	err := svc.ProcessEmbed(ctx, doc.ID)
	if !errors.Is(err, ErrStaleChunks) {
		t.Fatalf("err = %v, want ErrStaleChunks", err)
	}
	after, err := svc.Get(ctx, doc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.EmbedStatus == EmbedStatusDone {
		t.Error("stale result must not mark the document as done")
	}
}

func TestStartEmbedGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("未配置模型", func(t *testing.T) {
		svc, _, _ := newTestService(1 << 20)
		doc := seedImportedNote(t, svc)
		if _, err := svc.StartEmbed(ctx, doc.ID); !errors.Is(err, ErrEmbeddingNotConfigured) {
			t.Fatalf("err = %v, want ErrEmbeddingNotConfigured", err)
		}
	})

	t.Run("文档还没处理完", func(t *testing.T) {
		svc, _, _ := newTestServiceWithEmbedder(newFakeEmbedder())
		res, err := svc.StartNoteImport(ctx, NoteInput{Content: "# 待处理\n\n正文。\n"})
		if err != nil {
			t.Fatalf("start import: %v", err)
		}
		if _, err := svc.StartEmbed(ctx, res.Document.ID); !errors.Is(err, ErrNotReady) {
			t.Fatalf("err = %v, want ErrNotReady", err)
		}
	})

	t.Run("排队中重复点击", func(t *testing.T) {
		svc, _, _ := newTestServiceWithEmbedder(newFakeEmbedder())
		doc := seedImportedNote(t, svc)
		// importNote 之后已经有一条 pending 的 embed 任务。
		if _, err := svc.StartEmbed(ctx, doc.ID); !errors.Is(err, ErrEmbedRunning) {
			t.Fatalf("err = %v, want ErrEmbedRunning", err)
		}
	})

	t.Run("正常受理", func(t *testing.T) {
		svc, repo, _ := newTestServiceWithEmbedder(newFakeEmbedder())
		doc := seedImportedNote(t, svc)
		// 把已有的 embed 任务收掉，模拟"上一次已经跑完"。
		repo.tasks.completeFor(doc.ID)

		res, err := svc.StartEmbed(ctx, doc.ID)
		if err != nil {
			t.Fatalf("start embed: %v", err)
		}
		if res.MessageID == "" {
			t.Error("message id should not be empty")
		}
		if res.Document.EmbedStatus != EmbedStatusPending {
			t.Errorf("embed_status = %s, want pending", res.Document.EmbedStatus)
		}
	})
}

// 回填的候选条件：ready、有分块、且（未 done 或模型不同）。
func TestStartEmbedBackfillCounts(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newTestServiceWithEmbedder(newFakeEmbedder())
	doc := seedImportedNote(t, svc)
	repo.tasks.completeFor(doc.ID)

	// 先把它标成"用旧模型算完了"：模型不同 → 仍是候选。
	oldModel := "old-model"
	repo.docs[doc.ID].EmbedStatus = EmbedStatusDone
	repo.docs[doc.ID].EmbedModel = &oldModel

	res, err := svc.StartEmbedBackfill(ctx, 10)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if res.Enqueued != 1 {
		t.Errorf("enqueued = %d, want 1 (model changed)", res.Enqueued)
	}

	// 再点一次：已有活跃任务 → 计入 skipped，不报错。
	res, err = svc.StartEmbedBackfill(ctx, 10)
	if err != nil {
		t.Fatalf("backfill again: %v", err)
	}
	if res.Enqueued != 0 || res.Skipped != 1 {
		t.Errorf("second run: enqueued/skipped = %d/%d, want 0/1", res.Enqueued, res.Skipped)
	}
}

func TestTestEmbeddingProbe(t *testing.T) {
	svc, _, _ := newTestServiceWithEmbedder(newFakeEmbedder())
	probe, err := svc.TestEmbedding(context.Background())
	if err != nil {
		t.Fatalf("test embedding: %v", err)
	}
	if probe.Model != "fake-embed" || probe.Dimensions != EmbeddingDimensions {
		t.Errorf("probe = %+v", probe)
	}

	unconfigured, _, _ := newTestService(1 << 20)
	if _, err := unconfigured.TestEmbedding(context.Background()); !errors.Is(err, ErrEmbeddingNotConfigured) {
		t.Errorf("err = %v, want ErrEmbeddingNotConfigured", err)
	}
}
