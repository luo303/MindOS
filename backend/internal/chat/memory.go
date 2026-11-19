package chat

import "unicode"

// 会话记忆相关的 outbox topic。
//
// 消息写入时同时排两条事件：一条先把正文写进 ES（关键词可立即召回），
// 一条异步算 embedding 后重写同一条（补上向量）。两条都幂等。
const (
	SearchMessageUpsert      = "search.message.upsert"
	SearchMessageEmbed       = "search.message.embed"
	SearchMessageDelete      = "search.message.delete"
	SearchConversationDelete = "search.conversation.delete"
)

// estimateTokens 是上下文预算用的启发式估算。
//
// CJK 字符约 1 token/字，非 CJK 约 4 字符/token；实际 usage 由模型返回，
// 这里只用来决定"带多少历史"，不用于计费。
func estimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
			continue
		}
		other++
	}
	return cjk + other/4
}
