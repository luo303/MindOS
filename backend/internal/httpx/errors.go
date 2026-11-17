package httpx

// 本文件定义"业务错误"这一层抽象。建议按顺序读：
//  1. Error 结构体 —— 一个业务错误由哪几部分组成
//  2. Error() 方法 —— 它凭什么能当作 error 使用
//  3. Wrap / WithMessage —— 如何在保留错误码的前提下附加信息
//  4. 预定义错误 + IsCode —— 业务代码怎么消费它们
//
// ============================================================================
// 读这个文件之前，必须先建立两个 Go 的心智模型（后面不再重复解释）
// ============================================================================
//
// 【模型一：Go 没有 class，只有 struct + 方法】
//
//	TS:  class AppError extends Error { constructor(...) { ... } }
//	Go:  type Error struct { ... }            ← 只定义数据
//	     func (e *Error) Error() string {...}  ← 方法写在类型外面
//
// 括号里那段 (e *Error) 叫"接收者"（receiver），相当于 TS 里的 this。
// 写 *Error 表示"由指针调用"，写 Error 表示"值也能调"。这不只是写法差别，
// 它会直接决定一个类型能否满足某个接口，见下面的 Error() 方法。
//
// 【模型二：接口是隐式实现的】
//
//	TS:  class Foo implements Bar { ... }   ← 必须显式声明
//	Go:  什么都不写，方法凑齐了就自动满足接口
//
// 标准库的 error 接口定义只有一行：
//
//	type error interface { Error() string }
//
// 所以下面的 *Error 一个 implements 都没写，却天然是一个 error 值。
// 由此得出一个反直觉的结论：**接口通常由使用方定义，而非实现方声明**。
// 本项目的 handler.PingFunc 就是这个思路。

import (
	"errors"
	"fmt"
	"net/http"
)

// Error 表示一个"可以返回给客户端"的业务错误。
//
// 【Go 语法：字段首字母大小写 = 可见性】
//
//	大写 = 导出（包外可访问，相当于 TS 的 public）
//	小写 = 包内私有（由编译器强制，不是靠命名约定）
//
// cause 故意小写：外部拿不到，只能通过 Unwrap() 间接访问。
// 这是把不变量锁在包里的做法，防止别处直接改写内部原因。
type Error struct {
	Code       string // 机器可读错误码，例如 USER_NOT_FOUND
	Message    string // 面向调用方的说明，会原样返回给客户端
	HTTPStatus int    // 对应的 HTTP 状态码
	cause      error  // 内部原因（数据库错误、第三方错误），不返回给客户端
}

