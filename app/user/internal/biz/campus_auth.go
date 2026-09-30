package biz

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fleamarket/pkg/errs"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/storage"
)

const (
	// emailAuthTTL 邮箱认证链接有效期。
	emailAuthTTL = 30 * time.Minute
	// authImageRetention 学生证照片保留期。
	//
	// 隐私合规要求（doc.md §13）：认证结论产出后 30 天删除原图，
	// 仅保留认证结果。学生证含姓名、学号、照片，属敏感个人信息，
	// 长期留存的风险远大于其在争议复核中的价值。
	authImageRetention = 30 * 24 * time.Hour
)

// CampusAuthUsecase 校园身份认证用例。
type CampusAuthUsecase struct {
	auths      CampusAuthRepo
	schools    SchoolRepo
	users      UserRepo
	tokens     EmailAuthTokenStore
	mailer     EmailSender
	files      storage.Storage
	ids        *idgen.Generator
	events     EventPublisher
	verifyBase string
	log        *slog.Logger
}

// CampusAuthConfig 认证用例的配置项。
//
// 用结构体而非裸 string 参数：wire 无法区分两个同为 string 的依赖，
// 而具名类型既能让注入无歧义，又不需要 biz 反向依赖 conf 包。
type CampusAuthConfig struct {
	// VerifyBaseURL 认证回调链接前缀，如 "https://flea.example.com/campus-auth/verify"。
	// 随部署域名变化，故由配置注入而非硬编码。
	VerifyBaseURL string
}

// NewCampusAuthUsecase 构造用例。
func NewCampusAuthUsecase(auths CampusAuthRepo, schools SchoolRepo, users UserRepo,
	tokens EmailAuthTokenStore, mailer EmailSender, files storage.Storage,
	ids *idgen.Generator, events EventPublisher, cfg CampusAuthConfig,
	logger *slog.Logger) *CampusAuthUsecase {

	return &CampusAuthUsecase{
		auths: auths, schools: schools, users: users, tokens: tokens,
		mailer: mailer, files: files, ids: ids, events: events,
		verifyBase: strings.TrimRight(cfg.VerifyBaseURL, "/"),
		log:        logger.With(slog.String("usecase", "campus_auth")),
	}
}

// ListSchools 查询学校字典。
func (uc *CampusAuthUsecase) ListSchools(ctx context.Context, keyword string,
	page, pageSize int) ([]*School, int64, error) {

	p, ps, _ := paginate(page, pageSize)
	schools, total, err := uc.schools.ListSchools(ctx, keyword, p, ps)
	if err != nil {
		return nil, 0, errs.FromDataError(err, "")
	}
	return schools, total, nil
}

// ListCampuses 查询某校的校区。
func (uc *CampusAuthUsecase) ListCampuses(ctx context.Context, schoolID int64) ([]*Campus, error) {
	if _, err := uc.schools.GetSchool(ctx, schoolID); err != nil {
		return nil, errs.FromDataError(err, "学校不存在")
	}
	list, err := uc.schools.ListCampuses(ctx, schoolID)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	return list, nil
}

// GetMyAuth 查询本人最新的认证记录。
func (uc *CampusAuthUsecase) GetMyAuth(ctx context.Context, userID int64) (*CampusAuth, error) {
	a, err := uc.auths.GetLatestByUser(ctx, userID)
	if errors.Is(err, errs.ErrNotFound) {
		// 从未提交过不是错误，返回一个 None 状态的占位记录，
		// 让客户端只需处理一种数据形态。
		return &CampusAuth{UserID: userID, Status: CampusAuthNone}, nil
	}
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	// 已过期的通过记录在读取时纠正状态，避免依赖定时任务的及时性。
	if a.Status == CampusAuthPassed && a.ExpireAt != nil && a.ExpireAt.Before(time.Now()) {
		a.Status = CampusAuthExpired
	}
	return a, nil
}

