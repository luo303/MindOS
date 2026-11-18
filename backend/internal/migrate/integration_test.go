package migrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/testsupport"
	"github.com/luo303/mindos/backend/migrations"
)

// 本文件是真实 PostgreSQL 集成测试，覆盖迁移的质量门槛：
// up → down → up、重复 up 幂等、dirty 中断识别与 force 恢复。
//
// 运行方式：先启动 compose，再设置指向"postgres 维护库"的连接串：
//
//	MINDOS_TEST_DSN="host=localhost port=5432 user=mindos password=mindos_dev_password dbname=postgres sslmode=disable"
//	go test -race -count=1 -run Integration ./internal/migrate/
//
// 未设置该变量时全部跳过（CI 之外不强制有数据库），但迁移验收必须实测通过。
// 测试会创建 mindos_migrate_test_<随机> 数据库并在结束时 DROP，不碰开发库。

// setupTestDB 创建独立测试库并返回指向它的 Runner。
// 用独立库而不是共享开发库：集成测试要执行 down（删扩展），
// 跑在开发库上会把别人正在用的环境拆掉。
func setupTestDB(t *testing.T) *Runner {
	t.Helper()
	db := testsupport.NewDatabase(t, "migrate")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping test db: %v", err)
	}
	return New(db, migrations.FS)
}

func TestUpDownUpIntegration(t *testing.T) {
	runner := setupTestDB(t)
	ctx := context.Background()

	// 从未迁移过：Version 不报错误返回零值。
	st, err := runner.Version(ctx)
	if err != nil {
		t.Fatalf("Version on fresh db: %v", err)
	}
	if st.Version != 0 || st.Dirty {
		t.Fatalf("fresh db status = %+v, want zero", st)
	}

	// 回归：表已建但没有记录（ensureTable 之后、第一条迁移之前）也必须等于
	// "从未迁移"。早期实现只把"表不存在"当零值，漏了 QueryRow 的 ErrNoRows，
	// 导致空库首次 up 直接失败：Up 先建表再查版本，每次都撞在 ErrNoRows 上。
	if err := runner.ensureTable(ctx); err != nil {
		t.Fatalf("ensureTable: %v", err)
	}
	st, err = runner.Version(ctx)
	if err != nil {
		t.Fatalf("Version on empty schema_migrations: %v", err)
	}
	if st.Version != 0 || st.Dirty {
		t.Fatalf("empty schema_migrations status = %+v, want zero", st)
	}

	// up：应用到最新版本。
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	st, err = runner.Version(ctx)
	if err != nil {
		t.Fatalf("Version after up: %v", err)
	}
	latest, err := runner.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if st.Version != latest || st.Dirty {
		t.Fatalf("after up = %+v, want version %d clean", st, latest)
	}

	// 重复 up 幂等：不报错、版本不变。
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("second Up should be no-op: %v", err)
	}
	st, _ = runner.Version(ctx)
	if st.Version != latest {
		t.Fatalf("version changed after no-op up: %d", st.Version)
	}

	// down 的语义是"回滚最近一个"（与 CLI 帮助、README 一致），所以这里逐个回滚到 0。
	// 原来只调一次就断言归零，在只有一个迁移时恰好成立；迁移数增加后必然失败，
	// 而这条用例要求 MINDOS_TEST_DSN，平时不跑就发现不了。
	before, _ := runner.Version(ctx)
	if err := runner.Down(ctx); err != nil {
		t.Fatalf("Down: %v", err)
	}
	st, err = runner.Version(ctx)
	if err != nil {
		t.Fatalf("Version after down: %v", err)
	}
	if st.Version != before.Version-1 {
		t.Fatalf("one down moved version %d → %d, want exactly one step back", before.Version, st.Version)
	}

	// 剩下的逐个回滚，验证每个 down 脚本都能真的把结构拆干净。
	for st.Version > 0 {
		if err := runner.Down(ctx); err != nil {
			t.Fatalf("Down at version %d: %v", st.Version, err)
		}
		if st, err = runner.Version(ctx); err != nil {
			t.Fatalf("Version after down: %v", err)
		}
	}
	if st.Version != 0 {
		t.Fatalf("after rolling back all, version = %d, want 0", st.Version)
	}

	// 再 up 回到最新（空库 up→down→up 的完整循环）。
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("Up again: %v", err)
	}
	st, _ = runner.Version(ctx)
	if st.Version != latest || st.Dirty {
		t.Fatalf("after re-up = %+v, want version %d clean", st, latest)
	}

	// 验证 0001 迁移真的启用了扩展（而不只是版本号写对了）。
	var extExists bool
	row := runner.db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')")
	if err := row.Scan(&extExists); err != nil {
		t.Fatalf("check extension: %v", err)
	}
	if !extExists {
		t.Fatal("pgvector extension should be enabled after up")
	}
}

