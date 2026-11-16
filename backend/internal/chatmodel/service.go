// 本文件是对话模型配置的业务层：校验、加密、默认项规则、取用哪一条。
//
// 它不认识 HTTP（handler 负责），也不认识 Eino（agent 负责）：连通性测试通过
// 注入进来的 Prober 完成，这样"配置怎么存"和"模型怎么调"两件事不绑在一起。
package chatmodel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// 模块级哨兵错误，handler 用 errors.Is 判断后翻成 HTTP 错误码。
var (
	// ErrNotFound 表示配置不存在（已被删除，或 id 从未存在）。
	ErrNotFound = errors.New("chat model not found")
	// ErrNameConflict 表示名字已被占用。
	//
	// 必须拦住：重名会让提问页的下拉框出现两个无法区分的选择，
	// 用户选错模型后只会觉得"回答质量不稳定"。
	ErrNameConflict = errors.New("chat model name already exists")
	// ErrNotConfigured 表示一条配置都没有。
	ErrNotConfigured = errors.New("no chat model is configured")
	// ErrNotSelected 表示有多条配置但没有任何一条是默认。
	ErrNotSelected = errors.New("multiple chat models configured but none is default")
	// ErrInvalidInput 表示字段不合法（空、超长、URL 协议不对）。
	ErrInvalidInput = errors.New("invalid chat model input")
)

