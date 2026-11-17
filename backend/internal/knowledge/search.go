// 本文件是知识模块的检索能力：向量路 + 全文路 + RRF 融合。
//
// 为什么必须两路：向量擅长"意思相近"（换个说法也能召回），全文擅长"字面精确"
// （错误码、函数名、版本号），任一路单独用都会在某一类问题上失灵。
//
// 为什么用 RRF 而不是把两路分数加权平均：两路的量纲根本不同——余弦相似度是
// 有绝对含义的 0~1 值，ts_rank_cd 只跟本次查询内部的分词命中情况有关，
// 两者直接相加等于把"米"和"摄氏度"加起来。RRF 只用名次，天然免疫量纲问题：
//
//	score(chunk) = Σ 1 / (rrfK + rank_i)      rank 从 1 开始，命中哪几路就加几次
//
// 常数 60 来自原论文的推荐值：它让第 1 名与第 2 名的差距（1/61 vs 1/62）
// 足够小，从而"两路都排在中等"的片段能赢过"一路第 1、另一路没有"的片段——
// 这正是融合想要的效果。
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

const (
	// rrfK 是 RRF 的平滑常数，见文件头说明。调大会让名次差异更平缓。
	rrfK = 60.0

	// efSearch 是 HNSW 迭代扫描的动态候选列表大小。
	// 100 与 defaultCandidates（30）不是一回事：后者是"查询返回多少条"，
	// 前者是"为了让过滤后还剩够多条，索引内部要看多远"。过滤条件越严格
	// （多租户就是这种情况），它越需要大一些。
	efSearch = 100

	// defaultCandidates 是每一路取多少候选再融合。
	//
	// 30 的依据：最终只要 top 8，而 RRF 需要看到"两路各自的前若干名"才能做出
	// 有意义的比较。取 30 约等于最终条数的 4 倍，既能捞回一些单路排名靠后的
	// 好片段，又不至于让 SQL 搬太多正文回来（每条 600 字，30 条约 18KB）。
	defaultCandidates = 30

	// defaultSearchLimit 是调用方没给 limit 时的兜底条数。
	defaultSearchLimit = 8

	// maxSearchLimit 是单次检索返回条数的上限。
	//
	// 20 不是性能限制而是上下文预算：8 条已经能占掉约 4k token，
	// 再往上加只会挤掉对话历史，而不会提高答案质量。
	maxSearchLimit = 20
)

// ErrEmptyQuery 表示检索词为空。
//
// 单独一个哨兵而不是复用 ErrEmptyDocument：前者是"这次调用参数不对"，
// 后者是"这份文档本身没内容"，上层要给出完全不同的提示。
var ErrEmptyQuery = errors.New("search query is empty")

// Hit 是一条检索命中的分块，也是引用与上下文的最小单位。
//
// 同时带 Similarity 与 TextRank：前者用于阈值闸门（有绝对含义），
// 后者只用于解释"为什么这条被召回"。RRFScore 是融合后的排序依据。
type Hit struct {
	DocumentID  int64   `gorm:"column:document_id" json:"document_id"`
	ChunkID     int64   `gorm:"column:id" json:"chunk_id"`
	Seq         int     `gorm:"column:seq" json:"seq"`
	Title       string  `gorm:"column:title" json:"title"`
	Heading     string  `gorm:"column:heading" json:"heading"`
	Content     string  `gorm:"column:content" json:"content"`
	StartOffset int     `gorm:"column:start_offset" json:"start_offset"`
	EndOffset   int     `gorm:"column:end_offset" json:"end_offset"`
	Similarity  float64 `gorm:"column:similarity" json:"similarity"`
	TextRank    float64 `gorm:"column:text_rank" json:"text_rank"`
	RRFScore    float64 `gorm:"-" json:"rrf_score"`
}

// SearchResult 是一次检索的完整结果。
//
// 为什么不只返回 []Hit：上层要拿 MaxSimilarity 做阈值闸门，而这个值必须来自
// 向量路的原始相似度（RRF 分数没有绝对含义，拿它比阈值只会得到一个恒真或
// 恒假的条件）。把"两路各命中多少""是不是只能靠全文"一起带出来，
// 上层就不需要自己从 Hit 里反推这些事实。
type SearchResult struct {
	Hits          []Hit
	VectorHits    int
	TextHits      int
	MaxSimilarity float64
	// Degraded 表示向量路一条没命中、结果全部来自全文路。
	//
	// 正常情况只有一种：库里还没有向量，或者 embed_model 与当前模型不一致
	// （换过模型但没重建）。此时语义检索实际是关着的，界面应该说明这一点，
	// 而不是让用户以为"语义检索能用只是没命中"。
	Degraded bool
}

