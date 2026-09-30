// Command dbctl 依据各服务声明的实体对象创建与维护数据库表。
//
// 这是需求「需要脚本根据实体（表）对象创建数据库表」的实现（doc.md §7）。
//
// 工作原理：
//  1. 各服务在 app/<svc>/entity/entity.go 的 init() 中向 pkg/orm 的全局
//     注册中心登记实体；
//  2. 本程序 blank import 这些包，触发 init() 完成注册；
//  3. 按分组取出实体，交给 pkg/orm 的 Migrator 执行。
//
// 新增服务时，只需在下方 import 块加一行 —— 这是唯一需要手工维护的地方。
//
// 用法:
//
//	dbctl migrate [--group user,product]     建表/加列
//	dbctl dump --out deploy/sql/schema.sql   导出 DDL 快照
//	dbctl drop --yes [--group user]          删表（仅 APP_ENV=dev）
//	dbctl list                               列出已注册的表
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fleamarket/pkg/orm"
	"gorm.io/gorm"

	// 各服务的实体包。import 的副作用（init 注册）即是目的，
	// 因此必须用 blank import 并保留下面的注释以防被工具自动清理。
	_ "github.com/fleamarket/app/product/entity"
	_ "github.com/fleamarket/app/review/entity"
	_ "github.com/fleamarket/app/trade/entity"
	_ "github.com/fleamarket/app/user/entity"
)

const defaultDSN = "fleamarket:fleamarket@tcp(127.0.0.1:3306)/fleamarket?charset=utf8mb4&parseTime=True&loc=Local"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "dbctl: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("缺少子命令")
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "migrate":
		return cmdMigrate(rest)
	case "dump":
		return cmdDump(rest)
	case "drop":
		return cmdDrop(rest)
	case "list":
		return cmdList(rest)
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("未知子命令 %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dbctl —— 依据实体对象管理数据库表

子命令:
  migrate   建表与加列（增量，不删列、不改窄类型）
  dump      导出数据库真实结构的 DDL 快照
  drop      删表，仅 APP_ENV=dev 且带 --yes 时允许
  list      列出已注册的分组与表

公共参数:
  --dsn     数据库连接串，默认取环境变量 DB_DSN
  --group   逗号分隔的分组名，留空表示全部
`)
}

// commonFlags 各子命令共享的参数。
type commonFlags struct {
	dsn    string
	groups string
}

func bindCommon(fs *flag.FlagSet, c *commonFlags) {
	fs.StringVar(&c.dsn, "dsn", os.Getenv("DB_DSN"), "数据库连接串，默认取环境变量 DB_DSN")
	fs.StringVar(&c.groups, "group", "", "逗号分隔的分组名，留空表示全部")
}

func (c *commonFlags) resolvedDSN() string {
	if c.dsn != "" {
		return c.dsn
	}
	return defaultDSN
}

func (c *commonFlags) groupList() []string {
	if strings.TrimSpace(c.groups) == "" {
		return nil
	}
	parts := strings.Split(c.groups, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	var c commonFlags
	bindCommon(fs, &c)
	if err := fs.Parse(args); err != nil {
		return err
	}

	groups := orm.Default.Groups()
	if len(groups) == 0 {
		fmt.Println("未注册任何实体。请确认 cmd/dbctl/main.go 中已 import 对应服务的 entity 包。")
		return nil
	}
	total := 0
	for _, g := range groups {
		es := orm.Default.Entities(g)
		fmt.Printf("[%s] %d 张表\n", g, len(es))
		for _, e := range es {
			fmt.Printf("    %s\n", e.TableName())
		}
		total += len(es)
	}
	fmt.Printf("\n共 %d 个分组，%d 张表\n", len(groups), total)
	return nil
}

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	var c commonFlags
	dryRun := fs.Bool("dry-run", false, "只打印将要操作的表，不实际执行")
	bindCommon(fs, &c)
	if err := fs.Parse(args); err != nil {
		return err
	}

	entities, err := orm.Default.Select(c.groupList()...)
	if err != nil {
		return err
	}
	if len(entities) == 0 {
		return errors.New("没有匹配的实体")
	}

	if *dryRun {
		fmt.Printf("将迁移 %d 张表:\n", len(entities))
		for _, e := range entities {
			fmt.Printf("    %s\n", e.TableName())
		}
		return nil
	}

	db, cleanup, err := open(c.resolvedDSN())
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	results, err := orm.NewMigrator(db).Migrate(ctx, entities)
	if err != nil {
		return err
	}

	created, altered := 0, 0
	for _, r := range results {
		switch {
		case r.Created:
			created++
			fmt.Printf("  + 新建 %s\n", r.Table)
		case len(r.AddedColumns) > 0:
			altered++
			fmt.Printf("  ~ 变更 %s: 新增列 %s\n", r.Table, strings.Join(r.AddedColumns, ", "))
		default:
			fmt.Printf("    无变化 %s\n", r.Table)
		}
	}
	fmt.Printf("\n完成: 共 %d 张表，新建 %d，变更 %d\n", len(results), created, altered)
	fmt.Println("提示: 执行 dbctl dump 更新 DDL 快照，使 schema 变更在 code review 中可见。")
	return nil
}

func cmdDump(args []string) error {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	var c commonFlags
	out := fs.String("out", "deploy/sql/schema.sql", "输出文件路径")
	bindCommon(fs, &c)
	if err := fs.Parse(args); err != nil {
		return err
	}

	entities, err := orm.Default.Select(c.groupList()...)
	if err != nil {
		return err
	}

	db, cleanup, err := open(c.resolvedDSN())
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	ddl, err := orm.NewMigrator(db).DumpDDL(ctx, entities)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(ddl), 0o644); err != nil {
		return fmt.Errorf("写入 %s: %w", *out, err)
	}
	fmt.Printf("已导出 %d 张表的 DDL 到 %s\n", len(entities), *out)
	return nil
}

func cmdDrop(args []string) error {
	fs := flag.NewFlagSet("drop", flag.ExitOnError)
	var c commonFlags
	yes := fs.Bool("yes", false, "确认执行删表")
	bindCommon(fs, &c)
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 双重闸门：环境必须是 dev，且必须显式 --yes。
	//
	// 为何两道都要：只判 --yes 挡不住在生产环境手滑；只判环境挡不住
	// 本地误操作正在使用的开发库。二者缺一不可。
	if env := os.Getenv("APP_ENV"); env != "dev" {
		return fmt.Errorf("drop 仅允许在 APP_ENV=dev 下执行，当前 APP_ENV=%q", env)
	}
	if !*yes {
		return errors.New("drop 需显式指定 --yes 以确认这是有意的破坏性操作")
	}

	entities, err := orm.Default.Select(c.groupList()...)
	if err != nil {
		return err
	}

	db, cleanup, err := open(c.resolvedDSN())
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := orm.NewMigrator(db).Drop(ctx, entities); err != nil {
		return err
	}
	fmt.Printf("已删除 %d 张表\n", len(entities))
	return nil
}

func open(dsn string) (*gorm.DB, func(), error) {
	db, cleanup, err := orm.Open(orm.Config{
		DSN: dsn,
		// 迁移是短时任务，不需要大连接池。
		MaxOpenConns: 4,
		MaxIdleConns: 2,
	})
	if err != nil {
		return nil, nil, err
	}
	return db, cleanup, nil
}