// View 是对外可见的配置视图。
//
// 这里出现 APIKeyHint 而不是 APIKey：接口永远不回明文，前端只需要
// "是不是这把 Key"的确认。三态更新时也不会把 hint 回填进密码框。
type View struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	BaseURL          string     `json:"base_url"`
	Model            string     `json:"model"`
	APIKeyHint       string     `json:"api_key_hint"`
	APIKeyConfigured bool       `json:"api_key_configured"`
	Temperature      *float32   `json:"temperature"`
	MaxTokens        *int       `json:"max_tokens"`
	IsDefault        bool       `json:"is_default"`
	LastUsedAt       *time.Time `json:"last_used_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// Resolved 是一条已解密的配置，只用于构造模型调用。
//
// APIKey 是明文，**绝不允许出现在 HTTP 响应、日志、qa_runs 或错误信息里**。
// 它存在的理由只有一个：给 HTTP 客户端填 Authorization 头。
type Resolved struct {
	ID          int64
	Name        string
	BaseURL     string
	Model       string
	APIKey      string
	Temperature *float32
	MaxTokens   *int
}

// CreateInput 是新增配置的输入。
type CreateInput struct {
	Name        string
	BaseURL     string
	Model       string
	APIKey      string
	Temperature *float32
	MaxTokens   *int
	IsDefault   bool
}

// UpdateInput 是全量替换其余字段 + Key 三态。
//
// 为什么 Key 用指针而其余字段用值：其余字段前端手里都有，整体覆盖最不容易出错；
// 而 Key 前端永远拿不到明文，只能用"不传=不改、空串=清除、有值=替换"三态表达。
type UpdateInput struct {
	Name        string
	BaseURL     string
	Model       string
	APIKey      *string
	Temperature *float32
	MaxTokens   *int
}

// Probe 是一次连通性测试的结果。
type Probe struct {
	// Model 是本次实测用的模型名，由 Service 填。
	// 不能让前端拿列表里的名字来显示：那条配置可能刚被改过，
	// 而这次测试用的是改动之后的值。
	Model   string
	Reply   string
	Latency time.Duration
}

// Prober 由 cmd/server 注入：拿一条已解密的配置真发一次请求。
//
// 定义在这里而不是让 chatmodel 依赖 Eino：配置管理与模型调用是两件事，
// 混在一起会让这个包为了一个测试接口背上整个模型框架。
type Prober func(ctx context.Context, target *Resolved) (Probe, error)

// Service 编排对话模型配置的读写。
type Service struct {
	repo   *Repository
	cipher *Cipher
	prober Prober
}

// NewService 组装业务层。cipher 必须非空——落库即加密是硬约束，
// 允许传 nil 就等于给"明文存库"留了一个后门。
func NewService(repo *Repository, cipher *Cipher, prober Prober) *Service {
	return &Service{repo: repo, cipher: cipher, prober: prober}
}

// List 返回全部配置。
func (s *Service) List(ctx context.Context) ([]View, error) {
	items, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(items))
	for i := range items {
		views = append(views, *viewOf(&items[i]))
	}
	return views, nil
}

// Create 新增一条配置。
//
// 第一条配置自动成为默认：否则用户配完还要再点一次"设为默认"，
// 第一次提问必然收到 400，体验上说不过去。这不是"服务端内置默认模型"，
// 而是"唯一一条就是默认"这条规则的提前落地。
func (s *Service) Create(ctx context.Context, in CreateInput) (*View, error) {
	name, baseURL, model, err := normalizeModel(in.Name, in.BaseURL, in.Model)
	if err != nil {
		return nil, err
	}
	if err := validateOptions(in.Temperature, in.MaxTokens); err != nil {
		return nil, err
	}

	item := &ChatModel{
		Name:        name,
		BaseURL:     baseURL,
		Model:       model,
		Temperature: in.Temperature,
		MaxTokens:   in.MaxTokens,
	}
	if key := strings.TrimSpace(in.APIKey); key != "" {
		if err := validateKey(key); err != nil {
			return nil, err
		}
		cipherText, err := s.cipher.Encrypt(key)
		if err != nil {
			return nil, err
		}
		item.APIKeyCipher = cipherText
		item.APIKeyHint = hintOf(key)
	}

	total, err := s.repo.Count(ctx)
	if err != nil {
		return nil, err
	}
	// 先以"非默认"插入，再单独设默认：这样只有一条写入路径，
	// 也不会在旧默认还在时撞 chat_models_default_uniq。
	if err := s.repo.Create(ctx, item); err != nil {
		if errors.Is(err, errDuplicate) {
			return nil, ErrNameConflict
		}
		return nil, err
	}
	if in.IsDefault || total == 0 {
		if err := s.repo.SetDefault(ctx, item.ID); err != nil {
			return nil, err
		}
	}
	return s.reload(ctx, item.ID)
}

// Update 整体替换一条配置的可编辑字段。
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (*View, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	name, baseURL, model, err := normalizeModel(in.Name, in.BaseURL, in.Model)
	if err != nil {
		return nil, err
	}
	if err := validateOptions(in.Temperature, in.MaxTokens); err != nil {
		return nil, err
	}

	current.Name = name
	current.BaseURL = baseURL
	current.Model = model
	current.Temperature = in.Temperature
	current.MaxTokens = in.MaxTokens

	// Key 三态：nil 保留原密文，空串清除，有值则重新加密。
	// 清除时连 hint 一起清掉，否则界面会显示"已配置 ****a1b2"但其实没有 Key。
	if in.APIKey != nil {
		key := strings.TrimSpace(*in.APIKey)
		if key == "" {
			current.APIKeyCipher = nil
			current.APIKeyHint = ""
		} else {
			if err := validateKey(key); err != nil {
				return nil, err
			}
			cipherText, err := s.cipher.Encrypt(key)
			if err != nil {
				return nil, err
			}
			current.APIKeyCipher = cipherText
			current.APIKeyHint = hintOf(key)
		}
	}

	if err := s.repo.Update(ctx, current); err != nil {
		if errors.Is(err, errDuplicate) {
			return nil, ErrNameConflict
		}
		return nil, err
	}
	return s.reload(ctx, id)
}

// Delete 删除一条配置。
//
// 删默认时不做自动继任：让用户明确选下一条，比系统替他挑一条更诚实。
// 删完之后如果没有默认且还剩多条，提问会返回 ErrNotSelected，界面引导去选。
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.repo.Delete(ctx, id)
}

// SetDefault 指定默认配置。
func (s *Service) SetDefault(ctx context.Context, id int64) (*View, error) {
	if err := s.repo.SetDefault(ctx, id); err != nil {
		return nil, err
	}
	return s.reload(ctx, id)
}

// Resolve 选出本次请求要用的配置，并把 Key 解密出来。
//
// 只给构造模型用；调用方不得把它序列化出去。
func (s *Service) Resolve(ctx context.Context, id *int64) (*Resolved, error) {
	item, err := s.repo.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	resolved := &Resolved{
		ID:          item.ID,
		Name:        item.Name,
		BaseURL:     item.BaseURL,
		Model:       item.Model,
		Temperature: item.Temperature,
		MaxTokens:   item.MaxTokens,
	}
	if len(item.APIKeyCipher) > 0 {
		key, err := s.cipher.Decrypt(item.APIKeyCipher)
		if err != nil {
			return nil, fmt.Errorf("chat model %d: %w", item.ID, err)
		}
		resolved.APIKey = key
	}
	return resolved, nil
}

// Touch 记录一次"最近使用"，失败只让调用方记 warn，不影响问答本身。
func (s *Service) Touch(ctx context.Context, id int64) error {
	return s.repo.Touch(ctx, id)
}

// Test 用指定配置真发一条消息。
//
// 配了不等于能用：Key 过期、模型名拼错、代理没放行、余额为零，
// 都只有真发一次请求才知道。它也是保存前的验证手段。
func (s *Service) Test(ctx context.Context, id int64) (*Probe, error) {
	if s.prober == nil {
		return nil, fmt.Errorf("%w: connectivity test is not available", ErrNotConfigured)
	}
	target, err := s.Resolve(ctx, &id)
	if err != nil {
		return nil, err
	}
	probe, err := s.prober(ctx, target)
	if err != nil {
		return nil, err
	}
	// 把"实际测的是哪个模型"一起带回去，前端不必猜。
	probe.Model = target.Model
	return &probe, nil
}

// reload 读回刚写入的行，返回带数据库时间戳的视图。
//
// 插入/更新后直接返回内存里的结构体，CreatedAt/UpdatedAt 会是零值或旧值，
// 界面上的"更新于"就会显示 1970 年。多一次查询换一个不会说错话的响应，值得。
func (s *Service) reload(ctx context.Context, id int64) (*View, error) {
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return viewOf(item), nil
}

func viewOf(item *ChatModel) *View {
	return &View{
		ID:               item.ID,
		Name:             item.Name,
		BaseURL:          item.BaseURL,
		Model:            item.Model,
		APIKeyHint:       item.APIKeyHint,
		APIKeyConfigured: len(item.APIKeyCipher) > 0,
		Temperature:      item.Temperature,
		MaxTokens:        item.MaxTokens,
		IsDefault:        item.IsDefault,
		LastUsedAt:       item.LastUsedAt,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

// normalizeModel 校验并规整三个必填字段。
//
// base_url 去掉尾部斜杠：SDK 拼接路径前本来会 TrimRight，存两种写法
// 会让"看起来一样的两条配置"在界面上难以区分。
func normalizeModel(name, baseURL, model string) (string, string, string, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	model = strings.TrimSpace(model)

	switch {
	case name == "":
		return "", "", "", fmt.Errorf("%w: name is required", ErrInvalidInput)
	case utf8.RuneCountInString(name) > 64:
		return "", "", "", fmt.Errorf("%w: name must be at most 64 characters", ErrInvalidInput)
	case baseURL == "":
		return "", "", "", fmt.Errorf("%w: base_url is required", ErrInvalidInput)
	case !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://"):
		return "", "", "", fmt.Errorf("%w: base_url must start with http:// or https://", ErrInvalidInput)
	case utf8.RuneCountInString(baseURL) > 512:
		return "", "", "", fmt.Errorf("%w: base_url is too long", ErrInvalidInput)
	case model == "":
		return "", "", "", fmt.Errorf("%w: model is required", ErrInvalidInput)
	case utf8.RuneCountInString(model) > 128:
		return "", "", "", fmt.Errorf("%w: model must be at most 128 characters", ErrInvalidInput)
	}
	return name, baseURL, model, nil
}

func validateOptions(temperature *float32, maxTokens *int) error {
	// 0~2 是 OpenAI 兼容接口的常见区间；个别厂商只接受 0~1，
	// 那种越界由厂商返回 400，我们再翻译成可读提示。
	if temperature != nil && (*temperature < 0 || *temperature > 2) {
		return fmt.Errorf("%w: temperature must be between 0 and 2", ErrInvalidInput)
	}
	if maxTokens != nil && *maxTokens <= 0 {
		return fmt.Errorf("%w: max_tokens must be positive", ErrInvalidInput)
	}
	return nil
}

func validateKey(key string) error {
	if utf8.RuneCountInString(key) > 512 {
		return fmt.Errorf("%w: api_key is too long", ErrInvalidInput)
	}
	return nil
}

// hintOf 取 Key 的末 4 位用于界面确认。
//
// 短 Key（<= 8）不显示尾号：那时末四位已经占了信息量的一大半，
// 显示出来反而帮了倒忙。
func hintOf(key string) string {
	if len(key) <= 8 {
		return ""
	}
	return key[len(key)-4:]
}
