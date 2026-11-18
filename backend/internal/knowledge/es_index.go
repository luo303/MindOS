package knowledge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
)

// EnsureSearchIndexes 创建检索读模型的索引与别名（幂等）。
//
// 开发/首次启动调用即可；生产建议用单独的运维命令执行，避免应用账号有过大权限。
// 索引名带 v1，读别名不带版本：重建时建 v2、切别名、删 v1，应用无需改配置。
func EnsureSearchIndexes(ctx context.Context, client *elasticsearch.Client, prefix string) error {
	if client == nil {
		return fmt.Errorf("ensure search indexes: nil client")
	}
	for _, spec := range []struct {
		index string
		alias string
		body  string
	}{
		{index: prefix + "-chunks-v1", alias: prefix + "-chunks", body: chunksIndexBody},
		{index: prefix + "-messages-v1", alias: prefix + "-messages", body: messagesIndexBody},
	} {
		if err := ensureIndex(ctx, client, spec.index, spec.alias, spec.body); err != nil {
			return err
		}
	}
	return nil
}

func ensureIndex(ctx context.Context, client *elasticsearch.Client, index, alias, body string) error {
	exists, err := esRequest(ctx, client, http.MethodHead, "/"+index, "")
	if err != nil {
		return fmt.Errorf("check index %s: %w", index, err)
	}
	if exists.status == http.StatusOK {
		// 索引在；别名可能因为上次创建到一半缺失，补一次。
		return ensureAlias(ctx, client, index, alias)
	}
	if exists.status != http.StatusNotFound {
		return fmt.Errorf("check index %s: %s", index, exists.body)
	}

	created, err := esRequest(ctx, client, http.MethodPut, "/"+index, body)
	if err != nil {
		return fmt.Errorf("create index %s: %w", index, err)
	}
	if created.status >= 300 && !strings.Contains(created.body, "resource_already_exists_exception") {
		return fmt.Errorf("create index %s: %s", index, created.body)
	}
	return ensureAlias(ctx, client, index, alias)
}

func ensureAlias(ctx context.Context, client *elasticsearch.Client, index, alias string) error {
	has, err := esRequest(ctx, client, http.MethodHead, "/_alias/"+alias, "")
	if err != nil {
		return fmt.Errorf("check alias %s: %w", alias, err)
	}
	if has.status == http.StatusOK {
		return nil
	}
	body := fmt.Sprintf(`{"actions":[{"add":{"index":%q,"alias":%q}}]}`, index, alias)
	res, err := esRequest(ctx, client, http.MethodPost, "/_aliases", body)
	if err != nil {
		return fmt.Errorf("create alias %s: %w", alias, err)
	}
	if res.status >= 300 && !strings.Contains(res.body, "alias") {
		return fmt.Errorf("create alias %s: %s", alias, res.body)
	}
	return nil
}

type esResponse struct {
	status int
	body   string
}

func esRequest(ctx context.Context, client *elasticsearch.Client, method, path, body string) (esResponse, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, reader)
	if err != nil {
		return esResponse{}, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Perform(req)
	if err != nil {
		return esResponse{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return esResponse{}, err
	}
	return esResponse{status: res.StatusCode, body: string(raw)}, nil
}

// chunksIndexBody 是知识库分块的读模型。
//
// smartcn 是 Elastic 官方中文分词插件；换成 IK 时只改 analyzer 名称，
// 查询 DSL 与 Go 代码不变。
const chunksIndexBody = `{
  "settings": {
    "number_of_shards": 1,
    "number_of_replicas": 0,
    "refresh_interval": "1s"
  },
  "mappings": {
    "properties": {
      "chunk_id":    { "type": "long" },
      "document_id": { "type": "long" },
      "owner_id":    { "type": "long" },
      "seq":          { "type": "integer" },
      "start_offset": { "type": "integer" },
      "end_offset":   { "type": "integer" },
      "collection":   { "type": "keyword" },
      "title":        { "type": "text", "analyzer": "smartcn", "search_analyzer": "smartcn" },
      "content":     { "type": "text", "analyzer": "smartcn", "search_analyzer": "smartcn" },
      "heading":     { "type": "text", "analyzer": "smartcn" },
      "tags":        { "type": "keyword" },
      "status":      { "type": "keyword" },
      "embed_model": { "type": "keyword" },
      "embedding":   { "type": "dense_vector", "dims": 1024, "index": true, "similarity": "cosine" },
      "updated_at":  { "type": "date" }
    }
  }
}`

// messagesIndexBody 是会话消息的读模型，供冷层召回使用。
const messagesIndexBody = `{
  "settings": {
    "number_of_shards": 1,
    "number_of_replicas": 0,
    "refresh_interval": "1s"
  },
  "mappings": {
    "properties": {
      "message_id":      { "type": "long" },
      "conversation_id": { "type": "long" },
      "owner_id":        { "type": "long" },
      "role":            { "type": "keyword" },
      "content":         { "type": "text", "analyzer": "smartcn", "search_analyzer": "smartcn" },
      "seq":             { "type": "integer" },
      "embedding":       { "type": "dense_vector", "dims": 1024, "index": true, "similarity": "cosine" },
      "created_at":      { "type": "date" }
    }
  }
}`
