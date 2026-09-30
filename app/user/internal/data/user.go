package data

import (
	"context"
	"errors"

	"github.com/fleamarket/app/user/entity"
	"github.com/fleamarket/app/user/internal/biz"

	"github.com/fleamarket/pkg/crypto"
	"github.com/fleamarket/pkg/errs"
	"gorm.io/gorm"
)

// userRepo 用户仓储的 GORM 实现。
type userRepo struct {
	data *Data
	// sec 负责手机号的加密与摘要。
	//
	// 加解密留在 data 层而非 biz：biz 处理的是明文手机号这一领域概念，
	// 「怎么存」是持久化细节。这样换加密方案不会波及业务逻辑。
	sec *crypto.SensitiveField
}

// NewUserRepo 构造仓储。
func NewUserRepo(data *Data, sec *crypto.SensitiveField) biz.UserRepo {
	return &userRepo{data: data, sec: sec}
}

func (r *userRepo) Create(ctx context.Context, u *biz.User) (*biz.User, error) {
	row, err := r.toEntity(u)
	if err != nil {
		return nil, err
	}
	if err := r.data.DB.WithContext(ctx).Create(row).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return r.toBiz(row)
}

func (r *userRepo) GetByID(ctx context.Context, id int64) (*biz.User, error) {
	var row entity.User
	if err := r.data.DB.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return r.toBiz(&row)
}

// GetByPhone 按手机号查询。
//
// 检索走 phone_hash 而非密文：AES-GCM 的随机 nonce 使同一手机号
// 两次加密结果不同，密文无法用于等值查询（见 pkg/crypto 的说明）。
func (r *userRepo) GetByPhone(ctx context.Context, phone string) (*biz.User, error) {
	hash := r.sec.Hash(phone)
	if hash == "" {
		return nil, errs.ErrNotFound
	}
	var row entity.User
	if err := r.data.DB.WithContext(ctx).First(&row, "phone_hash = ?", hash).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return r.toBiz(&row)
}

func (r *userRepo) GetByWxUnionID(ctx context.Context, unionID string) (*biz.User, error) {
	if unionID == "" {
		return nil, errs.ErrNotFound
	}
	var row entity.User
	if err := r.data.DB.WithContext(ctx).First(&row, "wx_union_id = ?", unionID).Error; err != nil {
		return nil, wrapDBError(err)
	}
	return r.toBiz(&row)
}

func (r *userRepo) BatchGetBrief(ctx context.Context, ids []int64) (map[int64]*biz.User, error) {
	if len(ids) == 0 {
		return map[int64]*biz.User{}, nil
	}
	var rows []entity.User
	// 只选摘要所需的列：批量查询常出现在列表页，
	// 把 phone_cipher 这类宽列一并拉出来是纯浪费。
	err := r.data.DB.WithContext(ctx).
		Select("id", "nickname", "avatar_key", "status", "credit_score",
			"campus_auth_status", "school_id", "campus_id", "created_at").
		Where("id IN ?", ids).Find(&rows).Error
	if err != nil {
		return nil, wrapDBError(err)
	}

	out := make(map[int64]*biz.User, len(rows))
	for i := range rows {
		// 摘要场景不需要手机号，跳过解密以省去无谓的 CPU 开销。
		out[rows[i].ID] = briefToBiz(&rows[i])
	}
	return out, nil
}

func (r *userRepo) UpdateProfile(ctx context.Context, id int64, nickname, avatarKey string) error {
	updates := map[string]any{}
	if nickname != "" {
		updates["nickname"] = nickname
	}
	// 头像允许被显式清空，故不判空——用 avatarKey 原值覆盖。
	updates["avatar_key"] = avatarKey
	if len(updates) == 0 {
		return nil
	}
	res := r.data.DB.WithContext(ctx).Model(&entity.User{}).
		Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return wrapDBError(res.Error)
	}
	if res.RowsAffected == 0 {
		// 影响 0 行既可能是用户不存在，也可能是值未变化。
		// 为区分，补一次存在性检查——否则会把「重复提交相同昵称」误报为用户不存在。
		var cnt int64
		if err := r.data.DB.WithContext(ctx).Model(&entity.User{}).
			Where("id = ?", id).Count(&cnt).Error; err != nil {
			return wrapDBError(err)
		}
		if cnt == 0 {
			return errs.ErrNotFound
		}
	}
	return nil
}