// searchStore 是检索需要的两条查询能力。
//
// 定义在使用方（本文件）而不是 Repository 那边：接口属于调用者，*Repository
// 什么都不用写就满足它。有了这层，融合与闸门逻辑可以用假实现测，
// 不必每个用例都起一个真库。
type searchStore interface {
	SearchVector(ctx context.Context, ownerID int64, vector []float32, model string, limit int, filter DocumentFilter) ([]Hit, error)
	SearchText(ctx context.Context, ownerID int64, query string, limit int, filter DocumentFilter) ([]Hit, error)
}

// Searcher 把"问一句话"变成"一组带分数的分块"。
//
// 它自己不算向量：Embedder 由外面注入，未配置时为 nil，Search 会直接
// 返回 ErrEmbeddingNotConfigured，让上层给出"先去配向量模型"的提示，
// 而不是悄悄退化成全文检索（中文全文在 simple 配置下基本不可用）。
type Searcher struct {
	store    searchStore
	embedder Embedder
}

// NewSearcher 组装检索器。embedder 允许为 nil，语义见 Searcher 的说明。
func NewSearcher(store searchStore, embedder Embedder) *Searcher {
	return &Searcher{store: store, embedder: embedder}
}

// Search 执行一次混合检索，只在该拥有者的文档里找。
//
// filter 只用到 Collection / Tag 两个字段，Status 会被忽略——
// 检索永远只看 ready 的文档，这是硬约束，不交给调用方决定。
func (s *Searcher) Search(ctx context.Context, ownerID int64, query string, limit int, filter DocumentFilter) (*SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrEmptyQuery
	}
	if s.embedder == nil {
		return nil, ErrEmbeddingNotConfigured
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	// 查询向量只算一次：两路里只有向量路需要它，但算两次没有意义，
	// 而一次嵌入调用就是一次网络往返。
	vectors, _, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, mapEmbedError(err)
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("knowledge: embedding returned %d vectors for 1 query", len(vectors))
	}

	vectorHits, err := s.store.SearchVector(ctx, ownerID, vectors[0], s.embedder.Model(), defaultCandidates, filter)
	if err != nil {
		return nil, err
	}
	textHits, err := s.store.SearchText(ctx, ownerID, query, defaultCandidates, filter)
	if err != nil {
		return nil, err
	}

	result := &SearchResult{
		Hits:       fuseRRF(vectorHits, textHits, limit),
		VectorHits: len(vectorHits),
		TextHits:   len(textHits),
		Degraded:   len(vectorHits) == 0 && len(textHits) > 0,
	}
	for _, hit := range vectorHits {
		if hit.Similarity > result.MaxSimilarity {
			result.MaxSimilarity = hit.Similarity
		}
	}
	return result, nil
}

