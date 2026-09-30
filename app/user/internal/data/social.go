package data

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/fleamarket/app/user/entity"
	"github.com/fleamarket/app/user/internal/biz"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/paging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// socialRepo 关注与拉黑关系的 GORM 实现。
type socialRepo struct {
	data *Data
}

// NewSocialRepo 构造仓储。
func NewSocialRepo(data *Data) biz.SocialRepo { return &socialRepo{data: data} }

// Follow 关注。
//
// 用 ON CONFLICT DO NOTHING 而非先查后插：重复关注是正常的用户行为
// （连点两次按钮），不应因唯一键冲突而报错。这样也避免了查-插之间的竞态。
func (r *socialRepo) Follow(ctx context.Context, followerID, followeeID int64) error {
	row := &entity.FollowRelation{
		FollowerID: followerID,
		FolloweeID: followeeID,
		CreatedAt:  time.Now(),
	}
	err := r.data.DB.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
	return wrapDBError(err)
}

func (r *socialRepo) Unfollow(ctx context.Context, followerID, followeeID int64) error {
	err := r.data.DB.WithContext(ctx).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Delete(&entity.FollowRelation{}).Error
	return wrapDBError(err)
}

func (r *socialRepo) IsFollowing(ctx context.Context, followerID, followeeID int64) (bool, error) {
	var cnt int64
	err := r.data.DB.WithContext(ctx).Model(&entity.FollowRelation{}).
		Where("follower_id = ? AND followee_id = ?", followerID, followeeID).
		Count(&cnt).Error
	if err != nil {
		return false, wrapDBError(err)
	}
	return cnt > 0, nil
}

func (r *socialRepo) ListFollowing(ctx context.Context, followerID int64,
	cursor string, limit int) ([]int64, string, bool, error) {

	return r.listRelation(ctx, relationQuery{
		table:   &entity.FollowRelation{},
		where:   "follower_id = ?",
		arg:     followerID,
		peerCol: "followee_id",
		cursor:  cursor,
		limit:   limit,
	})
}

func (r *socialRepo) Block(ctx context.Context, userID, blockedID int64, reason uint8) error {
	row := &entity.BlockRelation{
		UserID:        userID,
		BlockedUserID: blockedID,
		Reason:        reason,
		CreatedAt:     time.Now(),
	}
	// 重复拉黑同样视为成功，但要更新原因——用户可能换了个理由重新提交。
	err := r.data.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "blocked_user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"reason"}),
	}).Create(row).Error
	return wrapDBError(err)
}

func (r *socialRepo) Unblock(ctx context.Context, userID, blockedID int64) error {
	err := r.data.DB.WithContext(ctx).
		Where("user_id = ? AND blocked_user_id = ?", userID, blockedID).
		Delete(&entity.BlockRelation{}).Error
	return wrapDBError(err)
}

// IsBlockedEitherWay 一次查询覆盖两个方向。
//
// 用 OR 条件而非两次查询：这个判断在每条 IM 消息发送前都会被调用，
// 是最高频的内部 RPC 之一，省掉一次往返是值得的。
func (r *socialRepo) IsBlockedEitherWay(ctx context.Context, userID, peerID int64) (bool, error) {
	var cnt int64
	err := r.data.DB.WithContext(ctx).Model(&entity.BlockRelation{}).
		Where("(user_id = ? AND blocked_user_id = ?) OR (user_id = ? AND blocked_user_id = ?)",
			userID, peerID, peerID, userID).
		Count(&cnt).Error
	if err != nil {
		return false, wrapDBError(err)
	}
	return cnt > 0, nil
}

func (r *socialRepo) ListBlocked(ctx context.Context, userID int64,
	cursor string, limit int) ([]int64, string, bool, error) {

	return r.listRelation(ctx, relationQuery{
		table:   &entity.BlockRelation{},
		where:   "user_id = ?",
		arg:     userID,
		peerCol: "blocked_user_id",
		cursor:  cursor,
		limit:   limit,
	})
}

// relationQuery 描述一次关系列表查询。
type relationQuery struct {
	table   any
	where   string
	arg     int64
	peerCol string
	cursor  string
	limit   int
}