func (r *userRepo) UpdateWechatBinding(ctx context.Context, id int64,
	unionID, openIDWeb, openIDMini string) error {

	updates := map[string]any{"wx_union_id": unionID}
	if openIDWeb != "" {
		updates["wx_open_id_web"] = openIDWeb
	}
	if openIDMini != "" {
		updates["wx_open_id_mini"] = openIDMini
	}
	if err := r.data.DB.WithContext(ctx).Model(&entity.User{}).
		Where("id = ?", id).Updates(updates).Error; err != nil {
		return wrapDBError(err)
	}
	return nil
}

func (r *userRepo) TouchLogin(ctx context.Context, id int64) error {
	err := r.data.DB.WithContext(ctx).Model(&entity.User{}).
		Where("id = ?", id).Update("last_login_at", gorm.Expr("NOW()")).Error
	return wrapDBError(err)
}

func (r *userRepo) UpdateCampusSnapshot(ctx context.Context, id int64,
	status uint8, schoolID, campusID int64) error {

	err := r.data.DB.WithContext(ctx).Model(&entity.User{}).
		Where("id = ?", id).Updates(map[string]any{
		"campus_auth_status": status,
		"school_id":          schoolID,
		"campus_id":          campusID,
	}).Error
	return wrapDBError(err)
}

func (r *userRepo) CountsOf(ctx context.Context, id int64) (int32, int32, error) {
	var following, follower int64
	db := r.data.DB.WithContext(ctx)
	if err := db.Model(&entity.FollowRelation{}).
		Where("follower_id = ?", id).Count(&following).Error; err != nil {
		return 0, 0, wrapDBError(err)
	}
	if err := db.Model(&entity.FollowRelation{}).
		Where("followee_id = ?", id).Count(&follower).Error; err != nil {
		return 0, 0, wrapDBError(err)
	}
	return int32(following), int32(follower), nil
}

// --- 领域对象与表实体的互转 ---

func (r *userRepo) toEntity(u *biz.User) (*entity.User, error) {
	row := &entity.User{
		ID:               u.ID,
		Nickname:         u.Nickname,
		AvatarKey:        u.AvatarKey,
		Status:           u.Status,
		WxUnionID:        u.WxUnionID,
		WxOpenIDWeb:      u.WxOpenIDWeb,
		WxOpenIDMini:     u.WxOpenIDMini,
		CreditScore:      u.CreditScore,
		CampusAuthStatus: u.CampusAuthStatus,
		SchoolID:         u.SchoolID,
		CampusID:         u.CampusID,
	}
	if u.Phone != "" {
		cipher, err := r.sec.Encrypt(u.Phone)
		if err != nil {
			return nil, errs.Internal("手机号加密失败")
		}
		row.PhoneCipher = cipher
		row.PhoneHash = r.sec.Hash(u.Phone)
	}
	return row, nil
}

func (r *userRepo) toBiz(row *entity.User) (*biz.User, error) {
	u := briefToBiz(row)
	u.WxUnionID = row.WxUnionID
	u.WxOpenIDWeb = row.WxOpenIDWeb
	u.WxOpenIDMini = row.WxOpenIDMini
	u.LastLoginAt = row.LastLoginAt

	if row.PhoneCipher != "" {
		phone, err := r.sec.Decrypt(row.PhoneCipher)
		if err != nil {
			// 解密失败通常意味着密钥被轮换却未迁移数据。
			// 这是运维事故，必须暴露而非静默返回空手机号——
			// 静默会让「用户手机号不见了」表现为一个难以定位的业务 bug。
			return nil, errs.Internal("手机号解密失败，请检查加密密钥配置")
		}
		u.Phone = phone
	}
	return u, nil
}

// briefToBiz 只转换摘要字段，不涉及解密。
func briefToBiz(row *entity.User) *biz.User {
	return &biz.User{
		ID:               row.ID,
		Nickname:         row.Nickname,
		AvatarKey:        row.AvatarKey,
		Status:           row.Status,
		CreditScore:      row.CreditScore,
		CampusAuthStatus: row.CampusAuthStatus,
		SchoolID:         row.SchoolID,
		CampusID:         row.CampusID,
		CreatedAt:        row.CreatedAt,
	}
}

// wrapDBError 把 GORM 的错误转成 biz 层可识别的哨兵错误。
//
// 转换发生在 data 层是刻意的：biz 不该认识 gorm.ErrRecordNotFound，
// 否则换 ORM 就要改业务代码。
func wrapDBError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errs.ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return errs.ErrAlreadyExists
	default:
		return err
	}
}
