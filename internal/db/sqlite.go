package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// ============================================================
// SQLiteStore — 基于 SQLite 的存储实现
// 使用纯 Go 驱动 modernc.org/sqlite，无需 CGO
// ============================================================

// SQLiteStore 基于 SQLite 的存储实现
type SQLiteStore struct {
	db *sql.DB
}

// 编译时接口检查
var _ Store = (*SQLiteStore)(nil)

// NewSQLiteStore 创建 SQLite 存储实例
func NewSQLiteStore(ctx context.Context, dsn string) (*SQLiteStore, error) {
	if dsn == "" {
		dsn = "./community.db"
	}

	// 启用 WAL 模式和外键约束
	connStr := fmt.Sprintf("%s?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000", dsn)

	sqlDB, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 数据库失败: %w", err)
	}

	// SQLite 单写多读，限制连接数
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("SQLite 连接测试失败: %w", err)
	}

	return &SQLiteStore{db: sqlDB}, nil
}

// ============================================================
// 迁移
// ============================================================

// Migrate 执行数据库迁移
func (s *SQLiteStore) Migrate(ctx context.Context) error {
	data, err := MigrationFS.ReadFile("migrations/001_init_sqlite.sql")
	if err != nil {
		return fmt.Errorf("读取迁移脚本失败: %w", err)
	}

	// 按分号拆分并逐条执行
	statements := splitSQL(string(data))
	for _, stmt := range statements {
		if stmt = strings.TrimSpace(stmt); stmt == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("执行迁移语句失败: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}

// Close 关闭数据库连接
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// ============================================================
// 内部工具函数
// ============================================================

// splitSQL 按分号拆分 SQL（跳过引号内的分号、行注释和块注释）
// 支持: 单引号字符串、双引号标识符、-- 行注释、/* */ 块注释
func splitSQL(raw string) []string {
	var result []string
	var buf strings.Builder
	inSingleQuote := false
	inDoubleQuote := false

	for i := 0; i < len(raw); i++ {
		ch := raw[i]

		// 单引号开关
		if ch == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			buf.WriteByte(ch)
			continue
		}

		// 双引号开关
		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			buf.WriteByte(ch)
			continue
		}

		inQuote := inSingleQuote || inDoubleQuote

		// 引号外遇到分号 → 语句边界
		if ch == ';' && !inQuote {
			stmt := strings.TrimSpace(buf.String())
			if stmt != "" && !strings.HasPrefix(stmt, "--") {
				result = append(result, stmt)
			}
			buf.Reset()
			continue
		}

		// 引号外遇到块注释 /* → 跳到 */
		if ch == '/' && !inQuote && i+1 < len(raw) && raw[i+1] == '*' {
			i += 2 // 跳过 /*
			for i < len(raw)-1 {
				if raw[i] == '*' && raw[i+1] == '/' {
					i++ // 跳过 */
					break
				}
				i++
			}
			buf.WriteByte(' ')
			continue
		}

		// 引号外遇到行注释 → 跳到行尾
		if ch == '-' && !inQuote && i+1 < len(raw) && raw[i+1] == '-' {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			buf.WriteByte('\n')
			continue
		}

		buf.WriteByte(ch)
	}

	// 处理末尾无分号的语句
	if stmt := strings.TrimSpace(buf.String()); stmt != "" && !strings.HasPrefix(stmt, "--") {
		result = append(result, stmt)
	}
	return result
}
