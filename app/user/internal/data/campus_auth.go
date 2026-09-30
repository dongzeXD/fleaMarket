package data

import (
	"context"
	"strings"
	"time"

	"github.com/fleamarket/app/user/entity"
	"github.com/fleamarket/app/user/internal/biz"
)

// schoolRepo 学校与校区字典的 GORM 实现。
type schoolRepo struct {
	data *Data
}

// NewSchoolRepo 构造仓储。
func NewSchoolRepo(data *Data) biz.SchoolRepo { return &schoolRepo{data: data} }

func (r *schoolRepo) ListSchools(ctx context.Context, keyword string,
	page, pageSize int) ([]*biz.School, int64, error) {

	db := r.data.DB.WithContext(ctx).Model(&entity.School{}).Where("enabled = ?", true)
	if keyword != "" {
		// 前缀匹配而非全模糊：`LIKE '%xx%'` 无法用索引，
		// 学校字典虽小但这个接口在注册流程上，保持可索引更稳妥。
		db = db.Where("name LIKE ?", keyword+"%")
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, wrapDBError(err)
	}

	var rows []entity.School
	offset := (page - 1) * pageSize
	if err := db.Order("id ASC").Offset(offset).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, wrapDBError(err)
	}

	out := make([]*biz.School, 0, len(rows))
	for i := range rows {
		out = append(out, schoolToBiz(&rows[i]))
	}
	return out, total, nil
}

func (r *schoolRepo) GetSchool(ctx context.Context, id int64) (*biz.School, error) {
	var row entity.School
	if err := r.data.DB.WithContext(ctx).First(&row, "id = ? AND enabled = ?", id, true).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return schoolToBiz(&row), nil
}

func (r *schoolRepo) ListCampuses(ctx context.Context, schoolID int64) ([]*biz.Campus, error) {
	var rows []entity.Campus
	err := r.data.DB.WithContext(ctx).
		Where("school_id = ? AND enabled = ?", schoolID, true).
		Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, wrapDBError(err)
	}
	out := make([]*biz.Campus, 0, len(rows))
	for i := range rows {
		out = append(out, campusToBiz(&rows[i]))
	}
	return out, nil
}

func (r *schoolRepo) GetCampus(ctx context.Context, id int64) (*biz.Campus, error) {
	var row entity.Campus
	if err := r.data.DB.WithContext(ctx).First(&row, "id = ? AND enabled = ?", id, true).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return campusToBiz(&row), nil
}

func schoolToBiz(row *entity.School) *biz.School {
	return &biz.School{
		ID:           row.ID,
		Name:         row.Name,
		Province:     row.Province,
		EmailDomains: splitNonEmpty(row.EmailDomains),
	}
}

func campusToBiz(row *entity.Campus) *biz.Campus {
	return &biz.Campus{
		ID:       row.ID,
		SchoolID: row.SchoolID,
		Name:     row.Name,
		Lat:      row.Lat,
		Lng:      row.Lng,
	}
}

// campusAuthRepo 认证记录的 GORM 实现。
type campusAuthRepo struct {
	data *Data
}

// NewCampusAuthRepo 构造仓储。
func NewCampusAuthRepo(data *Data) biz.CampusAuthRepo { return &campusAuthRepo{data: data} }

func (r *campusAuthRepo) Create(ctx context.Context, a *biz.CampusAuth) (*biz.CampusAuth, error) {
	row := &entity.CampusAuth{
		ID:        a.ID,
		UserID:    a.UserID,
		SchoolID:  a.SchoolID,
		CampusID:  a.CampusID,
		Method:    a.Method,
		Status:    a.Status,
		StudentNo: a.StudentNo,
		RealName:  a.RealName,
		EduEmail:  a.EduEmail,
		ImageKeys: strings.Join(a.ImageKeys, ","),
		CreatedAt: time.Now(),
	}
	if err := r.data.DB.WithContext(ctx).Create(row).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return authToBiz(row), nil
}

// GetLatestByUser 取该用户最新的一条认证记录。
//
// 按 id 倒序而非 created_at：雪花 ID 本身单调递增，
// 用主键排序可避免在 created_at 上再建索引。
func (r *campusAuthRepo) GetLatestByUser(ctx context.Context, userID int64) (*biz.CampusAuth, error) {
	var row entity.CampusAuth
	err := r.data.DB.WithContext(ctx).
		Where("user_id = ?", userID).Order("id DESC").First(&row).Error
	if err != nil {
		return nil, wrapDBError(err)
	}
	return authToBiz(&row), nil
}

func (r *campusAuthRepo) GetByID(ctx context.Context, id int64) (*biz.CampusAuth, error) {
	var row entity.CampusAuth
	if err := r.data.DB.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return authToBiz(&row), nil
}