// Error 让 *Error 实现 error 接口。
//
// 【为什么接收者是 *Error 而不是 Error】
// Go 的方法集规则：
//
//	类型 T  的方法集 = 接收者为 T 的方法
//	类型 *T 的方法集 = 接收者为 T 和 *T 的方法
//
// 这里写成 (e *Error)，于是只有 *Error 拥有 Error()，Error（值）没有：
// `var err error = &Error{...}` 合法，而 `var err error = Error{...}` 编译不过。
//
// 【为什么选指针】两个实际原因：
//  1. 不复制结构体。Go 的参数传递和赋值都是值拷贝，4 个字段虽小，但这是普遍习惯。
//  2. 与 Wrap / WithMessage 返回 *Error 保持一致。全项目只留一种形态，
//     省得以后在 errors.As 里猜"这里是值还是指针"——那是最难查的一类 bug。
//
// 【易错点】若把接收者改成值 (e Error)，一旦代码里同时出现 Error{} 字面量和 &Error{}，
// 接口判定就会漏，表现为"明明包装过，却取不到错误码"。
func (e *Error) Error() string {
	// 字符串拼接用 + 就够，编译器会优化，不需要 fmt.Sprintf。
	// 错误信息可能出现在热路径上，少一层格式化开销。
	if e.cause != nil {
		return e.Code + ": " + e.Message + ": " + e.cause.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap 支持 errors.Is / errors.As 沿链查找。
//
// 【机制：错误链不是语法特性，而是一个约定】
// Go 没有异常栈，它靠一个约定俗成的方法名把因果串起来：
// 任何类型只要实现了 Unwrap() error，标准库的 errors.Is/As 就会顺着继续往里找。
//
//	ErrNotFound.Wrap(dbErr)  →  errors.Is(err, dbErr) == true
//
// 自己写的包装类型只要提供这个方法，就自动接入标准库错误链，无需注册。
func (e *Error) Unwrap() error { return e.cause }

// WithMessage 基于预定义错误派生出带自定义提示的新错误。
//
// 【Go 语法点】
//   - format string, args ...any：args 是变参（variadic），调用时可传 0 个或多个参数，
//     函数内部它就是一个 []any。any 是 interface{} 的别名（Go 1.18 起官方推荐写法）。
//   - cp := *e：结构体是值类型，*e 解引用再赋值 = 一次逐字段浅拷贝。
//     这是 Go 里复制结构体最常用的写法，不需要任何 clone 工具。
//
// 【为什么要判断 len(args) == 0】
// fmt.Sprintf("50% off") 会把 % 当格式符，输出 "50%!o(MISSING)ff"。
// 无参数时直接赋值，才能安全传递含 % 的字面量。错误信息里带百分比、带 SQL 时必踩。
func (e *Error) WithMessage(format string, args ...any) *Error {
	cp := *e
	if len(args) == 0 {
		cp.Message = format
	} else {
		cp.Message = fmt.Sprintf(format, args...)
	}
	return &cp
}

// Wrap 在保留错误码与提示的前提下附加上内部原因。
//
// 【必须返回副本 —— 本文件最重要的设计】
// 下面 var 块里的 ErrInternal 是包级变量，全项目共享同一个实例。
// 若这里写 e.cause = cause（直接改自己），两个并发请求会互相覆盖对方的失败原因，
// 这本身就是数据竞争（go test -race 能抓到）。日志里会出现"订单错误套着用户查询错误"
// 这种灵异现象。
//
// 所以请记住：**预定义错误是模板，派生必须复制**。errors_test.go 里有测试守着这条。
//
// 【浅拷贝的边界】cp := *e 只复制字段。cause 是接口值（类型指针 + 数据指针），
// 复制后新旧 Error 指向同一个 cause 对象。这里安全，因为本包从不修改 cause，只替换。
// 若将来 cause 变成可变结构体，就得改深拷贝。
func (e *Error) Wrap(cause error) *Error {
	cp := *e
	cp.cause = cause
	return &cp
}

// 预定义错误。多个 handler / service 复用，保证错误码一致。
//
// 【Go 语法：var 块】var ( a = ...; b = ... ) 是批量声明，等价于写多个 var。
// 每个值都是 &Error{...}，即指向结构体字面量的指针。
//
// 【并发安全吗】安全，因为它们是只读的：所有派生操作（WithMessage / Wrap）都返回副本，
// 没有任何代码写这些字段。一旦有人直接写 ErrNotFound.Message = "xxx"，
// 就会同时污染所有请求 —— 这是 AGENTS.md 里禁止的行为。
var (
	ErrInternal             = &Error{Code: "INTERNAL", Message: "internal server error", HTTPStatus: http.StatusInternalServerError}
	ErrInvalidArgument      = &Error{Code: "INVALID_ARGUMENT", Message: "invalid argument", HTTPStatus: http.StatusBadRequest}
	ErrUnauthorized         = &Error{Code: "UNAUTHORIZED", Message: "unauthorized", HTTPStatus: http.StatusUnauthorized}
	ErrForbidden            = &Error{Code: "FORBIDDEN", Message: "forbidden", HTTPStatus: http.StatusForbidden}
	ErrNotFound             = &Error{Code: "NOT_FOUND", Message: "resource not found", HTTPStatus: http.StatusNotFound}
	ErrConflict             = &Error{Code: "CONFLICT", Message: "resource conflict", HTTPStatus: http.StatusConflict}
	ErrPayloadTooLarge      = &Error{Code: "PAYLOAD_TOO_LARGE", Message: "payload too large", HTTPStatus: http.StatusRequestEntityTooLarge}
	ErrUnsupportedMediaType = &Error{Code: "UNSUPPORTED_MEDIA_TYPE", Message: "unsupported media type", HTTPStatus: http.StatusUnsupportedMediaType}
	ErrServiceUnavailable   = &Error{Code: "SERVICE_UNAVAILABLE", Message: "service unavailable", HTTPStatus: http.StatusServiceUnavailable}
	// ErrModelNotConfigured 表示能力缺失而不是故障：向量模型三个配置项全空，
	// 或库里一条对话模型配置都没有。
	// 用 503 而不是 400——请求本身没问题，是这台机器还没配好。
	ErrModelNotConfigured = &Error{Code: "MODEL_NOT_CONFIGURED", Message: "model is not configured", HTTPStatus: http.StatusServiceUnavailable}
	// ErrEmbeddingNotConfigured 表示向量模型没配。
	//
	// 问答不因此退化成纯全文检索：中文全文在 simple 配置下基本召回不了东西，
	// 退化只会给用户"能答但不准"的假象，比直接说"还不能用"更糟。
	ErrEmbeddingNotConfigured = &Error{Code: "EMBEDDING_NOT_CONFIGURED", Message: "embedding model is not configured", HTTPStatus: http.StatusServiceUnavailable}
	// ErrModelNotSelected 表示有多条对话模型配置，但没有任何一条被设为默认，
	// 请求也没指定用哪条。
	// 用 400 而不是 503：服务是好的，缺的是"用哪个"这个参数。
	ErrModelNotSelected = &Error{Code: "MODEL_NOT_SELECTED", Message: "multiple chat models exist, none is the default", HTTPStatus: http.StatusBadRequest}
	// ErrChatModelNotFound 表示指定的对话模型配置不存在。
	ErrChatModelNotFound = &Error{Code: "CHAT_MODEL_NOT_FOUND", Message: "chat model does not exist", HTTPStatus: http.StatusNotFound}
	// ErrChatModelNameConflict 表示配置重名。
	ErrChatModelNameConflict = &Error{Code: "MODEL_NAME_CONFLICT", Message: "a chat model with this name already exists", HTTPStatus: http.StatusConflict}
	// ErrMasterKeyMismatch 表示已存的 API Key 解不开（主密钥换了、文件被删）。
	//
	// 用 500 而不是 401：这不是"用户没权限"，是这台机器上的加密材料对不上，
	// 只能重新填一次 Key，或者把备份的主密钥放回去。
	ErrMasterKeyMismatch = &Error{Code: "MASTER_KEY_MISMATCH", Message: "stored credential cannot be decrypted; please re-enter the API key", HTTPStatus: http.StatusInternalServerError}
	// ErrUpstreamFailed 表示外部模型服务返回了失败（凭据错、参数错、限流、超时）。
	// 用 502：错误发生在我们之外，客户端重试可能有用也可能没用，
	// 具体原因在 message 里，code 不再细分——细分留给任务表里的 error_code。
	ErrUpstreamFailed = &Error{Code: "UPSTREAM_FAILED", Message: "upstream model service failed", HTTPStatus: http.StatusBadGateway}

	// —— 账号与登录态 ——
	//
	// 这里刻意不用上面那个通用的 ErrUnauthorized：前端对"没登录"和"密码错"
	// 的处理完全不同（一个跳登录页，一个提示输入错误），共用一个码就分不开。
	//
	// ErrAuthRequired 表示没带 Cookie，或会话已过期/被撤销。
	ErrAuthRequired = &Error{Code: "AUTH_REQUIRED", Message: "authentication required", HTTPStatus: http.StatusUnauthorized}
	// ErrInvalidCredentials 表示邮箱或密码不对。
	// 文案不区分"用户不存在"与"密码错"：区分开就等于提供账号枚举接口。
	ErrInvalidCredentials = &Error{Code: "AUTH_INVALID_CREDENTIALS", Message: "invalid email or password", HTTPStatus: http.StatusUnauthorized}
	// ErrAccountLocked 表示连续失败触发了临时锁定。用 429 而不是 401：
	// 凭据可能是对的，是"现在不许试"，客户端应看 Retry-After 而不是清空表单。
	ErrAccountLocked = &Error{Code: "AUTH_ACCOUNT_LOCKED", Message: "too many failed attempts; try again later", HTTPStatus: http.StatusTooManyRequests}
	// ErrAccountDisabled 表示账号被管理员停用。
	ErrAccountDisabled = &Error{Code: "AUTH_ACCOUNT_DISABLED", Message: "this account is disabled", HTTPStatus: http.StatusForbidden}
	// ErrEmailTaken 表示注册的邮箱已存在。
	ErrEmailTaken = &Error{Code: "AUTH_EMAIL_TAKEN", Message: "this email is already registered", HTTPStatus: http.StatusConflict}
	// ErrWeakPassword 表示长度不符合策略。
	ErrWeakPassword = &Error{Code: "AUTH_WEAK_PASSWORD", Message: "password does not meet the length requirement", HTTPStatus: http.StatusBadRequest}
	// ErrRegistrationClosed 表示部署关闭了自助注册。
	ErrRegistrationClosed = &Error{Code: "AUTH_REGISTRATION_CLOSED", Message: "self-service registration is disabled", HTTPStatus: http.StatusForbidden}
	// ErrPasswordMismatch 表示修改密码时旧密码不对。
	ErrPasswordMismatch = &Error{Code: "AUTH_PASSWORD_MISMATCH", Message: "current password is incorrect", HTTPStatus: http.StatusBadRequest}
	// ErrOriginMismatch 表示跨站写请求被拒（CSRF 防护）。
	ErrOriginMismatch = &Error{Code: "AUTH_ORIGIN_MISMATCH", Message: "cross-site request rejected", HTTPStatus: http.StatusForbidden}
)

// IsCode 判断 err 链上是否有指定错误码的 *Error。
//
// 【为什么 errors.As 的第二个参数是 &appErr】
// appErr 的类型是 *Error，&appErr 就是 **Error（指针的指针）。
// 标准库需要指针的指针才能把结果写回调用方的变量 —— Go 全是值传递，
// 只传 *Error 的话，函数内部改的是副本。
//
// 【为什么不用类型断言 err.(*Error)】
// 错误可能被 fmt.Errorf("...: %w") 包了多层，类型断言只看得到最外层。
// 经验法则：**判断或提取底层错误，永远用 errors.Is / errors.As，不要用类型断言。**
func IsCode(err error, code string) bool {
	var appErr *Error
	return errors.As(err, &appErr) && appErr.Code == code
}
