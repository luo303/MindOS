// 本文件是向量模型客户端：把一批文本换成一批向量。
//
// 边界：platform 只管协议与错误分类，不认识"文档""分块"；
// 失败时返回的是传输层语义（凭据错 / 请求被拒 / 暂时不可用），
// 由知识模块翻译成自己的哨兵错误与错误码。
//
// 只支持 OpenAI 兼容的 POST {base}/embeddings。本项目不写厂商适配器：
// 换厂商就是换 base + key + model 三个变量。
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 向量化客户端的默认值。Timeout 与 BatchSize 可由配置覆盖，
// 这里的值只是 NewEmbedder 收到零值时兜底，避免"0 秒超时"这种必然失败的配置。
const (
	defaultEmbeddingTimeout   = 60 * time.Second
	defaultEmbeddingBatchSize = 10
	// maxEmbeddingResponseBytes 限制响应体积：1024 维 × float32 转 JSON 约 10KB/条，
	// 一批 64 条接近 1MB。设 32MB 是给"厂商返回了多余字段"留的余量，
	// 不是正常情况下的预期大小。
	maxEmbeddingResponseBytes = 32 << 20
)

// 向量化的传输层错误分类。
//
// 分成这三类是因为上层的处理方式完全不同：
//   - Unauthorized：Key 错了，重试一百次也一样；
//   - Rejected：请求本身有问题（模型名错、输入过长），改配置才能好；
//   - Unavailable：限流、超时、5xx，等一会儿或重试就好。
var (
	ErrEmbeddingUnauthorized = errors.New("embedding provider rejected the credentials")
	ErrEmbeddingRejected     = errors.New("embedding provider rejected the request")
	ErrEmbeddingUnavailable  = errors.New("embedding provider is unavailable")
	// ErrEmbeddingDimension 表示返回向量长度与预期不符。单独一类是因为
	// 它既不重试也没法靠改 Key 解决：要么换模型，要么改数据库列。
	ErrEmbeddingDimension = errors.New("embedding dimension mismatch")
)

// EmbeddingOptions 是构造客户端的参数。三个 string 必须非空，
// 由调用方（cmd/server）保证——它只在配置齐全时才创建客户端。
type EmbeddingOptions struct {
	APIBase    string
	APIKey     string
	Model      string
	Dimensions int
	Timeout    time.Duration
	BatchSize  int
}

// Embedder 是 OpenAI 兼容的向量模型客户端。
//
// 【为什么持有 *http.Client 而不是每次新建】
// http.Client 内部带连接池；每次新建等于每次重新握手 TLS，
// 一批文档几十次请求下来，握手开销比推理还大。
type Embedder struct {
	apiBase    string
	apiKey     string
	model      string
	dimensions int
	batchSize  int
	client     *http.Client
}

// NewEmbedder 构造客户端。参数不全或维度非正数时返回错误，
// 让启动阶段就失败，而不是等到第一个文档才 401。
func NewEmbedder(opts EmbeddingOptions) (*Embedder, error) {
	if strings.TrimSpace(opts.APIBase) == "" {
		return nil, errors.New("embedding: api base is required")
	}
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, errors.New("embedding: api key is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, errors.New("embedding: model is required")
	}
	if opts.Dimensions <= 0 {
		return nil, errors.New("embedding: dimensions must be greater than 0")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultEmbeddingTimeout
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = defaultEmbeddingBatchSize
	}
	return &Embedder{
		apiBase:    strings.TrimRight(opts.APIBase, "/"),
		apiKey:     opts.APIKey,
		model:      opts.Model,
		dimensions: opts.Dimensions,
		batchSize:  batch,
		// Timeout 覆盖"连接 + 发送 + 等响应 + 读完 body"整个过程。
		// 单独设 DialContext 超时只管建连，读一个慢响应照样能挂死。
		client: &http.Client{Timeout: timeout},
	}, nil
}

// Model 返回当前模型名，用于写进 document_chunks.embed_model。
func (e *Embedder) Model() string { return e.model }

// Dimensions 返回期望的向量维度，用于和数据库列的维度对齐。
func (e *Embedder) Dimensions() int { return e.dimensions }

// Embed 把一批文本换成等长同序的向量，并返回本次消耗的 token 数。
//
// 内部分批：厂商对单请求条数有上限（百炼 10 条），一次全发会被拒。
// 任一批失败就整体失败并返回已消耗的 token——调用方据此决定是否整篇重算。
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, int64, error) {
	if len(texts) == 0 {
		return nil, 0, nil
	}
	vectors := make([][]float32, 0, len(texts))
	var tokens int64
	for start := 0; start < len(texts); start += e.batchSize {
		end := min(start+e.batchSize, len(texts))
		batch, batchTokens, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, tokens, fmt.Errorf("embed texts [%d,%d): %w", start, end, err)
		}
		vectors = append(vectors, batch...)
		tokens += batchTokens
	}
	return vectors, tokens, nil
}

