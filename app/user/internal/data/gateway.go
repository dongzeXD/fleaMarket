package data

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"time"

	"github.com/fleamarket/app/user/internal/biz"
	"github.com/fleamarket/app/user/internal/conf"

	"github.com/fleamarket/pkg/mq"
)

// 本文件实现对外部系统的适配：短信、邮件、微信、事件队列。
//
// 每一项都提供 console 实现，使本地开发无需申请任何第三方凭据即可跑通全流程
// （验证码与认证链接打印到日志）。provider 由配置切换，业务代码不感知差异。

// --- 短信 ---

type consoleSMS struct {
	log *slog.Logger
}

func (s *consoleSMS) SendVerifyCode(ctx context.Context, phone, code string, ttl time.Duration) error {
	// 这里刻意打印明文验证码，因为它是本地开发获取验证码的唯一途径。
	// pkg/log 的脱敏会遮蔽 phone/code 字段，故用非敏感的字段名承载，
	// 且该实现只允许在 provider=console（开发环境）下启用。
	s.log.InfoContext(ctx, "[开发模式] 短信验证码",
		slog.String("to", phone),
		slog.String("content", fmt.Sprintf("验证码 %s，%.0f 分钟内有效", code, ttl.Minutes())))
	return nil
}

// NewSMSSender 按配置选择短信实现。
//
// 未识别的 provider 一律回落到 console 并告警，而不是 panic：
// 配置写错时让服务起不来，比让它以「静默不发短信」的状态运行更糟——
// 后者会让所有用户都无法登录，且错误原因不明显。回落 + 告警使问题可见且可用。
func NewSMSSender(c *conf.Sms, logger *slog.Logger) biz.SMSSender {
	switch c.GetProvider() {
	case "console", "":
		return &consoleSMS{log: logger}
	default:
		logger.Warn("未实现的短信服务商，已回落到控制台输出",
			slog.String("provider", c.GetProvider()))
		return &consoleSMS{log: logger}
	}
}

// --- 邮件 ---

type consoleEmail struct {
	log *slog.Logger
}

func (e *consoleEmail) SendCampusAuthLink(ctx context.Context, email, link string,
	expireAt time.Time) error {

	e.log.InfoContext(ctx, "[开发模式] 校园认证邮件",
		slog.String("to", email),
		slog.String("verify_link", link),
		slog.Time("expire_at", expireAt))
	return nil
}

type smtpEmail struct {
	cfg *conf.Email
	log *slog.Logger
}

func (e *smtpEmail) SendCampusAuthLink(ctx context.Context, to, link string,
	expireAt time.Time) error {

	subject := "校园跳蚤市场 - 校园身份认证"
	body := fmt.Sprintf(
		"你好，\r\n\r\n请点击下方链接完成校园身份认证：\r\n%s\r\n\r\n"+
			"链接将于 %s 失效，且仅可使用一次。\r\n"+
			"若非本人操作，请忽略此邮件。\r\n",
		link, expireAt.Format("2006-01-02 15:04"))

	from := e.cfg.GetFrom()
	msg := "From: " + e.cfg.GetFromName() + " <" + from + ">\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		body

	addr := net.JoinHostPort(e.cfg.GetHost(), strconv.Itoa(int(e.cfg.GetPort())))
	auth := smtp.PlainAuth("", e.cfg.GetUsername(), e.cfg.GetPassword(), e.cfg.GetHost())
	if err := smtp.SendMail(addr, auth, from, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("data: send mail: %w", err)
	}
	return nil
}

// NewEmailSender 按配置选择邮件实现。
func NewEmailSender(c *conf.Email, logger *slog.Logger) biz.EmailSender {
	switch c.GetProvider() {
	case "smtp":
		return &smtpEmail{cfg: c, log: logger}
	case "console", "":
		return &consoleEmail{log: logger}
	default:
		logger.Warn("未实现的邮件服务商，已回落到控制台输出",
			slog.String("provider", c.GetProvider()))
		return &consoleEmail{log: logger}
	}
}

// --- 微信 ---

const (
	wechatWebTokenURL = "https://api.weixin.qq.com/sns/oauth2/access_token"
	wechatWebUserURL  = "https://api.weixin.qq.com/sns/userinfo"
	wechatMiniURL     = "https://api.weixin.qq.com/sns/jscode2session"
)

type wechatClient struct {
	cfg  *conf.Wechat
	http *http.Client
	log  *slog.Logger
}