// listRelation 关注与黑名单列表的公共实现。
//
// 游标以 created_at 为主排序键、对端 ID 为决胜键。
// 只用 created_at 是不够的：同一秒内批量产生的关系会出现漏项或重复。
func (r *socialRepo) listRelation(ctx context.Context, q relationQuery) ([]int64, string, bool, error) {
	limit := paging.NormalizeLimit(q.limit)

	cur, err := paging.DecodeCursor(q.cursor)
	if err != nil {
		return nil, "", false, errs.InvalidArgument("分页游标无效")
	}

	type row struct {
		Peer      int64     `gorm:"column:peer"`
		CreatedAt time.Time `gorm:"column:created_at"`
	}

	db := r.data.DB.WithContext(ctx).Model(q.table).
		Select(q.peerCol+" AS peer", "created_at").
		Where(q.where, q.arg)

	if !cur.IsZero() {
		ts, err := strconv.ParseInt(cur.SortValue, 10, 64)
		if err != nil {
			return nil, "", false, errs.InvalidArgument("分页游标无效")
		}
		at := time.UnixMilli(ts)
		// 倒序翻页的复合比较：时间更早，或时间相同但对端 ID 更小。
		db = db.Where("(created_at < ?) OR (created_at = ? AND "+q.peerCol+" < ?)",
			at, at, cur.ID)
	}

	var rows []row
	// 多查一条用于判断是否还有下一页，省去一次 COUNT。
	if err := db.Order("created_at DESC").Order(q.peerCol + " DESC").
		Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, "", false, wrapDBError(err)
	}

	page := paging.BuildPage(rows, limit, func(x row) paging.Cursor {
		return paging.Cursor{
			SortValue: strconv.FormatInt(x.CreatedAt.UnixMilli(), 10),
			ID:        x.Peer,
		}
	})

	ids := make([]int64, 0, len(page.Items))
	for _, x := range page.Items {
		ids = append(ids, x.Peer)
	}
	return ids, page.NextCursor, page.HasMore, nil
}

// creditRepo 信誉分仓储的 GORM 实现。
type creditRepo struct {
	data *Data
	ids  *idgen.Generator
}

// NewCreditRepo 构造仓储。
func NewCreditRepo(data *Data, ids *idgen.Generator) biz.CreditRepo {
	return &creditRepo{data: data, ids: ids}
}

// Adjust 在单个事务内写流水并更新用户表的冗余分值。
//
// 幂等与一致性的实现要点：
//  1. 先插流水。(reason_code, ref_id) 上有唯一索引，重复插入会冲突，
//     以此判定「已处理过」——比先查后插更可靠，无竞态窗口。
//  2. 用 SQL 表达式在数据库内做加减与边界裁剪，而非读出来改再写回。
//     后者在并发下会丢更新：两个事务都读到 100，各自算出 102 和 95，
//     最后一个写入覆盖前一个。
//  3. 流水的 after 字段在同一事务内回读，保证与用户表最终值一致。
func (r *creditRepo) Adjust(ctx context.Context, userID int64, delta int32,
	reason uint8, refID int64, remark string, minScore, maxScore int32) (int32, bool, error) {

	var (
		finalScore int32
		applied    bool
	)

	logID, err := r.ids.NextID()
	if err != nil {
		return 0, false, err
	}

	err = r.data.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		log := &entity.CreditLog{
			ID:         logID,
			UserID:     userID,
			Delta:      delta,
			ReasonCode: reason,
			RefID:      refID,
			Remark:     remark,
			CreatedAt:  time.Now(),
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(log)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 命中幂等，读出当前分值后直接返回。
			var u entity.User
			if err := tx.Select("credit_score").First(&u, "id = ?", userID).Error; err != nil {
				return err
			}
			finalScore = u.CreditScore
			applied = false
			return nil
		}

		// GREATEST/LEAST 在数据库内完成边界裁剪，避免读改写的丢更新。
		if err := tx.Model(&entity.User{}).Where("id = ?", userID).
			Update("credit_score",
				gorm.Expr("GREATEST(?, LEAST(?, credit_score + ?))", minScore, maxScore, delta)).
			Error; err != nil {
			return err
		}

		var u entity.User
		if err := tx.Select("credit_score").First(&u, "id = ?", userID).Error; err != nil {
			return err
		}
		finalScore = u.CreditScore
		applied = true

		// 回填流水的 after 字段，使流水可独立解释分值变化过程。
		return tx.Model(&entity.CreditLog{}).Where("id = ?", log.ID).
			Update("after", finalScore).Error
	})

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, false, errs.ErrNotFound
		}
		return 0, false, err
	}
	return finalScore, applied, nil
}
