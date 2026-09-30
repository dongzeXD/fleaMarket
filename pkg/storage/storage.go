// Package storage 提供对象存储抽象与 S3 兼容实现。
//
// 核心能力是预签名 URL：客户端直传对象存储，图片不经过业务服务。
// 这样做的收益是服务端不必为上传承担带宽与内存——一张 5MB 的图片
// 若经服务端转发，在高并发下会迅速吃满内存与网卡。
package storage

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound 对象不存在。
var ErrNotFound = errors.New("storage: object not found")

// ObjectInfo 对象元信息。
type ObjectInfo struct {
	Key          string
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
}

// PresignedUpload 预签名上传凭证。
type PresignedUpload struct {
	// URL 客户端 PUT 的目标地址。
	URL string
	// Key 对象在存储中的 key，客户端上传成功后回传给业务接口。
	Key string
	// Method HTTP 方法，固定为 PUT。
	Method string
	// Headers 客户端必须携带的请求头。
	//
	// 其中 Content-Type 是签名的一部分：客户端若改用其他类型，
	// 签名校验会失败。这是在服务端不参与传输的前提下限制文件类型的手段。
	Headers map[string]string
	// ExpireAt 凭证过期时间。
	ExpireAt time.Time
	// MaxSize 允许的最大字节数，供客户端前置校验。
	MaxSize int64
}

// Storage 对象存储接口。
type Storage interface {
	// CreateUploadURL 签发上传凭证。
	//
	// key 由调用方决定，应包含用户隔离前缀（如 "tmp/{userID}/{uuid}.jpg"），
	// 使服务端在提交阶段能校验 key 的归属，防止用户冒用他人上传的对象。
	CreateUploadURL(ctx context.Context, req UploadRequest) (*PresignedUpload, error)

	// PublicURL 返回对象的可访问地址。
	//
	// 公开桶直接拼 CDN 域名；私有桶签发带有效期的下载链接。
	// process 为图片处理参数（如 "resize,w_400"），由实现按存储厂商的
	// 语法拼接；不支持处理的实现应忽略该参数而非报错。
	PublicURL(ctx context.Context, key string, process string, ttl time.Duration) (string, error)

	// Stat 查询对象元信息。用于在提交阶段确认客户端确实完成了上传，
	// 以及校验真实的 Content-Type 与大小。
	Stat(ctx context.Context, key string) (*ObjectInfo, error)

	// Delete 删除对象。用于清理未被引用的临时上传，以及按隐私要求
	// 定期删除学生证照片这类敏感材料。
	Delete(ctx context.Context, keys ...string) error

	// Move 移动对象（复制后删除源）。
	//
	// 用于把通过校验的临时对象从 tmp/ 迁到正式目录，
	// 使「未提交的上传」可以被生命周期规则统一清理。
	Move(ctx context.Context, srcKey, dstKey string) error
}

// UploadRequest 签发上传凭证的参数。
type UploadRequest struct {
	// Key 目标对象 key。
	Key string
	// ContentType 允许的 MIME 类型，参与签名。
	ContentType string
	// MaxSize 最大字节数。
	MaxSize int64
	// TTL 凭证有效期。过短会导致弱网用户上传失败，过长则扩大凭证泄漏的影响窗口。
	TTL time.Duration
}

// Validate 校验参数完整性。
func (r UploadRequest) Validate() error {
	switch {
	case r.Key == "":
		return errors.New("storage: empty key")
	case r.ContentType == "":
		return errors.New("storage: empty content type")
	case r.MaxSize <= 0:
		return errors.New("storage: MaxSize must be positive")
	case r.TTL <= 0:
		return errors.New("storage: TTL must be positive")
	default:
		return nil
	}
}
