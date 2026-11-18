package agent

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// sqlInput 是 query_knowledge 的参数。
type sqlInput struct {
	SQL string `json:"sql" jsonschema:"description=一条只读 SELECT；必须显式写列名，单条语句"`
}

// sqlOutput 是给模型的查询结果。单元格统一转成字符串，避免 JSON 里混入
// time.Time / []byte 这类模型不认识的编码。
type sqlOutput struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	RowCount  int        `json:"row_count"`
	Truncated bool       `json:"truncated"`
	LatencyMS int64      `json:"latency_ms"`
}

// auditLog 收集本次 run 的 SQL 审计，run 结束后由 chat 落进 qa_runs.retrieved。
type auditLog struct {
	mu    sync.Mutex
	items []SQLAudit
}

func (l *auditLog) add(item SQLAudit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, item)
}

func (l *auditLog) all() []SQLAudit {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]SQLAudit, len(l.items))
	copy(out, l.items)
	return out
}

// sqlToolDescription 同时承担"说明"与"路由"：模型必须知道有哪些列、
// 哪些事不能做，否则它会写 SELECT *，然后被权限拒绝、反复重试。
const sqlToolDescription = `在只读数据库上执行一条 SELECT，用于统计、分组、筛选、时间范围查询。

只能查询当前用户的知识库数据，可用表与列：
- documents: id, title, source_type, mime, collection, tags, status, chunk_count, size_bytes, embed_status, embed_model, embed_dimensions, embedded_chunks, embedded_at, created_at, updated_at, owner_id
- document_chunks: id, document_id, seq, heading, start_offset, end_offset, token_count, embed_model, embedded_at, created_at

限制：
1. 只能是一条 SELECT 或 WITH 查询，不能修改数据。
2. 必须显式写列名；SELECT * 会因为没有 content 等列的权限而被拒绝。
3. 正文 content 不可读；问"内容讲了什么"要用 search_knowledge。
4. 最多返回 200 行，超过 3 秒会超时。`

// newSQLTool 构造 query_knowledge 工具。
//
// 租户 ID 在构造时由宿主传入，工具的 schema 里没有这个字段：
// 模型无法要求"查别人的数据"，代码也不会从 SQL 文本里解析租户。
func newSQLTool(runner SQLRunner, tenantID int64, budget *toolBudget, audits *auditLog) (tool.InvokableTool, error) {
	invoke := func(ctx context.Context, in sqlInput) (sqlOutput, error) {
		if err := budget.take(); err != nil {
			return sqlOutput{}, err
		}
		query := strings.TrimSpace(in.SQL)
		start := time.Now()

		result, err := runner.Run(ctx, tenantID, query)
		audit := SQLAudit{
			Kind:      "sql",
			Digest:    sqlDigest(query),
			LatencyMS: time.Since(start).Milliseconds(),
		}
		if err != nil {
			// 失败也留痕：模型可能反复写错，审计里要看得到它试了什么形状。
			audit.Error = truncateRunes(err.Error(), 200)
			audits.add(audit)
			return sqlOutput{}, err
		}

		audit.RowCount = len(result.Rows)
		audit.Truncated = result.Truncated
		audits.add(audit)

		return sqlOutput{
			Columns:   result.Columns,
			Rows:      result.Rows,
			RowCount:  len(result.Rows),
			Truncated: result.Truncated,
			LatencyMS: result.Latency.Milliseconds(),
		}, nil
	}

	return utils.InferTool("query_knowledge", sqlToolDescription, invoke)
}

// 编译期确认执行器满足工具依赖。
var _ SQLRunner = (*SQLExecutor)(nil)
