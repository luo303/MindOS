package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// sqlTimeout 是单条查询的硬超时。角色上的 statement_timeout 也是 3s，
	// 这里再设一次是为了让 ctx 先取消、连接尽快回收，而不是等服务端断开。
	sqlTimeout = 3 * time.Second

	// sqlMaxRows / sqlMaxBytes 是给模型的上下文预算。
	sqlMaxRows  = 200
	sqlMaxBytes = 64 << 10

	// sqlMaxRunes 是模型提交的 SQL 文本上限。
	sqlMaxRunes = 2000

	// sqlCellRunes 是单个单元格的长度上限。
	sqlCellRunes = 500
)

// ErrSQLRejected 表示 SQL 没通过形状校验（不是数据库报错）。
var ErrSQLRejected = errors.New("sql rejected")

// ErrSQLDenied 表示数据库拒绝了对象/列访问。对模型只回一句通用提示，
// 不回对象名——避免把"库里有哪张表"当成可探测的信息。
var ErrSQLDenied = errors.New("sql permission denied")

// SQLResult 是一条只读查询的结果，字段都是给模型看的。
type SQLResult struct {
	Columns   []string
	Rows      [][]string
	Truncated bool
	Latency   time.Duration
}

// SQLAudit 是一次 SQL 调用的审计记录，落进 qa_runs.retrieved。
//
// 不存完整 SQL、也不存结果：审计只需要"查过、查到了几行、花了多久"，
// 存原文等于把用户数据复制进审计表。
type SQLAudit struct {
	Kind      string `json:"kind"`
	Digest    string `json:"sql_digest,omitempty"`
	RowCount  int    `json:"row_count,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SQLRunner 是工具需要的执行能力。*SQLExecutor 满足它；测试可以换成假的。
type SQLRunner interface {
	Run(ctx context.Context, tenantID int64, query string) (SQLResult, error)
}

// SQLExecutor 在独立的只读连接池上执行模型生成的 SELECT。
//
// 它不碰 GORM、不拿主库句柄：签名上就只有 *sql.DB，而且这个池子用的是
// 只读角色。即使校验漏了什么，数据库层还有角色权限、RLS、只读事务三道。
type SQLExecutor struct {
	db *sql.DB
}

// NewSQLExecutor 包装一个已 Ping 过的只读连接池。
func NewSQLExecutor(db *sql.DB) *SQLExecutor { return &SQLExecutor{db: db} }

// Run 执行一条查询。tenantID 由调用方从已认证的 Principal 取出，
// 不接受模型提供的任何租户字段。
func (e *SQLExecutor) Run(ctx context.Context, tenantID int64, raw string) (SQLResult, error) {
	if err := validateSQL(raw); err != nil {
		return SQLResult{}, err
	}
	if e == nil || e.db == nil {
		return SQLResult{}, errors.New("sql executor not configured")
	}

	ctx, cancel := context.WithTimeout(ctx, sqlTimeout)
	defer cancel()

	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SQLResult{}, fmt.Errorf("begin readonly tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// tenantID 是 int64，拼出来只可能是数字；这里不接受任何字符串。
	stmt := "SET LOCAL app.tenant_id = '" + strconv.FormatInt(tenantID, 10) + "'"
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return SQLResult{}, fmt.Errorf("set tenant: %w", err)
	}
	// 会话级护栏跟着事务走，不依赖角色的全局默认值（那会变成跨库共享状态）。
	for _, setting := range []string{
		"SET LOCAL statement_timeout = '3s'",
		"SET LOCAL lock_timeout = '1s'",
		"SET LOCAL search_path = public",
	} {
		if _, err := tx.ExecContext(ctx, setting); err != nil {
			return SQLResult{}, fmt.Errorf("set session guard: %w", err)
		}
	}

	start := time.Now()
	rows, err := tx.QueryContext(ctx,
		"SELECT * FROM ("+raw+") AS q LIMIT "+strconv.Itoa(sqlMaxRows+1))
	if err != nil {
		return SQLResult{}, classifySQLError(err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return SQLResult{}, fmt.Errorf("read columns: %w", err)
	}

	// database/sql 的 Scan 需要一组指针；先用 any 接住，再按类型转成字符串。
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}

	result := SQLResult{Columns: cols, Rows: make([][]string, 0, sqlMaxRows)}
	totalBytes := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return SQLResult{}, fmt.Errorf("scan row: %w", err)
		}
		if len(result.Rows) == sqlMaxRows {
			result.Truncated = true
			break
		}
		row := make([]string, len(values))
		for i, v := range values {
			cell := formatSQLCell(v)
			totalBytes += len(cell)
			row[i] = cell
		}
		if totalBytes > sqlMaxBytes {
			result.Truncated = true
			break
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return SQLResult{}, classifySQLError(err)
	}
	result.Latency = time.Since(start)
	return result, nil
}

// validateSQL 只做"形状"校验：一条 SELECT/WITH、没有分号、长度受限。
//
// 它不是安全边界（安全边界是角色权限 + 只读事务 + 包装后的单语句），
// 目的是把明显不对的输入在发库之前挡掉，并给模型一个可读的错误。
func validateSQL(raw string) error {
	sqlText := strings.TrimSpace(raw)
	if sqlText == "" {
		return fmt.Errorf("%w: empty sql", ErrSQLRejected)
	}

	// 允许末尾一个分号，但中间不允许：多条语句直接拒绝。
	for strings.HasSuffix(sqlText, ";") {
		sqlText = strings.TrimSpace(strings.TrimSuffix(sqlText, ";"))
	}
	if strings.Contains(sqlText, ";") {
		return fmt.Errorf("%w: only a single statement is allowed", ErrSQLRejected)
	}
	if utf8.RuneCountInString(sqlText) > sqlMaxRunes {
		return fmt.Errorf("%w: sql is too long (max %d)", ErrSQLRejected, sqlMaxRunes)
	}

	head, ok := firstKeyword(sqlText)
	if !ok || (head != "SELECT" && head != "WITH") {
		return fmt.Errorf("%w: only SELECT or WITH is allowed", ErrSQLRejected)
	}
	// 函数已经被收回，这里再挡一次是为了给模型明确的提示，
	// 而不是让它拿到一个 42501 后反复重试。
	if strings.Contains(strings.ToLower(sqlText), "set_config(") {
		return fmt.Errorf("%w: set_config is not allowed", ErrSQLRejected)
	}
	return nil
}

// firstKeyword 跳过前导的空白与注释，返回第一个单词的大写形式。
func firstKeyword(s string) (string, bool) {
	for {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, 10); i >= 0 {
				s = s[i+1:]
				continue
			}
			return "", false
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s, "*/")
			if i < 0 {
				return "", false
			}
			s = s[i+2:]
			continue
		}
		break
	}
	if s == "" {
		return "", false
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return "", false
	}
	return strings.ToUpper(s[:end]), true
}

// classifySQLError 把数据库错误翻成两类：权限类回通用提示，其余原样回给模型。
//
// 不回对象名：模型本来就知道自己写了什么表；对"猜表名"的探测，
// 统一说"没有权限"比"关系 users 不存在/权限不足"更少泄漏信息。
func classifySQLError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42501", "42P01":
			return fmt.Errorf("%w: only documents / document_chunks with granted columns are readable", ErrSQLDenied)
		}
	}
	return fmt.Errorf("sql error: %w", err)
}

// formatSQLCell 把驱动返回值转成模型能读的字符串。
func formatSQLCell(v any) string {
	var s string
	switch value := v.(type) {
	case nil:
		s = "NULL"
	case []byte:
		s = string(value)
	case time.Time:
		s = value.UTC().Format(time.RFC3339)
	default:
		s = fmt.Sprint(value)
	}
	return truncateRunes(s, sqlCellRunes)
}

// sqlDigest 只保留 SQL 的指纹，不把原文写进审计。
func sqlDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
