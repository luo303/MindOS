package platform

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// VectorExtensionPresent 检查当前数据库是否启用了 pgvector 扩展。
//
// 返回值区分两种"不算成功"的情况，这是 /system/status 能定位
// "扩展缺失"这一类问题的关键：
//   - (false, nil)：库能连，但扩展没装——属于"缺东西"，不是故障；
//   - (_, err)：数据库本身不可用——属于故障。
//
// 把这两种情况混在一起，状态接口就只能输出"down"，人无法判断该装扩展还是修连接。
func VectorExtensionPresent(ctx context.Context, db *gorm.DB) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("database not initialized")
	}
	var exists bool
	err := db.WithContext(ctx).
		Raw("SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')").
		Scan(&exists).Error
	if err != nil {
		return false, fmt.Errorf("check vector extension: %w", err)
	}
	return exists, nil
}

// VectorColumnType 返回某个向量列的声明类型，例如 "vector(1024)"。
//
// 用途是启动自检：代码里的维度常量与迁移里的列维度必须一致，
// 不一致时每一次写入都会失败（"expected 1024 dimensions, not 1536"）。
// 这种问题不该等用户第一次导入才暴露——那时错误信息还会被包成
// "process_failed"，看不出是 schema 问题。
//
// 用 format_type 而不是读 atttypmod：typmod 的编码方式由类型自己决定
// （varchar 要减 4，vector 直接存维度），直接比字符串更不容易踩坑。
func VectorColumnType(ctx context.Context, db *gorm.DB, table, column string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database not initialized")
	}
	var typ *string
	err := db.WithContext(ctx).
		Raw(`SELECT format_type(atttypid, atttypmod)
		     FROM pg_attribute
		     WHERE attrelid = to_regclass(?) AND attname = ? AND NOT attisdropped`,
			table, column).
		Scan(&typ).Error
	if err != nil {
		return "", fmt.Errorf("read column type of %s.%s: %w", table, column, err)
	}
	if typ == nil {
		return "", fmt.Errorf("column %s.%s does not exist", table, column)
	}
	return *typ, nil
}
