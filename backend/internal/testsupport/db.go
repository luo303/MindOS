// Package testsupport 给集成测试提供临时数据库。
//
// 存在的理由：每个包各写一份建库/删库逻辑，复制出来的第一个错误就是
// "管理连接用 defer 关闭"——defer 在 setup 返回时就执行，等到 t.Cleanup
// 去 DROP 时连接已经关了，错误被 `_ =` 吞掉，测试库于是一个个留下来。
// 曾积累到 367 个、2.9 GB。集中到这里之后，新增测试只需要一行。
package testsupport

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// DSNEnv 是指向"维护库"的连接串环境变量。
//
// 需要一个能 CREATE DATABASE 的连接（通常是 postgres 库），
// 测试自己会在它下面建临时库，不碰任何已有数据。
const DSNEnv = "MINDOS_TEST_DSN"

// NewDatabase 创建一个临时库并返回指向它的连接。
//
// 库名形如 `mindos_<prefix>_test_<纳秒>`：prefix 用来分辨是哪个包的用例留下的，
// 出现异常时不用猜。测试结束（包括失败、panic）由 t.Cleanup 删除。
//
// 未设置 DSNEnv 时跳过用例，而不是失败：本地没有数据库的人跑 `go test ./...`
// 不该红。
func NewDatabase(t *testing.T, prefix string) *sql.DB {
	t.Helper()

	adminDSN := os.Getenv(DSNEnv)
	if adminDSN == "" {
		t.Skipf("%s not set; skipping integration test", DSNEnv)
	}

	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	// 这里故意不 defer Close：Cleanup 里要先 DROP 再关连接。
	// 顺序反了 DROP 必然失败，且失败信息会被淹没在日志里。

	name := fmt.Sprintf("mindos_%s_test_%d", prefix, time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close()
		t.Fatalf("create test database %s: %v", name, err)
	}

	// 用 pgx 解析连接串再改库名，而不是字符串替换：
	// key=value 与 URI 两种形式都能正确处理。
	pgCfg, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		_ = admin.Close()
		t.Fatalf("parse %s: %v", DSNEnv, err)
	}
	pgCfg.Database = name
	db, err := sql.Open("pgx", stdlib.RegisterConnConfig(pgCfg))
	if err != nil {
		_ = admin.Close()
		t.Fatalf("open test db %s: %v", name, err)
	}

	t.Cleanup(func() {
		// 先关测试库自己的连接，再删库。WITH (FORCE) 也能强断，
		// 但依赖它意味着把 PG 13+ 的语法当成必需项，显式关更稳。
		_ = db.Close()
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + pgx.Identifier{name}.Sanitize() + " WITH (FORCE)"); err != nil {
			// 清理失败必须留痕：吞掉它，垃圾库就会悄悄堆积。
			t.Logf("drop test database %s failed: %v", name, err)
		}
		_ = admin.Close()
	})

	return db
}

// NewUser 插入一个最小用户并返回 id。
//
// 多租户下 documents / qa_runs / chat_models 都有 owner_id 外键，
// 集成测试必须先造一个归属。password_hash 写一个不可用的固定值：
// 测试不校验密码，真的算一次 bcrypt 只会让每个用例慢 200ms。
func NewUser(t *testing.T, db *gorm.DB, email string) int64 {
	t.Helper()

	var id int64
	err := db.Raw(
		`INSERT INTO users (email, password_hash, display_name)
		 VALUES (?, '!integration-test-only', 'test user')
		 RETURNING id`, email).Scan(&id).Error
	if err != nil {
		t.Fatalf("create test user %s: %v", email, err)
	}
	return id
}

// NewGorm 把一个 *sql.DB 包成 gorm 连接，配置与生产保持一致。
//
// TranslateError 必须开：否则唯一冲突不会变成 gorm.ErrDuplicatedKey，
// 测试就测不到线上那条错误翻译路径。
//
// 单独成一个函数而不是和建库合并：migrate 包自己也要测迁移，
// 它只能用一个还没迁移过的空库，合并成 NewMigratedDB 会形成循环导入。
func NewGorm(t *testing.T, db *sql.DB) *gorm.DB {
	t.Helper()

	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{
		SkipDefaultTransaction: true,
		TranslateError:         true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	return gdb
}