// StartEmailAuthResult 发起邮箱认证的结果。
type StartEmailAuthResult struct {
	Email    string
	ExpireAt time.Time
}

// StartEmailAuth 发起校园邮箱认证。
//
// 校验链路：学校存在 → 校区属于该校 → 邮箱域名命中该校白名单 →
// 生成一次性令牌 → 发信。
//
// 域名白名单是这条路径能自动通过审核的唯一依据，因此匹配必须严格
// （见 School.AllowsEmail 对子域名边界的处理）。
func (uc *CampusAuthUsecase) StartEmailAuth(ctx context.Context, userID int64,
	schoolID, campusID int64, email string) (*StartEmailAuthResult, error) {

	school, err := uc.schools.GetSchool(ctx, schoolID)
	if err != nil {
		return nil, errs.FromDataError(err, "学校不存在")
	}
	campus, err := uc.schools.GetCampus(ctx, campusID)
	if err != nil {
		return nil, errs.FromDataError(err, "校区不存在")
	}
	if campus.SchoolID != schoolID {
		return nil, errs.InvalidArgument("校区不属于所选学校")
	}
	if !school.AllowsEmail(email) {
		return nil, errs.WithMeta(
			errs.InvalidArgument("该邮箱不是所选学校的校园邮箱"),
			map[string]string{"allowed_domains": strings.Join(school.EmailDomains, ",")},
		)
	}

	// 已通过且未过期时不再重复认证。
	if cur, err := uc.GetMyAuth(ctx, userID); err == nil && cur.Status == CampusAuthPassed {
		return nil, errs.Conflict("已完成校园认证")
	}

	id, err := uc.ids.NextID()
	if err != nil {
		return nil, errs.Internal("ID 生成失败")
	}
	record := &CampusAuth{
		ID:       id,
		UserID:   userID,
		SchoolID: schoolID,
		CampusID: campusID,
		Method:   AuthMethodEduEmail,
		// 邮箱认证在点击链接后才通过，此前是 Pending。
		Status:   CampusAuthPending,
		EduEmail: email,
	}
	if _, err := uc.auths.Create(ctx, record); err != nil {
		return nil, errs.FromDataError(err, "")
	}

	token, err := randomToken(32)
	if err != nil {
		return nil, errs.Internal("令牌生成失败")
	}
	if err := uc.tokens.Save(ctx, token, id, emailAuthTTL); err != nil {
		return nil, errs.FromDataError(err, "")
	}

	expireAt := time.Now().Add(emailAuthTTL)
	link := uc.verifyBase + "?token=" + url.QueryEscape(token)
	if err := uc.mailer.SendCampusAuthLink(ctx, email, link, expireAt); err != nil {
		uc.log.ErrorContext(ctx, "发送认证邮件失败", slog.Any("error", err))
		return nil, errs.Unavailable("邮件服务暂时不可用，请稍后再试")
	}

	return &StartEmailAuthResult{Email: email, ExpireAt: expireAt}, nil
}

// VerifyEmailAuth 校验邮箱认证链接。
func (uc *CampusAuthUsecase) VerifyEmailAuth(ctx context.Context, token string) (*CampusAuth, error) {
	authID, ok, err := uc.tokens.Consume(ctx, token)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if !ok {
		return nil, errs.InvalidArgument("认证链接无效或已过期，请重新发起认证")
	}

	record, err := uc.auths.GetByID(ctx, authID)
	if err != nil {
		return nil, errs.FromDataError(err, "认证记录不存在")
	}
	if record.Status == CampusAuthPassed {
		// 令牌已被消费却又收到通过状态，说明是并发重复点击，直接返回成功。
		return record, nil
	}

	// 邮箱认证无过期时间：学生毕业后邮箱通常会被回收，
	// 届时由学校侧失效，平台无从判断具体毕业时间，强加过期只会误伤在校生。
	if err := uc.auths.MarkPassed(ctx, record.ID, 0, nil); err != nil {
		return nil, errs.FromDataError(err, "")
	}
	record.Status = CampusAuthPassed
	now := time.Now()
	record.VerifiedAt = &now

	uc.syncUserSnapshot(ctx, record)
	return record, nil
}

