package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/luo303/mindos/backend/internal/dispatch"
	"github.com/luo303/mindos/backend/internal/memory"
	"github.com/luo303/mindos/backend/internal/platform"
)

// memoryExtractHandler 解包投递信封，业务规则由 memory 模块处理。
func memoryExtractHandler(extractor *memory.Extractor) dispatch.Handler {
	return func(ctx context.Context, env platform.Envelope) error {
		var event memory.ExtractEvent
		if err := json.Unmarshal(env.Payload, &event); err != nil {
			return fmt.Errorf("decode memory extract event: %w", err)
		}
		return extractor.Run(ctx, event)
	}
}
