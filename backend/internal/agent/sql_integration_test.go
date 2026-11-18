package agent

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/migrations"
)

// TestSQLExecutorIntegration 用真实数据库验证执行器：RLS、列授权、只读、
// 以及"租户由代码设置"这条边界。连接通过启动参数 role=... 切到只读角色，
// 这样不需要把 AI 角色的密码写进测试环境。
func TestSQLExecutorIntegration(t *testing.T) {
	admin := testsupport.NewDatabase(t, "agentsql")
	ctx := context.Background()

	if err := migrate.New(admin, migrations.FS).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	gdb := testsupport.NewGorm(t, admin)
	ownerA := testsupport.NewUser(t, gdb, "sql-a@example.com")
	ownerB := testsupport.NewUser(t, gdb, "sql-b@example.com")

	var docA, docB int64
	if err := gdb.Raw(
		`INSERT INTO documents (owner_id, title, source_type, mime, content_hash, chunk_count)
		 VALUES (?, 'a', 'file', 'text/markdown', 'hash-a', 1) RETURNING id`, ownerA).Scan(&docA).Error; err != nil {
		t.Fatalf("insert doc a: %v", err)
	}
	if err := gdb.Raw(
		`INSERT INTO documents (owner_id, title, source_type, mime, content_hash, chunk_count)
		 VALUES (?, 'b', 'file', 'text/markdown', 'hash-b', 1) RETURNING id`, ownerB).Scan(&docB).Error; err != nil {
		t.Fatalf("insert doc b: %v", err)
	}
	for _, doc := range []int64{docA, docB} {
		if err := gdb.Exec(
			`INSERT INTO document_chunks (document_id, seq, content, start_offset, end_offset, token_count)
			 VALUES (?, 0, 'chunk', 0, 5, 1)`, doc).Error; err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}

	var dbName string
	if err := admin.QueryRowContext(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatalf("current database: %v", err)
	}
	cfg, err := pgx.ParseConfig(os.Getenv(testsupport.DSNEnv))
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.Database = dbName
	// 用启动参数切角色：不需要 AI 角色密码，执行器仍然以该角色身份登录。
	cfg.RuntimeParams["role"] = "mindos_ai_reader"
	aiDB, err := sql.Open("pgx", stdlib.RegisterConnConfig(cfg))
	if err != nil {
		t.Fatalf("open ai db: %v", err)
	}
	defer aiDB.Close()

	if err := CheckAIReader(ctx, aiDB); err != nil {
		t.Fatalf("CheckAIReader: %v", err)
	}

	exec := NewSQLExecutor(aiDB)
	count := func(tenant int64, query string) string {
		t.Helper()
		result, err := exec.Run(ctx, tenant, query)
		if err != nil {
			t.Fatalf("run %q: %v", query, err)
		}
		if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
			t.Fatalf("result = %+v, want 1x1", result)
		}
		return result.Rows[0][0]
	}

	if got := count(ownerA, "SELECT count(*) FROM documents"); got != "1" {
		t.Errorf("owner A documents = %s, want 1", got)
	}
	if got := count(ownerB, "SELECT count(*) FROM documents"); got != "1" {
		t.Errorf("owner B documents = %s, want 1", got)
	}
	if got := count(ownerA, "SELECT count(*) FROM documents WHERE owner_id = "+itoa(ownerB)); got != "0" {
		t.Errorf("cross-tenant count = %s, want 0", got)
	}
	if got := count(ownerA, "SELECT count(*) FROM document_chunks"); got != "1" {
		t.Errorf("owner A chunks = %s, want 1", got)
	}

	for _, query := range []string{
		"SELECT content FROM documents",
		"SELECT * FROM documents",
		"SELECT count(*) FROM users",
		"SELECT set_config('app.tenant_id', '2', true)",
	} {
		if _, err := exec.Run(ctx, ownerA, query); !errors.Is(err, ErrSQLDenied) && !errors.Is(err, ErrSQLRejected) {
			t.Errorf("run %q = %v, want denied/rejected", query, err)
		}
	}

	for _, query := range []string{
		"DELETE FROM documents",
		"SELECT 1; SELECT 2",
	} {
		if _, err := exec.Run(ctx, ownerA, query); !errors.Is(err, ErrSQLRejected) {
			t.Errorf("run %q = %v, want ErrSQLRejected", query, err)
		}
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