// SubmitStudentCardAuth 提交学号认证材料，进入人工审核。
func (uc *CampusAuthUsecase) SubmitStudentCardAuth(ctx context.Context, userID int64,
	schoolID, campusID int64, studentNo, realName string, imageKeys []string) (*CampusAuth, error) {

	if _, err := uc.schools.GetSchool(ctx, schoolID); err != nil {
		return nil, errs.FromDataError(err, "学校不存在")
	}
	campus, err := uc.schools.GetCampus(ctx, campusID)
	if err != nil {
		return nil, errs.FromDataError(err, "校区不存在")
	}
	if campus.SchoolID != schoolID {
		return nil, errs.InvalidArgument("校区不属于所选学校")
	}

	if cur, err := uc.GetMyAuth(ctx, userID); err == nil {
		switch cur.Status {
		case CampusAuthPassed:
			return nil, errs.Conflict("已完成校园认证")
		case CampusAuthPending:
			return nil, errs.Conflict("已有认证申请在审核中，请耐心等待")
		}
	}

	// 一个学号只能绑定一个账号——这是防小号的关键约束。
	taken, err := uc.auths.ExistsPassedStudentNo(ctx, schoolID, studentNo, userID)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}
	if taken {
		return nil, errs.AlreadyExists("该学号已被其他账号认证，如有异议请联系管理员")
	}

	// 校验图片确实已上传，且 key 属于本人的上传目录。
	//
	// 前者防止客户端伪造 key 蒙过审核；后者防止盗用他人上传的对象——
	// 预签名直传的模式下，服务端不参与传输，这两项校验是唯一的把关点。
	for _, key := range imageKeys {
		if !belongsToUser(key, userID) {
			return nil, errs.PermissionDenied("图片归属校验失败")
		}
		if _, err := uc.files.Stat(ctx, key); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, errs.InvalidArgument("图片尚未上传完成，请重试")
			}
			return nil, errs.Unavailable("图片服务暂时不可用")
		}
	}

	id, err := uc.ids.NextID()
	if err != nil {
		return nil, errs.Internal("ID 生成失败")
	}
	record := &CampusAuth{
		ID:        id,
		UserID:    userID,
		SchoolID:  schoolID,
		CampusID:  campusID,
		Method:    AuthMethodStudentCard,
		Status:    CampusAuthPending,
		StudentNo: studentNo,
		RealName:  realName,
		ImageKeys: imageKeys,
	}
	created, err := uc.auths.Create(ctx, record)
	if err != nil {
		return nil, errs.FromDataError(err, "")
	}

	if err := uc.users.UpdateCampusSnapshot(ctx, userID, CampusAuthPending, schoolID, campusID); err != nil {
		uc.log.WarnContext(ctx, "同步认证状态到用户表失败", slog.Any("error", err))
	}

	// 通知 review-service 生成人工审核工单。
	uc.publishAuth(ctx, TopicCampusAuthSubmitted, created)
	return created, nil
}

// ReviewStudentCardAuth 人工审核结论回写，由 review-service 通过内部 RPC 调用。
func (uc *CampusAuthUsecase) ReviewStudentCardAuth(ctx context.Context, authID int64,
	reviewerID int64, pass bool, rejectReason string) error {

	record, err := uc.auths.GetByID(ctx, authID)
	if err != nil {
		return errs.FromDataError(err, "认证记录不存在")
	}
	if record.Status != CampusAuthPending {
		return errs.Conflict("该认证申请已处理")
	}

	if !pass {
		if err := uc.auths.MarkRejected(ctx, authID, reviewerID, rejectReason); err != nil {
			return errs.FromDataError(err, "")
		}
		if err := uc.users.UpdateCampusSnapshot(ctx, record.UserID,
			CampusAuthRejected, record.SchoolID, record.CampusID); err != nil {
			uc.log.WarnContext(ctx, "同步驳回状态失败", slog.Any("error", err))
		}
		return nil
	}

	if err := uc.auths.MarkPassed(ctx, authID, reviewerID, nil); err != nil {
		return errs.FromDataError(err, "")
	}
	record.Status = CampusAuthPassed
	uc.syncUserSnapshot(ctx, record)
	return nil
}

