// Package entity 定义本服务的数据库表实体。
//
// 为何放在 internal 之外（见 doc.md §7.1）：cmd/dbctl 需要 import 各服务的
// 实体才能做统一建表，而 internal 会阻止跨模块导入。
//
// 因此这里只允许出现「纯数据结构」——字段、GORM tag、TableName()。
// Repo 实现、领域逻辑、service 一律留在 internal 下，其他服务即使
// import 了本包也拿不到任何可执行的业务能力，不构成边界泄漏。
package entity

import "github.com/fleamarket/pkg/orm"

// Group 本服务的实体分组名，供 dbctl 的 --group 参数使用。
const Group = "product"

// init 向全局注册中心登记本服务的全部表。
//
// 声明顺序即建表顺序：字典表（分类、POI）在前，引用它们的业务表在后。
func init() {
	orm.Register(Group,
		Category{},
		CampusPOI{},
		Product{},
		ProductImage{},
		PriceHistory{},
		Favorite{},
		WantedPost{},
	)
}