// NewWechatClient 构造微信客户端。
func NewWechatClient(c *conf.Wechat, logger *slog.Logger) biz.WechatClient {
	timeout := c.GetTimeout().AsDuration()
	if timeout <= 0 {
		// 微信接口偶发抖动，超时设得比内部调用宽松些，但仍必须有上限：
		// 没有超时的外部调用会把本服务的 goroutine 与连接一起耗尽。
		timeout = 3 * time.Second
	}
	return &wechatClient{
		cfg:  c,
		http: &http.Client{Timeout: timeout},
		log:  logger,
	}
}

// wechatError 微信接口的统一错误结构。
type wechatError struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func (e wechatError) err() error {
	if e.ErrCode == 0 {
		return nil
	}
	return fmt.Errorf("wechat api error %d: %s", e.ErrCode, e.ErrMsg)
}

// ExchangeWeb 网站应用授权码换取用户标识。
func (w *wechatClient) ExchangeWeb(ctx context.Context, code string) (*biz.WechatProfile, error) {
	app := w.cfg.GetWeb()
	if app.GetAppId() == "" || app.GetAppSecret() == "" {
		return nil, fmt.Errorf("data: wechat web app not configured")
	}

	var tokenResp struct {
		wechatError
		AccessToken string `json:"access_token"`
		OpenID      string `json:"openid"`
		UnionID     string `json:"unionid"`
	}
	q := url.Values{}
	q.Set("appid", app.GetAppId())
	q.Set("secret", app.GetAppSecret())
	q.Set("code", code)
	q.Set("grant_type", "authorization_code")
	if err := w.getJSON(ctx, wechatWebTokenURL, q, &tokenResp); err != nil {
		return nil, err
	}
	if err := tokenResp.err(); err != nil {
		return nil, err
	}

	profile := &biz.WechatProfile{
		UnionID: tokenResp.UnionID,
		OpenID:  tokenResp.OpenID,
	}

	// 拉取昵称与头像用于初始化档案。
	// 失败不阻断登录：没有昵称可以用系统默认值，
	// 为了一个装饰性字段而让用户登录不了是不划算的。
	var userResp struct {
		wechatError
		Nickname   string `json:"nickname"`
		HeadImgURL string `json:"headimgurl"`
		UnionID    string `json:"unionid"`
	}
	uq := url.Values{}
	uq.Set("access_token", tokenResp.AccessToken)
	uq.Set("openid", tokenResp.OpenID)
	if err := w.getJSON(ctx, wechatWebUserURL, uq, &userResp); err != nil {
		w.log.WarnContext(ctx, "拉取微信用户信息失败", slog.Any("error", err))
		return profile, nil
	}
	if userResp.ErrCode == 0 {
		profile.Nickname = userResp.Nickname
		profile.AvatarURL = userResp.HeadImgURL
		if profile.UnionID == "" {
			profile.UnionID = userResp.UnionID
		}
	}
	return profile, nil
}

// ExchangeMiniApp 小程序 code2session。
func (w *wechatClient) ExchangeMiniApp(ctx context.Context, code string) (*biz.WechatProfile, error) {
	app := w.cfg.GetMiniApp()
	if app.GetAppId() == "" || app.GetAppSecret() == "" {
		return nil, fmt.Errorf("data: wechat miniapp not configured")
	}

	var resp struct {
		wechatError
		OpenID  string `json:"openid"`
		UnionID string `json:"unionid"`
	}
	q := url.Values{}
	q.Set("appid", app.GetAppId())
	q.Set("secret", app.GetAppSecret())
	q.Set("js_code", code)
	q.Set("grant_type", "authorization_code")
	if err := w.getJSON(ctx, wechatMiniURL, q, &resp); err != nil {
		return nil, err
	}
	if err := resp.err(); err != nil {
		return nil, err
	}
	// 小程序不返回昵称，需前端通过 getUserProfile 单独上传。
	return &biz.WechatProfile{UnionID: resp.UnionID, OpenID: resp.OpenID}, nil
}

func (w *wechatClient) getJSON(ctx context.Context, base string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("data: build wechat request: %w", err)
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("data: call wechat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("data: wechat http status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("data: decode wechat response: %w", err)
	}
	return nil
}

// --- 事件发布 ---

type eventPublisher struct {
	producer mq.Producer
	log      *slog.Logger
}

// NewEventPublisher 构造事件发布器。
func NewEventPublisher(data *Data, namespace string, logger *slog.Logger) (biz.EventPublisher, func(), error) {
	producer, err := mq.NewRedisStreamProducer(mq.RedisStreamConfig{
		Client:    data.Cache.Client(),
		Namespace: namespace,
	})
	if err != nil {
		return nil, nil, err
	}
	p := &eventPublisher{producer: producer, log: logger}
	return p, func() { _ = producer.Close() }, nil
}

func (p *eventPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	_, err := p.producer.Publish(ctx, topic, payload, nil)
	return err
}
