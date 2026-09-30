package service

import (
	"github.com/fleamarket/app/product/internal/biz"

	commonv1 "github.com/fleamarket/api/common/v1"
	productv1 "github.com/fleamarket/api/product/v1"
	"github.com/fleamarket/pkg/paging"
)

// 本文件集中放置 proto ↔ 领域对象的转换，避免在四个 service 文件里重复。

func imageRef(key string) *commonv1.ImageRef {
	if key == "" {
		return nil
	}
	// URL 留空：拼 CDN 域名与图片处理参数是网关的职责，
	// 各业务服务不应各自持有一份 CDN 配置。
	return &commonv1.ImageRef{ObjectKey: key}
}

func imageRefs(images []biz.Image) []*commonv1.ImageRef {
	out := make([]*commonv1.ImageRef, 0, len(images))
	for _, img := range images {
		out = append(out, &commonv1.ImageRef{
			ObjectKey: img.ObjectKey,
			Width:     img.Width,
			Height:    img.Height,
		})
	}
	return out
}

func campusRef(schoolID, campusID int64) *commonv1.CampusRef {
	if campusID == 0 {
		return nil
	}
	// 只回填 ID，名称由网关按字典批量补全，避免每个接口都联表查字典。
	return &commonv1.CampusRef{
		CampusId: campusID,
		School:   &commonv1.SchoolRef{SchoolId: schoolID},
	}
}

