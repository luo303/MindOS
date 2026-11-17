package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	// snippetRunes 是给模型的单条片段长度上限。
	//
	// 600 与切分粒度对齐：分块本身约 600 字，再截一次是为了防止
	// 早期文档用了更大粒度时把某一轮上下文直接撑爆。
	snippetRunes = 600

	// maxToolLimit 是工具一次最多返回几条。
	// 给模型留了调大的余地，但上限压住：条数越多，越容易挤掉它自己的推理空间。
	maxToolLimit = 10
)

// ErrToolCallLimit 表示本次问答的工具调用次数用尽。
//
// 它作为工具错误返回给模型（而不是中止 run）：模型看到"工具失败"通常会
// 收手并用已有片段作答，这比直接掐断更接近人想要的结果。
var ErrToolCallLimit = errors.New("tool call limit reached")

// searchInput 是 search_knowledge 的参数结构。
//
// json 与 jsonschema 两个 tag 都要写：前者决定解码时的字段名，
// 后者是给模型看的参数说明——没有说明的字段，模型经常会不填或乱填。
type searchInput struct {
	Query string `json:"query" jsonschema:"description=检索词，尽量用原文里的关键词，不要写整句问题"`
	Limit int    `json:"limit" jsonschema:"description=返回条数 1-10，默认 5"`
}

// searchOutput 是单个片段对模型的呈现。
//
// 只给模型需要的东西：编号、标题、段落序号、正文。
// 相似度不给——模型会拿它当"可信度"来判断，而分数高低不代表内容对不对。
type searchOutput struct {
	N       int    `json:"n"`
	Title   string `json:"title"`
	Section int    `json:"section"`
	Content string `json:"content"`
}

// newSearchTool 构造 search_knowledge 工具。
//
// 它只做三件事：限流、调 Retriever、把结果登记进 registry 并回给模型。
// 不碰 repository、不拼 SQL、不认识文档状态——那些都在 knowledge 层。
func newSearchTool(retriever Retriever, ownerID int64, registry *Registry, maxCalls int) (tool.InvokableTool, error) {
	var calls atomic.Int32

	invoke := func(ctx context.Context, in searchInput) ([]searchOutput, error) {
		// 先计数再判断：并行的两个调用都抢同一个计数器，
		// 用原子加保证"最多 maxCalls 次"是硬上限，而不是大致上限。
		if int(calls.Add(1)) > maxCalls {
			return nil, fmt.Errorf("%w (max %d)", ErrToolCallLimit, maxCalls)
		}
		query := strings.TrimSpace(in.Query)
		if query == "" {
			// 空查询直接拒掉，不要浪费一次向量化 + 两路 SQL。
			return nil, errors.New("query is required")
		}
		limit := in.Limit
		if limit <= 0 || limit > maxToolLimit {
			limit = 5
		}

		hits, err := retriever.Search(ctx, ownerID, query, limit)
		if err != nil {
			return nil, err
		}
		if len(hits) == 0 {
			// 返回空数组而不是错误：检索为空是正常结果，
			// 报错会让模型以为工具坏了，转而用编造的内容填坑。
			return []searchOutput{}, nil
		}

		numbered := registry.Register(hits)
		out := make([]searchOutput, 0, len(numbered))
		for _, item := range numbered {
			out = append(out, searchOutput{
				N:       item.N,
				Title:   item.Title,
				Section: item.Seq,
				Content: truncateRunes(item.Content, snippetRunes),
			})
		}
		return out, nil
	}

	return utils.InferTool("search_knowledge", "在个人知识库中检索片段，返回带编号的原文", invoke)
}

// truncateRunes 按码点截断，不按字节。
//
// 按字节截会把一个汉字切成两半，产生非法 UTF-8——JSON 编码时变成 \ufffd，
// 模型看到一串问号，会以为这段内容损坏了。
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
