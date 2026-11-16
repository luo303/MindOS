package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/luo303/mindos/backend/internal/chatmodel"
)

const (
	// defaultModelTimeout 是单次模型调用的兜底超时。
	//
	// 必须有：推理型模型偶尔会卡住几十秒，没有超时就会把整个问答拖到
	// HTTP 连接断开为止，而那时用户已经不知道发生了什么。
	defaultModelTimeout = 60 * time.Second

	// probeTimeout 是连通性测试的超时，比正常调用短：
	// 测试的目的是"能不能通"，不是"能不能生成一篇长文"。
	probeTimeout = 30 * time.Second

	// probeReplyRunes 是测试接口回显的最大长度。
	probeReplyRunes = 120
)

// ChatModelConfig 是构造对话模型需要的全部参数。
//
// 它刻意不是 chatmodel.Resolved：那一层带 ID、名字等管理字段，
// 这一层只需要"怎么发请求"。转换写在 FromResolved 里。
type ChatModelConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	Temperature *float32
	MaxTokens   *int
	Timeout     time.Duration
}

// NewChatModel 按 OpenAI 兼容协议构造模型客户端。
//
// BaseURL 必须填到版本号为止（例如 https://.../v2）：底层 SDK 的 fullURL()
// 自己会拼 /chat/completions，填全路径会拼成 .../chat/completions/chat/completions，
// 表现是 404，而不是报"协议不对"。
func NewChatModel(ctx context.Context, cfg ChatModelConfig) (model.BaseModel[*schema.Message], error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultModelTimeout
	}
	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL:     cfg.BaseURL,
		APIKey:      cfg.APIKey,
		Model:       cfg.Model,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
		Timeout:     timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("new chat model: %w", err)
	}
	return cm, nil
}

// FromResolved 把一条已解密的配置翻成构造参数。
//
// 明文 Key 只在这一步出现一次，之后交给 SDK；不落库、不进日志。
func FromResolved(target *chatmodel.Resolved, timeout time.Duration) ChatModelConfig {
	return ChatModelConfig{
		BaseURL:     target.BaseURL,
		APIKey:      target.APIKey,
		Model:       target.Model,
		Temperature: target.Temperature,
		MaxTokens:   target.MaxTokens,
		Timeout:     timeout,
	}
}

// Probe 实现 chatmodel.Prober：用给定配置真发一条消息。
//
// 为什么复用真实调用路径而不是单独写一个 HTTP 探针：探针能过、真实调用失败
// 是最没用的测试结果。这里走的就是问答用的同一个客户端。
func Probe(ctx context.Context, target *chatmodel.Resolved) (chatmodel.Probe, error) {
	start := time.Now()

	timeout := probeTimeout
	cfg := FromResolved(target, timeout)
	chatModel, err := NewChatModel(ctx, cfg)
	if err != nil {
		return chatmodel.Probe{}, err
	}

	msg, err := chatModel.Generate(ctx, []*schema.Message{
		schema.UserMessage("用一句中文回答：你是什么模型？"),
	})
	if err != nil {
		return chatmodel.Probe{}, fmt.Errorf("call model: %w", err)
	}

	return chatmodel.Probe{
		Reply:   truncateRunes(msg.Content, probeReplyRunes),
		Latency: time.Since(start),
	}, nil
}
