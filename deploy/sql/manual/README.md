# 人工审核的 SQL 脚本

## 为什么需要这个目录

`dbctl migrate` 基于 GORM AutoMigrate，语义被刻意限制为**只增不减**：建表、加列、加索引。
它不会删列、不会把字段类型改窄、不会重命名。

这不是工具的缺陷，而是有意的边界（见 `doc.md` §7.2）。
删列与改类型是**有损操作**：一旦执行，数据无法通过回滚代码恢复。
这类变更必须经过人工审查、在明确的发布窗口执行、并提前准备好数据备份，
不能由一条随手敲下的 `make migrate` 完成。

## 什么变更要走这里

- 删除列或表
- 收窄字段类型（`varchar(255)` → `varchar(64)`、`bigint` → `int`）
- 重命名列或表
- 修改主键、唯一约束
- 需要数据回填或转换的变更
- 大表加索引（需评估锁表时间，考虑 `pt-online-schema-change` 之类的在线工具）

## 命名规范

```
NNN_简短描述.sql
NNN_简短描述.rollback.sql   # 回滚脚本，必须同时提供
```

`NNN` 为三位递增序号，如 `001_drop_user_legacy_avatar.sql`。

## 每个脚本必须包含的内容

```sql
-- 变更编号: 001
-- 目的: 删除 t_user.legacy_avatar 列（已由 avatar_key 取代）
-- 影响表: t_user（约 12 万行）
-- 预估耗时: < 1s
-- 是否需要停机: 否
-- 前置条件: 应用代码已发布至不再读写该列的版本
-- 备份: 执行前已 dump t_user 至 backup/20260930_t_user.sql

ALTER TABLE t_user DROP COLUMN legacy_avatar;
```

## 执行流程

1. 提 MR，脚本与回滚脚本一并提交，至少一名他人 review。
2. 在开发库验证脚本与回滚脚本均可正常执行。
3. 生产执行前备份受影响的表。
4. 执行后运行 `make db-dump` 更新 `deploy/sql/schema.sql`，使快照与真实结构一致。
5. 同步更新对应服务的 `entity` 定义，否则下次 `dbctl migrate` 会把删掉的列加回来。

> 第 5 步容易被遗漏，是这套机制最主要的失效场景：
> 实体里还留着字段，而数据库里已经删了，`migrate` 会静默地把它重建。
> `schema.sql` 快照进 git 的意义就在于让这种不一致在 diff 中暴露。
