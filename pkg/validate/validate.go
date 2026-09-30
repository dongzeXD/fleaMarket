// Package validate 把 protobuf 的 buf.validate 约束接入 kratos 中间件。
//
// 关键价值（见 doc.md §6.2）：校验规则写在 proto 的字段注解里，
// 一处声明同时产生两个效果——出现在 OpenAPI 文档中作为字段约束说明，
// 以及在服务端运行时强制执行。不需要在 Go 和 TypeScript 里各写一遍。
//
// kratos v3 已提供 middleware/validate.Validator(ValidatorFunc) 的挂载点，
// 本包只负责提供 protovalidate 的 ValidatorFunc 实现与错误信息整形。
package validate

import (
	"errors"
	"fmt"
	"strings"

	"buf.build/go/protovalidate"
	"github.com/go-kratos/kratos/v3/middleware"
	kratosvalidate "github.com/go-kratos/kratos/v3/middleware/validate"
	"google.golang.org/protobuf/proto"
)

// Middleware 返回校验中间件。
//
// protovalidate.New 需编译 CEL 表达式，开销较大，因此在服务启动时构造一次复用，
// 而不是每次请求新建。
func Middleware() (middleware.Middleware, error) {
	v, err := protovalidate.New()
	if err != nil {
		return nil, fmt.Errorf("validate: init protovalidate: %w", err)
	}
	return kratosvalidate.Validator(func(req any) error {
		msg, ok := req.(proto.Message)
		if !ok {
			// 非 protobuf 请求不校验，交由后续中间件处理。
			return nil
		}
		if err := v.Validate(msg); err != nil {
			return friendly(err)
		}
		return nil
	}), nil
}

// friendly 把多条违规整形为紧凑的一行提示。
//
// protovalidate 默认的多行错误（每条违规一行，前缀 " - "）在 HTTP JSON 响应里
// 是难读的转义换行，故压成分号分隔的单行。
// Violation.String() 本身已是 "字段路径: 原因" 的形式，直接复用。
func friendly(err error) error {
	var ve *protovalidate.ValidationError
	if !errors.As(err, &ve) || len(ve.Violations) == 0 {
		return err
	}
	parts := make([]string, 0, len(ve.Violations))
	for _, v := range ve.Violations {
		parts = append(parts, v.String())
	}
	return fmt.Errorf("参数校验失败 [%s]", strings.Join(parts, "; "))
}
