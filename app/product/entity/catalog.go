package entity

import (
	"time"

	"gorm.io/gorm"
)

// POI 类型。与 api/product/v1 的 POIType 枚举同值。
const (
	POITypeGate             uint8 = 1
	POITypeDorm             uint8 = 2
	POITypeCanteen          uint8 = 3
	POITypeLibrary          uint8 = 4
	POITypeTeachingBuilding uint8 = 5
	POITypeParcelStation    uint8 = 6
	POITypeOther            uint8 = 99
)

// 求购帖状态。
const (
	WantedStatusOpen    uint8 = 1
	WantedStatusClosed  uint8 = 2
	WantedStatusExpired uint8 = 3
)

// Category 商品分类。两级结构，一级的 ParentID 为 0。
type Category struct {
	ID        int64  `gorm:"primaryKey;autoIncrement:false"`
	ParentID  int64  `gorm:"not null;default:0;index:idx_parent_sort,priority:1;comment:父分类,0为一级"`
	Name      string `gorm:"type:varchar(32);not null"`
	IconKey   string `gorm:"type:varchar(255);comment:图标对象key"`
	Sort      int32  `gorm:"not null;default:0;index:idx_parent_sort,priority:2"`
	Enabled   bool   `gorm:"not null;default:true"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Category) TableName() string { return "t_category" }

// Favorite 收藏关系。
//
// 联合主键而非自增 ID：唯一性就是 (user_id, product_id)，
// 额外主键不提供任何查询价值。
//
// idx_product_created 支撑「查收藏了某商品的用户」——
// notify-service 扇出降价提醒时按此分页拉取。
type Favorite struct {
	UserID    int64     `gorm:"primaryKey;comment:用户ID"`
	ProductID int64     `gorm:"primaryKey;index:idx_product_created,priority:1;comment:商品ID"`
	CreatedAt time.Time `gorm:"index:idx_product_created,priority:2"`
}

func (Favorite) TableName() string { return "t_favorite" }

// CampusPOI 校园内的面交地点。
type CampusPOI struct {
	ID       int64   `gorm:"primaryKey;autoIncrement:false"`
	CampusID int64   `gorm:"not null;index:idx_campus_type,priority:1;comment:所属校区"`
	Name     string  `gorm:"type:varchar(64);not null"`
	Type     uint8   `gorm:"not null;index:idx_campus_type,priority:2;comment:见 POIType*"`
	Lat      float64 `gorm:"type:decimal(10,7)"`
	Lng      float64 `gorm:"type:decimal(10,7)"`
	// Recommended 推荐地点通常人流量大、有监控，面交安全性更高。
	Recommended bool `gorm:"not null;default:false;comment:是否推荐面交点"`
	Enabled     bool `gorm:"not null;default:true"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (CampusPOI) TableName() string { return "t_campus_poi" }

// WantedPost 求购帖。
type WantedPost struct {
	ID     int64 `gorm:"primaryKey;autoIncrement:false"`
	UserID int64 `gorm:"not null;index:idx_user_status,priority:1;comment:发帖人"`

	Title       string `gorm:"type:varchar(60);not null"`
	Description string `gorm:"type:varchar(1000)"`
	// BudgetMaxCents 心理价位上限。0 表示价格可议。
	BudgetMaxCents int64 `gorm:"not null;default:0;comment:预算上限(分)"`

	CategoryID int64 `gorm:"not null;index:idx_campus_category,priority:2;comment:分类ID"`
	SchoolID   int64 `gorm:"not null"`
	CampusID   int64 `gorm:"not null;index:idx_campus_category,priority:1;comment:校区ID"`

	Status uint8 `gorm:"not null;default:1;index:idx_user_status,priority:2;comment:见 WantedStatus*"`

	ViewCount int64 `gorm:"not null;default:0"`

	// ExpireAt 过期时间。求购是有时效的需求，过期后自动收起，
	// 避免半年前的求购帖长期占据列表顶部。
	ExpireAt time.Time `gorm:"not null;index:idx_expire_at;comment:过期时间"`

	CreatedAt time.Time `gorm:"index"`
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (WantedPost) TableName() string { return "t_wanted_post" }