// fuseRRF 按名次融合两路结果，返回前 limit 条。
//
// 同一个分块被两路同时命中时，两个 1/(k+rank) 相加——这正是 RRF 的设计：
// 多路共识的片段排名应该更高，而不是取两路的最大值。
func fuseRRF(vectorHits, textHits []Hit, limit int) []Hit {
	type scored struct {
		hit   Hit
		score float64
	}
	merged := make(map[int64]*scored, len(vectorHits)+len(textHits))

	absorb := func(hits []Hit, fromVector bool) {
		for i, hit := range hits {
			acc, ok := merged[hit.ChunkID]
			if !ok {
				// 第一次见到这个分块：正文、标题、偏移都由它带进来。
				// 两路查的是同一张表同一行，字段不会打架。
				acc = &scored{hit: hit}
				merged[hit.ChunkID] = acc
			}
			acc.score += 1 / (rrfK + float64(i+1))
			if fromVector {
				acc.hit.Similarity = hit.Similarity
			} else {
				acc.hit.TextRank = hit.TextRank
			}
		}
	}
	absorb(vectorHits, true)
	absorb(textHits, false)

	out := make([]Hit, 0, len(merged))
	for _, acc := range merged {
		hit := acc.hit
		hit.RRFScore = acc.score
		out = append(out, hit)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RRFScore != out[j].RRFScore {
			return out[i].RRFScore > out[j].RRFScore
		}
		// 同分时用相似度、再用 id 兜底：map 的遍历顺序是随机的，
		// 没有确定的第二排序键，同一个问题两次搜索可能给出不同顺序，
		// 那种"偶尔不一样"最难复现。
		if out[i].Similarity != out[j].Similarity {
			return out[i].Similarity > out[j].Similarity
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// vectorSearchSQL 的 %s 位置拼筛选条件（collection / tag），其余全部参数化。
//
// `1 - (embedding <=> q)` 是余弦距离转相似度：<=> 返回 0（完全相同）到 2（完全相反），
// 归一化向量下大致落在 0~1，减一下就得到"越大越像"。
const vectorSearchSQL = `
SELECT c.id, c.document_id, c.seq, COALESCE(c.heading, '') AS heading,
       c.content, c.start_offset, c.end_offset, d.title,
       1 - (c.embedding <=> CAST(? AS vector)) AS similarity
  FROM document_chunks c
  JOIN documents d ON d.id = c.document_id
 WHERE d.status = 'ready'
   AND d.owner_id = ?
   AND c.embedding IS NOT NULL
   AND d.embed_model = ?%s
 ORDER BY c.embedding <=> CAST(? AS vector)
 LIMIT ?`

// SearchVector 走向量路。
//
// model 必须传"当前配置的模型"：换过模型但没重建时，库里的旧向量与查询向量
// 不在同一空间，算出来的距离看着正常其实毫无意义。过滤掉它们比返回错的结果好。
func (r *Repository) SearchVector(ctx context.Context, ownerID int64, vector []float32, model string, limit int, filter DocumentFilter) ([]Hit, error) {
	if len(vector) != EmbeddingDimensions {
		return nil, fmt.Errorf("knowledge: query vector has %d dimensions, expected %d",
			len(vector), EmbeddingDimensions)
	}
	clauses, args := searchFilterClauses(filter)
	encoded := pgvector.NewVector(vector).String()

	var hits []Hit
	query := fmt.Sprintf(vectorSearchSQL, clauses)
	queryArgs := []any{encoded, ownerID, model}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, encoded, limit)

	// 为什么包一个事务：HNSW 是"先取 top-N 再过滤"，加上 owner 条件后
	// 一次扫描可能取不满 N 条（别人的分块被过滤掉）。迭代扫描让它在
	// 结果不足时继续往下扫，而开关只能通过 SET LOCAL / set_config 设置，
	// 那要求处于事务里。
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 用 current_setting(..., true) 探测参数是否存在：未知参数返回 NULL，
		// 老版本 pgvector 不会报错。探测不到就退回普通扫描（召回可能变差，
		// 但检索不能因此整个失败）。
		var probe *string
		_ = tx.Raw("SELECT current_setting('hnsw.iterative_scan', true)").Scan(&probe).Error
		if probe != nil {
			_ = tx.Exec("SELECT set_config('hnsw.iterative_scan', 'relaxed_order', true)").Error
			// set_config 的第二个参数是 text：直接传 int 会被 pgx 以 text 编码拒绝。
			_ = tx.Exec("SELECT set_config('hnsw.ef_search', ?, true)", strconv.Itoa(efSearch)).Error
		}
		return tx.Raw(query, queryArgs...).Scan(&hits).Error
	})
	if err != nil {
		return nil, translateError("search vectors", err)
	}
	return hits, nil
}

// textSearchSQL 的 %s 与向量路一致。
//
// 用 websearch_to_tsquery 而不是 plainto_tsquery：前者把 "a b"、`"a b"`、`a or b`
// 都按用户直觉解释，也永远不会因为奇怪的标点报错（语法错误在它是 0 结果）。
const textSearchSQL = `
SELECT c.id, c.document_id, c.seq, COALESCE(c.heading, '') AS heading,
       c.content, c.start_offset, c.end_offset, d.title,
       ts_rank_cd(c.tsv, q) AS text_rank
  FROM document_chunks c
  JOIN documents d ON d.id = c.document_id
 CROSS JOIN websearch_to_tsquery('simple', ?) AS q
 WHERE d.status = 'ready'
   AND d.owner_id = ?
   AND c.tsv @@ q%s
 ORDER BY text_rank DESC
 LIMIT ?`

// SearchText 走全文路。
//
// 不按 embed_model 过滤：全文命中跟"向量是不是当前模型算的"没有关系。
// 库里还没向量时，这一路是唯一还能工作的检索方式。
func (r *Repository) SearchText(ctx context.Context, ownerID int64, query string, limit int, filter DocumentFilter) ([]Hit, error) {
	clauses, args := searchFilterClauses(filter)

	var hits []Hit
	raw := fmt.Sprintf(textSearchSQL, clauses)
	queryArgs := []any{query, ownerID}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit)
	if err := r.db.WithContext(ctx).Raw(raw, queryArgs...).Scan(&hits).Error; err != nil {
		return nil, translateError("search text", err)
	}
	return hits, nil
}

// searchFilterClauses 把可选的收窄条件翻成 SQL 片段。
//
// 返回值以 " AND" 开头（直接拼在 WHERE 末尾），没有条件时返回空串。
// 只拼固定片段、值一律走参数，所以不存在注入面。
func searchFilterClauses(filter DocumentFilter) (string, []any) {
	var parts []string
	var args []any
	if filter.Collection != nil {
		parts = append(parts, "d.collection = ?")
		args = append(args, *filter.Collection)
	}
	if filter.Tag != nil {
		// ANY(tags) 走 documents_tags_idx（GIN），与列表筛选同一个写法。
		parts = append(parts, "? = ANY(d.tags)")
		args = append(args, *filter.Tag)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(parts, " AND "), args
}