// MarkPassed 置为通过。
//
// 带 status = Pending 条件是乐观并发控制：若两个请求同时处理同一申请
// （人工审核与邮件链接并发），只有一个能成功，另一个影响 0 行。
// 由调用方据此判断是否重复处理。
func (r *campusAuthRepo) MarkPassed(ctx context.Context, id, reviewerID int64,
	expireAt *time.Time) error {

	now := time.Now()
	res := r.data.DB.WithContext(ctx).Model(&entity.CampusAuth{}).
		Where("id = ? AND status = ?", id, entity.CampusAuthStatusPending).
		Updates(map[string]any{
			"status":      entity.CampusAuthStatusPassed,
			"reviewer_id": reviewerID,
			"verified_at": &now,
			"expire_at":   expireAt,
		})
	if res.Error != nil {
		return wrapDBError(res.Error)
	}
	return nil
}

func (r *campusAuthRepo) MarkRejected(ctx context.Context, id, reviewerID int64, reason string) error {
	res := r.data.DB.WithContext(ctx).Model(&entity.CampusAuth{}).
		Where("id = ? AND status = ?", id, entity.CampusAuthStatusPending).
		Updates(map[string]any{
			"status":        entity.CampusAuthStatusRejected,
			"reviewer_id":   reviewerID,
			"reject_reason": reason,
		})
	return wrapDBError(res.Error)
}

func (r *campusAuthRepo) ExistsPassedStudentNo(ctx context.Context, schoolID int64,
	studentNo string, exceptUserID int64) (bool, error) {

	if studentNo == "" {
		return false, nil
	}
	var cnt int64
	err := r.data.DB.WithContext(ctx).Model(&entity.CampusAuth{}).
		Where("school_id = ? AND student_no = ? AND status = ? AND user_id <> ?",
			schoolID, studentNo, entity.CampusAuthStatusPassed, exceptUserID).
		Count(&cnt).Error
	if err != nil {
		return false, wrapDBError(err)
	}
	return cnt > 0, nil
}

// ListImagesToPurge 找出结论已产出超过保留期、且照片尚未清理的记录。
//
// 条件里的 image_keys <> ” 很关键：没有它，每次任务都会重新捞到
// 那些已清空 keys 但因删除对象失败而未标记的记录，造成无效重试。
func (r *campusAuthRepo) ListImagesToPurge(ctx context.Context,
	retention time.Duration, limit int) ([]*biz.CampusAuth, error) {

	cutoff := time.Now().Add(-retention)
	var rows []entity.CampusAuth
	err := r.data.DB.WithContext(ctx).
		Where("image_purged_at IS NULL AND image_keys <> ''").
		Where("status IN ?", []uint8{
			entity.CampusAuthStatusPassed, entity.CampusAuthStatusRejected,
		}).
		Where("updated_at < ?", cutoff).
		Order("id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, wrapDBError(err)
	}
	out := make([]*biz.CampusAuth, 0, len(rows))
	for i := range rows {
		out = append(out, authToBiz(&rows[i]))
	}
	return out, nil
}

func (r *campusAuthRepo) MarkImagePurged(ctx context.Context, id int64) error {
	now := time.Now()
	err := r.data.DB.WithContext(ctx).Model(&entity.CampusAuth{}).
		Where("id = ?", id).Updates(map[string]any{
		// 同时清空 keys：保留已删除对象的 key 没有意义，
		// 且留着会让人误以为图片还在。
		"image_keys":      "",
		"image_purged_at": &now,
	}).Error
	return wrapDBError(err)
}

func authToBiz(row *entity.CampusAuth) *biz.CampusAuth {
	return &biz.CampusAuth{
		ID:           row.ID,
		UserID:       row.UserID,
		SchoolID:     row.SchoolID,
		CampusID:     row.CampusID,
		Method:       row.Method,
		Status:       row.Status,
		StudentNo:    row.StudentNo,
		RealName:     row.RealName,
		EduEmail:     row.EduEmail,
		ImageKeys:    splitNonEmpty(row.ImageKeys),
		RejectReason: row.RejectReason,
		ReviewerID:   row.ReviewerID,
		VerifiedAt:   row.VerifiedAt,
		ExpireAt:     row.ExpireAt,
		CreatedAt:    row.CreatedAt,
	}
}

// splitNonEmpty 按逗号切分并剔除空项。
//
// 剔除空项是必要的：strings.Split("", ",") 返回 [""]，
// 若不处理，「没有图片」会变成「有一个空 key 的图片」，
// 后续 Stat 调用会拿空 key 去查对象存储。
func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// 编译期确认接口实现完整，避免接口变更后到运行时才发现漏实现。
var (
	_ biz.SchoolRepo     = (*schoolRepo)(nil)
	_ biz.CampusAuthRepo = (*campusAuthRepo)(nil)
)