// embeddingRequest 是 OpenAI /embeddings 的请求体。
//
// 只声明 model 与 input 两个字段。
// 不写 temperature（与向量无关）、也不写 encoding_format：前者纯属噪音，
// 后者虽然 OpenAI 认，但兼容实现不一定——默认就是 float，少发一个字段就少一类 400。
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingResponse 只取需要的字段。data 的 index 不一定按输入顺序返回
// （并行实现的常见行为），所以下面按 index 回填而不是直接用返回顺序。
type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int64 `json:"prompt_tokens"`
	} `json:"usage"`
}

// embedBatch 发一批。返回的向量与输入顺序一致。
func (e *Embedder) embedBatch(ctx context.Context, texts []string) ([][]float32, int64, error) {
	payload, err := json.Marshal(embeddingRequest{
		Model: e.model,
		Input: texts,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("marshal embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apiBase+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("build embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		// 网络错误一律归为"暂时不可用"：连接被拒、DNS、超时都可能是一过性的。
		// 唯一例外是 ctx 被取消，它会被上层原样带出去，不会变成"重试"。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, ctxErr
		}
		return nil, 0, fmt.Errorf("%w: %v", ErrEmbeddingUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEmbeddingResponseBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: read response: %v", ErrEmbeddingUnavailable, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, 0, classifyStatus(resp.StatusCode, body)
	}

	var parsed embeddingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, 0, fmt.Errorf("%w: decode response: %v", ErrEmbeddingRejected, err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, 0, fmt.Errorf("%w: asked %d texts but got %d vectors",
			ErrEmbeddingRejected, len(texts), len(parsed.Data))
	}

	// 按 index 回填：不能假设返回顺序等于输入顺序。
	out := make([][]float32, len(texts))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(texts) {
			return nil, 0, fmt.Errorf("%w: vector index %d out of range",
				ErrEmbeddingRejected, item.Index)
		}
		if len(item.Embedding) != e.dimensions {
			return nil, 0, fmt.Errorf("%w: model %s returned %d dimensions, expected %d",
				ErrEmbeddingDimension, e.model, len(item.Embedding), e.dimensions)
		}
		if out[item.Index] != nil {
			return nil, 0, fmt.Errorf("%w: duplicated vector index %d",
				ErrEmbeddingRejected, item.Index)
		}
		out[item.Index] = item.Embedding
	}
	for i := range out {
		if out[i] == nil {
			return nil, 0, fmt.Errorf("%w: missing vector for input index %d",
				ErrEmbeddingRejected, i)
		}
	}
	return out, parsed.Usage.PromptTokens, nil
}

// classifyStatus 把 HTTP 状态码翻成三类错误。
//
// 响应体只取前 300 字节拼进错误：厂商的错误信息对排查很有用
// （"model not found" 和 "input too long" 的处理方式完全不同），
// 但不能整段塞进日志与任务表。
func classifyStatus(status int, body []byte) error {
	detail := strings.TrimSpace(string(body))
	if len(detail) > 300 {
		detail = detail[:300] + "..."
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: status %d: %s", ErrEmbeddingUnauthorized, status, detail)
	case status == http.StatusTooManyRequests || status >= 500:
		return fmt.Errorf("%w: status %d: %s", ErrEmbeddingUnavailable, status, detail)
	default:
		return fmt.Errorf("%w: status %d: %s", ErrEmbeddingRejected, status, detail)
	}
}

// ProbeResult 是一次连通性测试的结果，给设置页展示。
type ProbeResult struct {
	Model      string
	Dimensions int
	Latency    time.Duration
}

// Probe 发一条短文本验证"Key 能用、模型存在、维度对得上"。
//
// 为什么必须真调一次：配置三项都填了不代表能用——Key 过期、模型名拼错、
// 代理没开白名单，都只有发请求才知道。用一句固定短文本，成本可忽略。
func (e *Embedder) Probe(ctx context.Context) (ProbeResult, error) {
	start := time.Now()
	vectors, _, err := e.embedBatch(ctx, []string{"mindos embedding probe"})
	if err != nil {
		return ProbeResult{}, err
	}
	return ProbeResult{
		Model:      e.model,
		Dimensions: len(vectors[0]),
		Latency:    time.Since(start),
	}, nil
}
