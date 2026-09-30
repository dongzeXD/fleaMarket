package storage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config S3 兼容存储的配置。
//
// 本地开发指向 MinIO，生产指向云厂商的 S3 兼容端点，代码无差异。
type S3Config struct {
	// Endpoint 服务端点，不含协议，如 "localhost:9000"。
	Endpoint string
	// AccessKey / SecretKey 凭据。必须由环境变量或密钥管理服务注入，
	// 不得出现在配置文件中。
	AccessKey string
	SecretKey string
	// UseSSL 是否使用 HTTPS。
	UseSSL bool
	// Region 区域，MinIO 可留空。
	Region string
	// Bucket 桶名。
	Bucket string
	// PublicBaseURL 公开访问的基础地址（CDN 域名）。
	//
	// 留空时 PublicURL 退化为签发带有效期的私有链接。
	// 生产环境应配置 CDN 域名，否则每张图片都要签名，既慢又无法被 CDN 缓存。
	PublicBaseURL string
	// ProcessQueryKey 图片处理参数的 query 参数名。
	//
	// 不同厂商语法不同（阿里云 x-oss-process、腾讯云 imageMogr2），
	// 因此做成配置项而非硬编码，保持公共库对厂商中立。
	// 留空则忽略处理参数。
	ProcessQueryKey string
}

// S3Storage S3 兼容实现。
type S3Storage struct {
	cli *minio.Client
	cfg S3Config
}

var _ Storage = (*S3Storage)(nil)

// NewS3 创建 S3 存储客户端。
func NewS3(c S3Config) (*S3Storage, error) {
	switch {
	case c.Endpoint == "":
		return nil, errors.New("storage: empty endpoint")
	case c.Bucket == "":
		return nil, errors.New("storage: empty bucket")
	case c.AccessKey == "" || c.SecretKey == "":
		return nil, errors.New("storage: empty credentials")
	}

	cli, err := minio.New(c.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""),
		Secure: c.UseSSL,
		Region: c.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: init s3 client: %w", err)
	}
	return &S3Storage{cli: cli, cfg: c}, nil
}

// EnsureBucket 幂等创建桶。仅用于本地开发与测试初始化；
// 生产环境的桶应由基础设施代码预先创建并配置好生命周期规则。
func (s *S3Storage) EnsureBucket(ctx context.Context) error {
	exists, err := s.cli.BucketExists(ctx, s.cfg.Bucket)
	if err != nil {
		return fmt.Errorf("storage: check bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.cli.MakeBucket(ctx, s.cfg.Bucket, minio.MakeBucketOptions{Region: s.cfg.Region}); err != nil {
		return fmt.Errorf("storage: make bucket: %w", err)
	}
	return nil
}

func (s *S3Storage) CreateUploadURL(ctx context.Context, req UploadRequest) (*PresignedUpload, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// 把 Content-Type 纳入签名：客户端若上传其他类型，S3 端签名校验即失败。
	// 这是在「服务端不参与传输」前提下做类型限制的唯一可靠手段——
	// 仅靠客户端自觉或仅校验扩展名都是可绕过的。
	u, err := s.cli.PresignHeader(ctx, "PUT", s.cfg.Bucket, req.Key, req.TTL, nil,
		map[string][]string{"Content-Type": {req.ContentType}})
	if err != nil {
		return nil, fmt.Errorf("storage: presign put %s: %w", req.Key, err)
	}

	return &PresignedUpload{
		URL:      u.String(),
		Key:      req.Key,
		Method:   "PUT",
		Headers:  map[string]string{"Content-Type": req.ContentType},
		ExpireAt: time.Now().Add(req.TTL),
		MaxSize:  req.MaxSize,
	}, nil
}

func (s *S3Storage) PublicURL(ctx context.Context, key, process string, ttl time.Duration) (string, error) {
	if key == "" {
		return "", errors.New("storage: empty key")
	}

	// 配了 CDN 域名就直接拼，不签名——签名后的 URL 每次都不同，
	// 会让 CDN 缓存彻底失效。
	if s.cfg.PublicBaseURL != "" {
		base := strings.TrimRight(s.cfg.PublicBaseURL, "/")
		full := base + "/" + strings.TrimLeft(key, "/")
		if process != "" && s.cfg.ProcessQueryKey != "" {
			full += "?" + s.cfg.ProcessQueryKey + "=" + url.QueryEscape(process)
		}
		return full, nil
	}

	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	params := url.Values{}
	if process != "" && s.cfg.ProcessQueryKey != "" {
		params.Set(s.cfg.ProcessQueryKey, process)
	}
	u, err := s.cli.PresignedGetObject(ctx, s.cfg.Bucket, key, ttl, params)
	if err != nil {
		return "", fmt.Errorf("storage: presign get %s: %w", key, err)
	}
	return u.String(), nil
}

func (s *S3Storage) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	info, err := s.cli.StatObject(ctx, s.cfg.Bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: stat %s: %w", key, err)
	}
	return &ObjectInfo{
		Key:          key,
		Size:         info.Size,
		ContentType:  info.ContentType,
		ETag:         info.ETag,
		LastModified: info.LastModified,
	}, nil
}

func (s *S3Storage) Delete(ctx context.Context, keys ...string) error {
	for _, k := range keys {
		if err := s.cli.RemoveObject(ctx, s.cfg.Bucket, k, minio.RemoveObjectOptions{}); err != nil {
			return fmt.Errorf("storage: delete %s: %w", k, err)
		}
	}
	return nil
}

func (s *S3Storage) Move(ctx context.Context, srcKey, dstKey string) error {
	_, err := s.cli.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: s.cfg.Bucket, Object: dstKey},
		minio.CopySrcOptions{Bucket: s.cfg.Bucket, Object: srcKey},
	)
	if err != nil {
		return fmt.Errorf("storage: copy %s -> %s: %w", srcKey, dstKey, err)
	}
	// 复制成功后再删源。顺序反了会在中间失败时丢数据。
	if err := s.cli.RemoveObject(ctx, s.cfg.Bucket, srcKey,
		minio.RemoveObjectOptions{}); err != nil {
		// 源删除失败不影响目标已就绪，仅留下一个待清理的临时对象，
		// 由桶的生命周期规则兜底，因此不视为错误。
		return nil
	}
	return nil
}
