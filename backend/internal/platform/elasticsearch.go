package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/luo303/mindos/backend/internal/config"
)

// NewElasticsearch 建立检索读模型的客户端，并在启动时 Ping 一次。
//
// ES 不是事实库：连不上时 search 工具会失败，但 PG/SQL 工具仍可用。
// 是否使用 ES 由 SEARCH_BACKEND 决定；native 模式不会走到这里。
func NewElasticsearch(ctx context.Context, cfg config.ElasticConfig) (*elasticsearch.Client, error) {
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: cfg.URLs,
		Username:  cfg.Username,
		Password:  cfg.Password,
	})
	if err != nil {
		return nil, fmt.Errorf("new elasticsearch client: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := client.Ping(client.Ping.WithContext(pingCtx))
	if err != nil {
		return nil, fmt.Errorf("ping elasticsearch: %w", err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, fmt.Errorf("ping elasticsearch: %s", res.String())
	}
	return client, nil
}
