package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEmbedding 起一个假的 /embeddings 服务。
//
// 不拿真实模型做单测（第 1 节约定 6）：真实模型会变、会限流、会收钱，
// 断言它的输出等于把测试绑在一个外部系统上。这里只验证协议与错误分类。
func fakeEmbedding(t *testing.T, dim int, status int, requests *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			atomic.AddInt32(requests, 1)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %s, want /embeddings", r.URL.Path)
		}
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		// 故意倒序返回：很多并行实现都会这样，客户端必须按 index 回填。
		data := make([]map[string]any, 0, len(req.Input))
		for i := len(req.Input) - 1; i >= 0; i-- {
			vec := make([]float32, dim)
			vec[0] = float32(i) // 用首元素标记来源下标，便于断言没有错位
			data = append(data, map[string]any{"index": i, "embedding": vec})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  data,
			"usage": map[string]any{"prompt_tokens": len(req.Input) * 7},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testEmbedder(t *testing.T, baseURL string, batch int) *Embedder {
	t.Helper()
	e, err := NewEmbedder(EmbeddingOptions{
		APIBase:    baseURL,
		APIKey:     "test-key",
		Model:      "fake-embed",
		Dimensions: 3,
		BatchSize:  batch,
		Timeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	return e
}

// 3 条文本、批大小 2 → 两次请求；返回顺序打乱也要按 index 落对位置。
func TestEmbedBatchesAndKeepsInputOrder(t *testing.T) {
	var requests int32
	srv := fakeEmbedding(t, 3, http.StatusOK, &requests)
	e := testEmbedder(t, srv.URL, 2)

	vectors, tokens, err := e.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Errorf("requests = %d, want 2 (3 texts / batch 2)", got)
	}
	if len(vectors) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vectors))
	}
	// 假服务把"批内下标"写进首元素：第 1 批是 0、1，第 2 批只有一条所以是 0。
	// 值对不上就说明按 index 回填的逻辑错了。
	wantFirst := []float32{0, 1, 0}
	for i, v := range vectors {
		if v[0] != wantFirst[i] {
			t.Errorf("vector[%d][0] = %v, want %v", i, v[0], wantFirst[i])
		}
	}
	if tokens != 21 {
		t.Errorf("tokens = %d, want 21 (3 texts x 7)", tokens)
	}
}

// 请求体只带 model 与 input：兼容实现对着多余字段报 400 的情况真实存在
// （硅基流动、部分自建网关都遇到过），少发一个字段就少一类故障。
func TestEmbedRequestCarriesOnlyModelAndInput(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"index": 0, "embedding": []float32{1, 0, 0}}},
			"usage": map[string]any{"prompt_tokens": 3},
		})
	}))
	t.Cleanup(srv.Close)

	e := testEmbedder(t, srv.URL, 2)
	if _, _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(req) != 2 {
		t.Errorf("request fields = %v, want only model and input", req)
	}
	if _, ok := req["encoding_format"]; ok {
		t.Error("encoding_format should not be sent; default is float and some providers reject it")
	}
}

func TestEmbedEmptyInputDoesNotCallProvider(t *testing.T) {
	var requests int32
	srv := fakeEmbedding(t, 3, http.StatusOK, &requests)
	e := testEmbedder(t, srv.URL, 2)

	vectors, tokens, err := e.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil): %v", err)
	}
	if vectors != nil || tokens != 0 {
		t.Errorf("vectors/tokens = %v/%d, want nil/0", vectors, tokens)
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Errorf("requests = %d, want 0 for empty input", n)
	}
}

// 维度不符是最容易"看起来成功"的失败：向量写进 vector(1024) 列会直接报错，
// 但错误信息埋得很深，所以提前判出来并单独分类。
func TestEmbedDimensionMismatch(t *testing.T) {
	srv := fakeEmbedding(t, 2, http.StatusOK, nil)
	e := testEmbedder(t, srv.URL, 2)

	_, _, err := e.Embed(context.Background(), []string{"a"})
	if !errors.Is(err, ErrEmbeddingDimension) {
		t.Fatalf("err = %v, want ErrEmbeddingDimension", err)
	}
}

func TestEmbedErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrEmbeddingUnauthorized},
		{http.StatusForbidden, ErrEmbeddingUnauthorized},
		{http.StatusBadRequest, ErrEmbeddingRejected},
		{http.StatusTooManyRequests, ErrEmbeddingUnavailable},
		{http.StatusInternalServerError, ErrEmbeddingUnavailable},
		{http.StatusBadGateway, ErrEmbeddingUnavailable},
	}
	for _, tc := range cases {
		srv := fakeEmbedding(t, 3, tc.status, nil)
		e := testEmbedder(t, srv.URL, 2)
		_, _, err := e.Embed(context.Background(), []string{"a"})
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}
}

// 连不上属于"暂时不可用"：这类错误值得重试，不该判成永久失败。
func TestEmbedConnectionFailureIsUnavailable(t *testing.T) {
	e := testEmbedder(t, "http://127.0.0.1:1", 2)
	_, _, err := e.Embed(context.Background(), []string{"a"})
	if !errors.Is(err, ErrEmbeddingUnavailable) {
		t.Fatalf("err = %v, want ErrEmbeddingUnavailable", err)
	}
}

func TestProbeReturnsModelAndDimensions(t *testing.T) {
	srv := fakeEmbedding(t, 3, http.StatusOK, nil)
	e := testEmbedder(t, srv.URL, 2)

	res, err := e.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Model != "fake-embed" || res.Dimensions != 3 {
		t.Errorf("probe = %+v, want fake-embed/3", res)
	}
}

func TestNewEmbedderValidatesRequiredOptions(t *testing.T) {
	cases := []EmbeddingOptions{
		{APIKey: "k", Model: "m", Dimensions: 3},          // 缺 base
		{APIBase: "http://x", Model: "m", Dimensions: 3},  // 缺 key
		{APIBase: "http://x", APIKey: "k", Dimensions: 3}, // 缺 model
		{APIBase: "http://x", APIKey: "k", Model: "m"},    // 缺维度
	}
	for i, opts := range cases {
		if _, err := NewEmbedder(opts); err == nil {
			t.Errorf("case %d: expected an error for incomplete options", i)
		}
	}
}

// 假服务的错误体很长时只截前 300 字节：错误会进任务表与日志，
// 不能把整段 HTML 错误页塞进去。
func TestClassifyStatusTruncatesBody(t *testing.T) {
	body := make([]byte, 600)
	for i := range body {
		body[i] = 'x'
	}
	err := classifyStatus(http.StatusBadRequest, body)
	if !errors.Is(err, ErrEmbeddingRejected) {
		t.Fatalf("err = %v, want ErrEmbeddingRejected", err)
	}
	if len(err.Error()) > 400 {
		t.Errorf("error message too long: %d bytes", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error %q should mention the status code", err.Error())
	}
}