// syncUserSnapshot 把认证结果同步到用户表冗余字段并发布事件。
func (uc *CampusAuthUsecase) syncUserSnapshot(ctx context.Context, a *CampusAuth) {
	if err := uc.users.UpdateCampusSnapshot(ctx, a.UserID,
		CampusAuthPassed, a.SchoolID, a.CampusID); err != nil {
		// 这里失败会导致用户表与认证表不一致，是需要告警的：
		// 用户已通过认证却因冗余字段未更新而无法发布商品。
		uc.log.ErrorContext(ctx, "同步认证通过状态到用户表失败",
			slog.Int64("user_id", a.UserID), slog.Any("error", err))
	}
	uc.publishAuth(ctx, TopicCampusAuthPassed, a)
}

func (uc *CampusAuthUsecase) publishAuth(ctx context.Context, topic string, a *CampusAuth) {
	if uc.events == nil {
		return
	}
	payload := []byte(`{"auth_id":` + strconv.FormatInt(a.ID, 10) +
		`,"user_id":` + strconv.FormatInt(a.UserID, 10) +
		`,"school_id":` + strconv.FormatInt(a.SchoolID, 10) +
		`,"campus_id":` + strconv.FormatInt(a.CampusID, 10) +
		`,"method":` + strconv.FormatUint(uint64(a.Method), 10) + `}`)
	if err := uc.events.Publish(ctx, topic, payload); err != nil {
		uc.log.WarnContext(ctx, "发布认证事件失败",
			slog.String("topic", topic), slog.Any("error", err))
	}
}

// PurgeAuthImages 清理超过保留期的学生证照片，由定时任务调用。
//
// 返回已清理的记录数。单次处理上限 limit，避免一次任务长时间占用连接。
func (uc *CampusAuthUsecase) PurgeAuthImages(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	records, err := uc.auths.ListImagesToPurge(ctx, authImageRetention, limit)
	if err != nil {
		return 0, err
	}

	purged := 0
	for _, r := range records {
		if len(r.ImageKeys) > 0 {
			if err := uc.files.Delete(ctx, r.ImageKeys...); err != nil {
				// 单条失败不中断整批：下次任务会重新捞到它。
				uc.log.WarnContext(ctx, "删除认证照片失败",
					slog.Int64("auth_id", r.ID), slog.Any("error", err))
				continue
			}
		}
		if err := uc.auths.MarkImagePurged(ctx, r.ID); err != nil {
			uc.log.WarnContext(ctx, "标记照片已清理失败",
				slog.Int64("auth_id", r.ID), slog.Any("error", err))
			continue
		}
		purged++
	}
	return purged, nil
}

// belongsToUser 校验对象 key 是否位于该用户的上传目录下。
//
// 约定的 key 形态为 "tmp/{userID}/..." 或 "campus-auth/{userID}/..."。
// 这条校验挡住的是「拿到他人 key 后冒用其上传内容」。
func belongsToUser(key string, userID int64) bool {
	want := "/" + strconv.FormatInt(userID, 10) + "/"
	i := strings.Index(key, "/")
	if i < 0 {
		return false
	}
	return strings.HasPrefix(key[i:], want)
}

// randomToken 生成 URL 安全的随机令牌。
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func paginate(page, pageSize int) (int, int, int) {
	if page <= 0 {
		page = 1
	}
	switch {
	case pageSize <= 0:
		pageSize = 20
	case pageSize > 100:
		pageSize = 100
	}
	return page, pageSize, (page - 1) * pageSize
}
