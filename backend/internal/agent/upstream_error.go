package agent

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	openai "github.com/meguminnnnnnnnn/go-openai"
)

var vendorSecretPattern = regexp.MustCompile(`(?i)(sk-|bearer\s+|api[_-]?key[=: ]+)[A-Za-z0-9_\-]{8,}`)

// UpstreamErrorInfo 从错误链里提取模型厂商返回的 HTTP 状态和 message。
//
// 只读取结构化字段，不回传原始响应体：厂商 body 里可能带请求参数或内部信息。
func UpstreamErrorInfo(err error) (status int, message string, ok bool) {
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatusCode, cleanVendorMessage(apiErr.Message), true
	}

	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		status = reqErr.HTTPStatusCode
		if len(reqErr.Body) > 0 {
			var resp openai.ErrorResponse
			if json.Unmarshal(reqErr.Body, &resp) == nil && resp.Error != nil {
				message = cleanVendorMessage(resp.Error.Message)
			}
		}
		return status, message, true
	}
	return 0, "", false
}

func cleanVendorMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	message = vendorSecretPattern.ReplaceAllString(message, "$1***")
	if message == "" {
		return ""
	}
	const max = 300
	runes := []rune(message)
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return message
}
