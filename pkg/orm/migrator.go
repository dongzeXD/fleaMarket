package orm

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// Migrator 依据实体对象创建或更新数据库表。
type Migrator struct {
	db *gorm.DB
}

// NewMigrator 创建迁移器。
func NewMigrator(db *gorm.DB) *Migrator { return &Migrator{db: db} }

// MigrateResult 单个表的迁移结果，供 dbctl 打印变更摘要。
type MigrateResult struct {
	Table   string
	Created bool
	// AddedColumns 本次新增的列。
	AddedColumns []string
}

// Migrate 对给定实体执行建表/加列。
//
// 行为边界（见 doc.md §7.2）：只做「增量」——建表、加列、加索引。
// GORM 的 AutoMigrate 本身不删列、不缩窄字段类型，这正是我们需要的保守语义：
// 删列与改类型是有损操作，必须走人工审核的 SQL 脚本（deploy/sql/manual/），
// 不能由一个随手执行的命令完成。
//
// 返回的结果中包含每个表的变更摘要，便于在 CI 或人工执行时确认影响面。
func (m *Migrator) Migrate(ctx context.Context, es []Entity) ([]MigrateResult, error) {
	db := m.db.WithContext(ctx)
	mig := db.Migrator()

	results := make([]MigrateResult, 0, len(es))
	for _, e := range es {
		table := e.TableName()

		existed := mig.HasTable(e)
		var before []string
		if existed {
			cols, err := mig.ColumnTypes(e)
			if err != nil {
				return nil, fmt.Errorf("orm: inspect columns of %s: %w", table, err)
			}
			for _, c := range cols {
				before = append(before, c.Name())
			}
		}

		if err := mig.AutoMigrate(e); err != nil {
			return nil, fmt.Errorf("orm: migrate %s: %w", table, err)
		}

		res := MigrateResult{Table: table, Created: !existed}
		if existed {
			cols, err := mig.ColumnTypes(e)
			if err != nil {
				return nil, fmt.Errorf("orm: inspect columns of %s: %w", table, err)
			}
			res.AddedColumns = diffColumns(before, cols)
		}
		results = append(results, res)
	}
	return results, nil
}

func diffColumns(before []string, after []gorm.ColumnType) []string {
	old := make(map[string]struct{}, len(before))
	for _, c := range before {
		old[c] = struct{}{}
	}
	var added []string
	for _, c := range after {
		if _, ok := old[c.Name()]; !ok {
			added = append(added, c.Name())
		}
	}
	sort.Strings(added)
	return added
}

// DumpDDL 导出给定实体当前在数据库中的建表语句快照。
//
// 为什么需要快照（doc.md §7.2）：「由实体反推建表」最容易失控的地方在于
// schema 变更不可见——改一个 struct tag 就悄悄改了线上表结构。
// 把 DDL 快照纳入 git，任何 schema 变动都会在 code review 的 diff 中显现。
//
// 注意本方法读取的是数据库的真实结构，因此需先执行 Migrate。
func (m *Migrator) DumpDDL(ctx context.Context, es []Entity) (string, error) {
	db := m.db.WithContext(ctx)

	var sb strings.Builder
	sb.WriteString("-- 由 `dbctl dump` 自动生成，请勿手工编辑。\n")
	sb.WriteString("-- 该文件是数据库真实结构的快照，用于在 code review 中暴露 schema 变更。\n\n")

	for _, e := range es {
		table := e.TableName()
		if !db.Migrator().HasTable(e) {
			return "", fmt.Errorf("orm: table %s does not exist, run migrate first", table)
		}
		var name, ddl string
		// SHOW CREATE TABLE 返回两列：Table、Create Table。
		row := db.Raw("SHOW CREATE TABLE `" + table + "`").Row()
		if err := row.Scan(&name, &ddl); err != nil {
			return "", fmt.Errorf("orm: show create table %s: %w", table, err)
		}
		sb.WriteString("-- ----------------------------\n")
		sb.WriteString("-- " + table + "\n")
		sb.WriteString("-- ----------------------------\n")
		sb.WriteString(ddl)
		sb.WriteString(";\n\n")
	}
	return sb.String(), nil
}

// Drop 删除给定实体对应的表。
//
// 本方法不做环境判断——那是 dbctl 命令层的职责（检查 APP_ENV 与 --yes）。
// 公共库只提供能力，不替调用方决定安全策略，否则公共库就与部署环境的
// 命名约定耦合了。
//
// 删除顺序与传入顺序相反，以便先删引用方、后删被引用方。
func (m *Migrator) Drop(ctx context.Context, es []Entity) error {
	db := m.db.WithContext(ctx)
	mig := db.Migrator()
	for i := len(es) - 1; i >= 0; i-- {
		e := es[i]
		if !mig.HasTable(e) {
			continue
		}
		if err := mig.DropTable(e); err != nil {
			return fmt.Errorf("orm: drop table %s: %w", e.TableName(), err)
		}
	}
	return nil
}