// TestAIReaderMigrationIntegration 验证 0014 的角色、策略与列授权真的生效，
// 并验证 down 只拆本库的策略/授权、不删共享角色。
func TestAIReaderMigrationIntegration(t *testing.T) {
	runner := setupTestDB(t)
	ctx := context.Background()

	if err := runner.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}

	mustBool := func(query string) bool {
		t.Helper()
		var value bool
		if err := runner.db.QueryRowContext(ctx, query).Scan(&value); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		return value
	}

	var super, bypass bool
	if err := runner.db.QueryRowContext(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = 'mindos_ai_reader'`,
	).Scan(&super, &bypass); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if super || bypass {
		t.Fatalf("role attrs = super:%v bypass:%v, want both false", super, bypass)
	}

	var policies int
	if err := runner.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_policies WHERE tablename IN ('documents', 'document_chunks')`,
	).Scan(&policies); err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if policies != 2 {
		t.Fatalf("policies = %d, want 2", policies)
	}

	if !mustBool(`SELECT has_column_privilege('mindos_ai_reader', 'public.documents', 'title', 'SELECT')`) {
		t.Error("documents.title should be readable")
	}
	if mustBool(`SELECT has_column_privilege('mindos_ai_reader', 'public.documents', 'content', 'SELECT')`) {
		t.Error("documents.content must not be readable")
	}
	if mustBool(`SELECT has_column_privilege('mindos_ai_reader', 'public.document_chunks', 'content', 'SELECT')`) {
		t.Error("document_chunks.content must not be readable")
	}
	if mustBool(`SELECT has_function_privilege('mindos_ai_reader', 'pg_catalog.set_config(text,text,boolean)', 'EXECUTE')`) {
		t.Error("set_config must not be executable")
	}

	if err := runner.Down(ctx); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if err := runner.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_policies WHERE tablename IN ('documents', 'document_chunks')`,
	).Scan(&policies); err != nil {
		t.Fatalf("count policies after down: %v", err)
	}
	if policies != 0 {
		t.Fatalf("policies after down = %d, want 0", policies)
	}
	if mustBool(`SELECT has_column_privilege('mindos_ai_reader', 'public.documents', 'title', 'SELECT')`) {
		t.Error("column privilege should be revoked by down")
	}
	// 角色是集群级的：down 不删它，否则别的库还在引用时会直接失败。
	if !mustBool(`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mindos_ai_reader')`) {
		t.Error("role should survive down; cleanup is manual after all databases are down")
	}

	if err := runner.Up(ctx); err != nil {
		t.Fatalf("Up again: %v", err)
	}
}

// TestDirtyRecoveryIntegration 覆盖"迁移中断"场景：
// 手工把版本标记弄脏，验证 Up 拒绝执行、Force 能恢复、恢复后可继续。
func TestDirtyRecoveryIntegration(t *testing.T) {
	runner := setupTestDB(t)
	ctx := context.Background()

	if err := runner.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	latest, _ := runner.Latest()

	// 模拟"执行到一半崩溃"：version 标记为 dirty。
	// 用不存在的版本号 999，验证报错信息能原样带回这个版本。
	if _, err := runner.db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, dirty) VALUES (999, true)`); err != nil {
		t.Fatalf("simulate dirty: %v", err)
	}

	st, err := runner.Version(ctx)
	if err != nil {
		t.Fatalf("Version with dirty: %v", err)
	}
	if !st.Dirty || st.Version != 999 {
		t.Fatalf("dirty status = %+v, want version 999 dirty", st)
	}

	// dirty 状态下 up / down 都必须拒绝，而不是带着脏标记继续跑。
	if err := runner.Up(ctx); err == nil {
		t.Fatal("Up should refuse while dirty")
	} else if !strings.Contains(err.Error(), "999") {
		t.Fatalf("dirty error should name version 999: %v", err)
	}
	if err := runner.Down(ctx); err == nil {
		t.Fatal("Down should refuse while dirty")
	}

	// 恢复路径一：确认结构实际完整后 force 到真实版本。
	if err := runner.Force(ctx, latest); err != nil {
		t.Fatalf("Force: %v", err)
	}
	st, err = runner.Version(ctx)
	if err != nil {
		t.Fatalf("Version after force: %v", err)
	}
	if st.Version != latest || st.Dirty {
		t.Fatalf("after force = %+v, want version %d clean", st, latest)
	}

	// 恢复后一切照常：up 是幂等空操作。
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("Up after recovery: %v", err)
	}
}

// TestForceToZeroIntegration 验证 force 0 把库恢复到"从未迁移"状态。
func TestForceToZeroIntegration(t *testing.T) {
	runner := setupTestDB(t)
	ctx := context.Background()

	if err := runner.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := runner.Force(ctx, 0); err != nil {
		t.Fatalf("Force 0: %v", err)
	}
	st, err := runner.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if st.Version != 0 || st.Dirty {
		t.Fatalf("after force 0 = %+v, want zero", st)
	}
}