func statusToProto(s uint8) productv1.ProductStatus {
	switch s {
	case biz.StatusPendingReview:
		return productv1.ProductStatus_PRODUCT_STATUS_PENDING_REVIEW
	case biz.StatusOnSale:
		return productv1.ProductStatus_PRODUCT_STATUS_ON_SALE
	case biz.StatusReserved:
		return productv1.ProductStatus_PRODUCT_STATUS_RESERVED
	case biz.StatusSold:
		return productv1.ProductStatus_PRODUCT_STATUS_SOLD
	case biz.StatusOffline:
		return productv1.ProductStatus_PRODUCT_STATUS_OFFLINE
	case biz.StatusRejected:
		return productv1.ProductStatus_PRODUCT_STATUS_REJECTED
	default:
		return productv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}

func statusFromProto(s productv1.ProductStatus) (uint8, bool) {
	switch s {
	case productv1.ProductStatus_PRODUCT_STATUS_PENDING_REVIEW:
		return biz.StatusPendingReview, true
	case productv1.ProductStatus_PRODUCT_STATUS_ON_SALE:
		return biz.StatusOnSale, true
	case productv1.ProductStatus_PRODUCT_STATUS_RESERVED:
		return biz.StatusReserved, true
	case productv1.ProductStatus_PRODUCT_STATUS_SOLD:
		return biz.StatusSold, true
	case productv1.ProductStatus_PRODUCT_STATUS_OFFLINE:
		return biz.StatusOffline, true
	case productv1.ProductStatus_PRODUCT_STATUS_REJECTED:
		return biz.StatusRejected, true
	default:
		return 0, false
	}
}

func conditionToProto(c uint8) productv1.Condition {
	switch c {
	case biz.ConditionNew:
		return productv1.Condition_CONDITION_NEW
	case biz.ConditionLikeNew:
		return productv1.Condition_CONDITION_LIKE_NEW
	case biz.ConditionLightlyUsed:
		return productv1.Condition_CONDITION_LIGHTLY_USED
	case biz.ConditionWellUsed:
		return productv1.Condition_CONDITION_WELL_USED
	default:
		return productv1.Condition_CONDITION_UNSPECIFIED
	}
}

func conditionFromProto(c productv1.Condition) uint8 {
	switch c {
	case productv1.Condition_CONDITION_NEW:
		return biz.ConditionNew
	case productv1.Condition_CONDITION_LIKE_NEW:
		return biz.ConditionLikeNew
	case productv1.Condition_CONDITION_LIGHTLY_USED:
		return biz.ConditionLightlyUsed
	case productv1.Condition_CONDITION_WELL_USED:
		return biz.ConditionWellUsed
	default:
		return 0
	}
}

func wantedStatusToProto(s uint8) productv1.WantedStatus {
	switch s {
	case biz.WantedOpen:
		return productv1.WantedStatus_WANTED_STATUS_OPEN
	case biz.WantedClosed:
		return productv1.WantedStatus_WANTED_STATUS_CLOSED
	case biz.WantedExpired:
		return productv1.WantedStatus_WANTED_STATUS_EXPIRED
	default:
		return productv1.WantedStatus_WANTED_STATUS_UNSPECIFIED
	}
}

func poiTypeToProto(t uint8) productv1.POIType {
	switch t {
	case 1:
		return productv1.POIType_POI_TYPE_GATE
	case 2:
		return productv1.POIType_POI_TYPE_DORM
	case 3:
		return productv1.POIType_POI_TYPE_CANTEEN
	case 4:
		return productv1.POIType_POI_TYPE_LIBRARY
	case 5:
		return productv1.POIType_POI_TYPE_TEACHING_BUILDING
	case 6:
		return productv1.POIType_POI_TYPE_PARCEL_STATION
	case 99:
		return productv1.POIType_POI_TYPE_OTHER
	default:
		return productv1.POIType_POI_TYPE_UNSPECIFIED
	}
}

func poiTypesFromProto(ts []productv1.POIType) []uint8 {
	out := make([]uint8, 0, len(ts))
	for _, t := range ts {
		switch t {
		case productv1.POIType_POI_TYPE_GATE:
			out = append(out, 1)
		case productv1.POIType_POI_TYPE_DORM:
			out = append(out, 2)
		case productv1.POIType_POI_TYPE_CANTEEN:
			out = append(out, 3)
		case productv1.POIType_POI_TYPE_LIBRARY:
			out = append(out, 4)
		case productv1.POIType_POI_TYPE_TEACHING_BUILDING:
			out = append(out, 5)
		case productv1.POIType_POI_TYPE_PARCEL_STATION:
			out = append(out, 6)
		case productv1.POIType_POI_TYPE_OTHER:
			out = append(out, 99)
		}
	}
	return out
}

// productToProto 转换商品详情。
//
// favorited 由调用方查询后传入而非在此处查：转换函数保持无 I/O，
// 否则一个列表转换会隐式产生 N 次数据库访问。
func productToProto(p *biz.Product, favorited bool) *productv1.Product {
	if p == nil {
		return nil
	}
	out := &productv1.Product{
		ProductId:          p.ID,
		SellerId:           p.SellerID,
		Title:              p.Title,
		Description:        p.Description,
		PriceCents:         p.PriceCents,
		OriginalPriceCents: p.OriginalPriceCents,
		Condition:          conditionToProto(p.Condition),
		Status:             statusToProto(p.Status),
		RejectReason:       p.RejectReason,
		Campus:             campusRef(p.SchoolID, p.CampusID),
		Images:             imageRefs(p.Images),
		ViewCount:          p.ViewCount,
		FavoriteCount:      p.FavoriteCount,
		Favorited:          favorited,
		HeatScore:          p.HeatScore,
		CreatedAt:          unixOrZero(p.CreatedAt.Unix()),
		UpdatedAt:          unixOrZero(p.UpdatedAt.Unix()),
	}
	// 分类与 POI 只回填 ID，名称由网关补全。
	if p.CategoryID != 0 {
		out.Category = &productv1.Category{CategoryId: p.CategoryID}
	}
	if p.POIID != nil && *p.POIID != 0 {
		out.Poi = &productv1.POI{PoiId: *p.POIID, CampusId: p.CampusID}
	}
	return out
}

func productBriefToProto(p *biz.Product, favorited bool) *productv1.ProductBrief {
	if p == nil {
		return nil
	}
	return &productv1.ProductBrief{
		ProductId:          p.ID,
		SellerId:           p.SellerID,
		Title:              p.Title,
		PriceCents:         p.PriceCents,
		OriginalPriceCents: p.OriginalPriceCents,
		Condition:          conditionToProto(p.Condition),
		Status:             statusToProto(p.Status),
		Cover:              imageRef(p.CoverKey),
		Campus:             campusRef(p.SchoolID, p.CampusID),
		FavoriteCount:      p.FavoriteCount,
		Favorited:          favorited,
		CreatedAt:          unixOrZero(p.CreatedAt.Unix()),
	}
}

func productBriefsToProto(ps []*biz.Product, marks map[int64]bool) []*productv1.ProductBrief {
	out := make([]*productv1.ProductBrief, 0, len(ps))
	for _, p := range ps {
		out = append(out, productBriefToProto(p, marks[p.ID]))
	}
	return out
}

// indexDocToProto 转换索引文档。
//
// 与 ProductBrief 不同，这里必须带上 description（全文匹配需要）
// 与 deleted 标记（索引需要知道哪些文档该移除）。
func indexDocToProto(d *biz.IndexDoc) *productv1.IndexDocument {
	if d == nil || d.Product == nil {
		return nil
	}
	p := d.Product
	return &productv1.IndexDocument{
		ProductId:        p.ID,
		SellerId:         p.SellerID,
		Title:            p.Title,
		Description:      p.Description,
		PriceCents:       p.PriceCents,
		Condition:        conditionToProto(p.Condition),
		Status:           statusToProto(p.Status),
		CategoryId:       p.CategoryID,
		ParentCategoryId: d.ParentCategoryID,
		SchoolId:         p.SchoolID,
		CampusId:         p.CampusID,
		CoverKey:         p.CoverKey,
		HeatScore:        p.HeatScore,
		FavoriteCount:    p.FavoriteCount,
		CreatedAt:        unixOrZero(p.CreatedAt.Unix()),
		Deleted:          d.Deleted,
	}
}

func categoryToProto(c *biz.Category) *productv1.Category {
	if c == nil {
		return nil
	}
	out := &productv1.Category{
		CategoryId: c.ID,
		ParentId:   c.ParentID,
		Name:       c.Name,
		IconKey:    c.IconKey,
		Sort:       c.Sort,
		Enabled:    c.Enabled,
	}
	for _, child := range c.Children {
		out.Children = append(out.Children, categoryToProto(child))
	}
	return out
}

func poiToProto(p *biz.POI) *productv1.POI {
	if p == nil {
		return nil
	}
	out := &productv1.POI{
		PoiId:       p.ID,
		CampusId:    p.CampusID,
		Name:        p.Name,
		Type:        poiTypeToProto(p.Type),
		Recommended: p.Recommended,
	}
	// 坐标为 0 表示未录入，此时不下发 location，
	// 避免客户端把 (0,0) 当真实坐标显示在几内亚湾。
	if p.Lat != 0 || p.Lng != 0 {
		out.Location = &commonv1.GeoPoint{Lat: p.Lat, Lng: p.Lng}
	}
	return out
}

func wantedToProto(w *biz.WantedPost) *productv1.WantedPost {
	if w == nil {
		return nil
	}
	out := &productv1.WantedPost{
		WantedId:       w.ID,
		UserId:         w.UserID,
		Title:          w.Title,
		Description:    w.Description,
		BudgetMaxCents: w.BudgetMaxCents,
		Campus:         campusRef(w.SchoolID, w.CampusID),
		Status:         wantedStatusToProto(w.Status),
		ViewCount:      w.ViewCount,
		CreatedAt:      unixOrZero(w.CreatedAt.Unix()),
		ExpireAt:       unixOrZero(w.ExpireAt.Unix()),
	}
	if w.CategoryID != 0 {
		out.Category = &productv1.Category{CategoryId: w.CategoryID}
	}
	return out
}

func wantedsToProto(ws []*biz.WantedPost) []*productv1.WantedPost {
	out := make([]*productv1.WantedPost, 0, len(ws))
	for _, w := range ws {
		out = append(out, wantedToProto(w))
	}
	return out
}

func cursorRequest(r *commonv1.CursorRequest) (string, int) {
	if r == nil {
		return "", paging.DefaultLimit
	}
	return r.GetCursor(), int(r.GetLimit())
}

func cursorReplyOf(next string, hasMore bool) *commonv1.CursorReply {
	return &commonv1.CursorReply{NextCursor: next, HasMore: hasMore}
}

// unixOrZero 把零值时间戳归一为 0。
//
// time.Time 零值的 Unix() 是 -6795364578，直接下发会让客户端
// 显示 1754 年这类荒谬日期。
func unixOrZero(sec int64) int64 {
	if sec <= 0 {
		return 0
	}
	return sec
}
