// Package platform 负责"外部依赖的连接与生命周期"：数据库、Redis、RabbitMQ。
// 它只关心如何建立连接、如何健康检查、如何关闭，不包含任何业务逻辑。
package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql 的 pgx 驱动
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/luo303/mindos/backend/internal/config"
)

// SlowQueryThreshold 超过它的 SQL 会被记录为 warn，便于早期发现缺失的索引。
const SlowQueryThreshold = 200 * time.Millisecond

// NewPostgres 创建 GORM 连接，并用带超时的 Ping 确认数据库真的可用。
//
// 三个容易踩的坑：
//  1. gorm.Open 是懒连接，不 Ping 的话启动时数据库不可用也不会报错；
//  2. 连接池参数设置在底层 *sql.DB 上，而不是 GORM 上；
//  3. TranslateError=true 会把数据库错误翻译成 gorm.ErrDuplicatedKey / ErrForeignKeyViolated，
//     业务层就不需要去解析 PostgreSQL 的错误码了。
func NewPostgres(ctx context.Context, cfg config.DBConfig, log *zap.Logger) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		SkipDefaultTransaction: true, // 单条写语句没必要再包一层事务
		TranslateError:         true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
		// 直接使用官方 logger 实现：它自带慢 SQL 阈值、错误过滤和参数填充，
		// 第二个参数换成实现了 logger.Writer 的 zapWriter，日志就进了我们的结构化日志通道。
		// （自己实现 logger.Interface 也可以，但要手动处理格式化、行号等细节，没必要。）
		Logger: gormlogger.New(
			zapWriter{log: log.Named("gorm")},
			gormlogger.Config{
				SlowThreshold:             SlowQueryThreshold,
				LogLevel:                  gormLoggerLevel(cfg), // dev 下打全部 SQL，生产只打慢 SQL 和错误
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		),
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping postgres %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Name, err)
	}

	return db, nil
}

// NewReadOnlyPool 用给定账号打开一个独立的小连接池。
//
// 它和主库池刻意分开：
//   - 账号不同（AI 只读角色），池子大小与生命周期互不影响；
//   - 用 URL 形式的 DSN，密码里有空格/引号也不会把 key=value 串拼坏；
//   - 池上限压到 4，与角色的 CONNECTION LIMIT 对齐，避免应用自己把角色连满。
func NewReadOnlyPool(ctx context.Context, cfg config.DBConfig, user, password string) (*sql.DB, error) {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   "/" + cfg.Name,
	}
	q := u.Query()
	q.Set("sslmode", cfg.SSLMode)
	q.Set("TimeZone", "UTC")
	u.RawQuery = q.Encode()

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		return nil, fmt.Errorf("open read-only postgres: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(10 * time.Minute)
	db.SetConnMaxIdleTime(time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping read-only postgres: %w", err)
	}
	return db, nil
}

// ClosePostgres 关闭底层连接池。
func ClosePostgres(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// DBPing 供健康检查使用：在给定超时内确认数据库可用。
func DBPing(ctx context.Context, timeout time.Duration, db *gorm.DB) error {
	if db == nil {
		return errors.New("database not initialized")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return sqlDB.PingContext(ctx)
}
