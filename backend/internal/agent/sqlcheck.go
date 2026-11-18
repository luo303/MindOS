package agent

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CheckAIReader 在启动时确认只读账号真的"只读、只看得到两张表、受 RLS 约束"。
//
// 为什么要自检而不是相信迁移：迁移可能没跑、角色可能被别人改过，
// 而这类错误不会在正常查询里暴露——直到某天模型写出 `SELECT * FROM users`。
// 自检失败直接拒绝启动，比"带病运行"安全。
//
// 只检查"结构性"条件，不查数据：它不需要知道任何人的数据。
func CheckAIReader(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("ai reader check: nil pool")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var issues []string

	var super, bypass bool
	if err := db.QueryRowContext(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&super, &bypass); err != nil {
		return fmt.Errorf("ai reader check: read role attrs: %w", err)
	}
	if super {
		issues = append(issues, "role is superuser")
	}
	if bypass {
		issues = append(issues, "role has BYPASSRLS")
	}

	var owned int
	if err := db.QueryRowContext(ctx, `
SELECT count(*)
  FROM pg_class c
  JOIN pg_roles r ON r.oid = c.relowner
 WHERE c.relname IN ('documents', 'document_chunks')
   AND r.rolname = current_user`).Scan(&owned); err != nil {
		return fmt.Errorf("ai reader check: read table ownership: %w", err)
	}
	if owned > 0 {
		issues = append(issues, "role owns a knowledge table")
	}

	rows, err := db.QueryContext(ctx, `
SELECT c.relname, c.relrowsecurity,
       (SELECT count(*) FROM pg_policies p
         WHERE p.schemaname = 'public' AND p.tablename = c.relname) AS policies
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relname IN ('documents', 'document_chunks')`)
	if err != nil {
		return fmt.Errorf("ai reader check: read rls state: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		var enabled bool
		var policies int
		if err := rows.Scan(&name, &enabled, &policies); err != nil {
			return fmt.Errorf("ai reader check: scan rls state: %w", err)
		}
		seen[name] = true
		if !enabled {
			issues = append(issues, name+" does not have RLS enabled")
		}
		if policies == 0 {
			issues = append(issues, name+" has no RLS policy")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("ai reader check: iterate rls state: %w", err)
	}
	for _, name := range []string{"documents", "document_chunks"} {
		if !seen[name] {
			issues = append(issues, name+" is missing")
		}
	}

	if can, err := boolQuery(ctx, db,
		`SELECT has_function_privilege(current_user, 'pg_catalog.set_config(text,text,boolean)', 'EXECUTE')`); err != nil {
		return err
	} else if can {
		issues = append(issues, "role can execute set_config")
	}

	for _, table := range []string{"users", "sessions", "chat_models", "qa_runs"} {
		if can, err := boolQuery(ctx, db,
			`SELECT has_table_privilege(current_user, $1, 'SELECT')`, table); err != nil {
			return err
		} else if can {
			issues = append(issues, "role can read "+table)
		}
	}

	for _, col := range [][2]string{
		{"documents", "content"},
		{"documents", "storage_path"},
		{"document_chunks", "content"},
		{"document_chunks", "embedding"},
	} {
		if can, err := boolQuery(ctx, db,
			`SELECT has_column_privilege(current_user, $1, $2, 'SELECT')`, col[0], col[1]); err != nil {
			return err
		} else if can {
			issues = append(issues, "role can read "+col[0]+"."+col[1])
		}
	}

	// 正向检查：统计列必须可读，否则工具一启动就是坏的。
	for _, col := range [][2]string{
		{"documents", "title"},
		{"documents", "chunk_count"},
		{"document_chunks", "token_count"},
	} {
		if can, err := boolQuery(ctx, db,
			`SELECT has_column_privilege(current_user, $1, $2, 'SELECT')`, col[0], col[1]); err != nil {
			return err
		} else if !can {
			issues = append(issues, "role cannot read "+col[0]+"."+col[1])
		}
	}

	if len(issues) > 0 {
		return fmt.Errorf("ai reader self-check failed: %s", strings.Join(issues, "; "))
	}
	return nil
}

// boolQuery 执行一条返回单布尔值的查询。
func boolQuery(ctx context.Context, db *sql.DB, query string, args ...any) (bool, error) {
	var value bool
	if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		return false, fmt.Errorf("ai reader check: query: %w", err)
	}
	return value, nil
}
