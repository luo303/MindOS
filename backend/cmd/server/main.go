// MindOS 服务入口。
//
// 这里的职责只有"组装"：读配置 → 建依赖 → 建 router → 启 server → 等退出信号 → 按序关闭。
// 业务代码不在这里出现，这样 entry 永远保持短小、容易阅读。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/agent"
	"github.com/luo303/mindos/backend/internal/approval"
	"github.com/luo303/mindos/backend/internal/auth"
	"github.com/luo303/mindos/backend/internal/chat"
	"github.com/luo303/mindos/backend/internal/chatmodel"
	"github.com/luo303/mindos/backend/internal/config"
	"github.com/luo303/mindos/backend/internal/dispatch"
	"github.com/luo303/mindos/backend/internal/handler"
	"github.com/luo303/mindos/backend/internal/knowledge"
	"github.com/luo303/mindos/backend/internal/logging"
	"github.com/luo303/mindos/backend/internal/memory"
	"github.com/luo303/mindos/backend/internal/middleware"
	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/outbox"
	"github.com/luo303/mindos/backend/internal/platform"
	"github.com/luo303/mindos/backend/internal/reaper"
	"github.com/luo303/mindos/backend/internal/router"
	"github.com/luo303/mindos/backend/internal/task"
	"github.com/luo303/mindos/backend/internal/todo"
	"github.com/luo303/mindos/backend/migrations"
)

// version 可以在编译时注入：go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

// workerIDFromProcess 生成"谁在跑任务"的标识：主机名 + 进程号。
//
// 为什么不用随机 UUID：出问题时要在日志和 jobs.worker_id 里对得上，
// 主机名 + pid 能直接告诉你去哪台机器、哪个进程看；重启后 pid 变化也符合预期
// （上一任留下的任务会由补扫收回，不会认错人）。
func workerIDFromProcess() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "mindos"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// newEngine 装配全局中间件并返回 HTTP 引擎。
//
// 单独成函数不是为了抽象，而是为了让 main_test.go 能验证真实装配顺序：
// 如果测试自己再写一遍 Use(...)，即使 main.go 的顺序被改坏，测试也发现不了。
//
// 【为什么不用 gin.Default()】
// 它的源码就是 New() + Use(Logger(), Recovery())；内置 Logger 只往 stdout
// 打纯文本，Recovery 也不使用我们的 zap 日志与统一响应。这里照同样的标准姿势
// 展开，但换成项目自己的实现。
//
// 【中间件顺序：Logger 必须在 Recovery 外层】
// 请求表现为洋葱模型：外层中间件的前置代码先执行，内层 Handler 结束后再反向
// 执行后置代码。Logger 的访问日志写在 c.Next() 之后，必须等内层执行完才能读到
// 最终状态码与耗时。若 Recovery 在外层，Handler panic 会沿着调用栈直接跳过
// Logger 的后续代码，导致这次失败请求没有访问日志；把 Recovery 放进 Logger
// 内层，它先接住 panic 并写完 500，Logger 的 c.Next() 就能正常返回并记录日志。
func newEngine(log *zap.Logger, isDev bool) *gin.Engine {
	engine := gin.New()
	engine.Use(
		middleware.RequestID(),
		middleware.Logger(log),
		middleware.Recovery(log, isDev),
	)
	return engine
}

func main() {
	// run 返回 error，main 只负责打印和退出码。
	// 这样所有资源清理都能用 defer 完成，而不需要在多处 os.Exit（os.Exit 不会执行 defer）。
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mindos: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. 配置：失败即退出，不带着错误配置启动。
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 2. 日志：越早越好，后续所有初始化失败都要能打日志。
	log, err := logging.New(cfg.Log)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	log.Info("starting mindos",
		zap.String("version", version),
		zap.String("env", cfg.App.Env),
		zap.String("addr", cfg.App.HTTPAddr),
	)

	// 3. 根 context：收到 SIGINT/SIGTERM 时被取消，所有后台任务都挂在它下面。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4. 基础设施连接。
	db, err := platform.NewPostgres(ctx, cfg.DB, log)
	if err != nil {
		return err // NewPostgres 已经描述了是哪个环节失败
	}
	defer func() {
		if err := platform.ClosePostgres(db); err != nil {
			log.Warn("close postgres", zap.Error(err))
		}
	}()

	// 只读 SQL 工具是可选的：配了 AI_DB_USER 才建独立池，并在启动时自检角色。
	// 没配就不注册 query_knowledge，问答只挂检索——这是默认形态。
	var sqlExecutor *agent.SQLExecutor
	if cfg.AI.Enabled() {
		aiPool, err := platform.NewReadOnlyPool(ctx, cfg.DB, cfg.AI.DBUser, cfg.AI.DBPassword)
		if err != nil {
			return err
		}
		defer func() { _ = aiPool.Close() }()

		if err := agent.CheckAIReader(ctx, aiPool); err != nil {
			return err
		}
		sqlExecutor = agent.NewSQLExecutor(aiPool)
		log.Info("sql query tool enabled", zap.String("db_user", cfg.AI.DBUser))
	} else {
		log.Info("sql query tool disabled: AI_DB_USER not set")
	}

	// 检索读模型是可选的：SEARCH_BACKEND=es 时连 ES 并确保索引/别名存在。
	// 现在只建基础设施；search_knowledge 的 ES 实现随后接入。
	var esClient *elasticsearch.Client
	var searchIndexer *knowledge.SearchIndexer
	if cfg.Elastic.Enabled() {
		var err error
		esClient, err = platform.NewElasticsearch(ctx, cfg.Elastic)
		if err != nil {
			return err
		}
		if err := knowledge.EnsureSearchIndexes(ctx, esClient, cfg.Elastic.IndexPrefix); err != nil {
			return err
		}
		searchIndexer = knowledge.NewSearchIndexer(db, esClient, cfg.Elastic.IndexPrefix, log.Named("search"))
		log.Info("elasticsearch ready", zap.Strings("urls", cfg.Elastic.URLs), zap.String("prefix", cfg.Elastic.IndexPrefix))
	} else {
		log.Info("search backend: native postgres")
	}

	// 5. HTTP 组件装配。
	health := handler.NewHealthHandler(db, cfg.DB.PingTimeout, version)

	// 迁移诊断依赖底层 *sql.DB（不是 GORM 会话）：migrate 包面向 database/sql
	// 设计，与具体 ORM 解耦。server 自己不执行迁移，只读取版本做诊断。
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB: %w", err)
	}
	runner := migrate.New(sqlDB, migrations.FS)
	if st, err := runner.Version(context.Background()); err != nil {
		log.Warn("query migration version", zap.Error(err))
	} else {
		log.Info("migration status",
			zap.Int("version", st.Version),
			zap.Bool("dirty", st.Dirty))
	}

	// 文档原文存储：只有 MinIO 一种实现，没有本地目录备选。
	// 启动时就建连并确保 bucket 存在：端点不通、凭据不对属于"部署错了"，
	// 要让服务直接起不来，而不是等用户第一次上传才看到 500。
	store, err := platform.NewMinIOStore(ctx, cfg.Storage.MinIO)
	if err != nil {
		return err
	}
	log.Info("document storage ready",
		zap.String("endpoint", store.Endpoint()),
		zap.String("bucket", store.Bucket()),
	)

	// 任务队列：连不上**不阻止启动**。
	//
	// 与 MinIO 的处理不同，理由在于职责：队列只是"叫醒"，任务状态在 jobs 表里，
	// 连不上就退化成轮询（Phase 6 的做法）。MQ 维护期间服务照常可用，
	// 只是任务延迟从毫秒回到 500ms——这比"MQ 挂了整个服务起不来"合理。
	rabbit, err := platform.OpenRabbit(platform.RabbitOptions{
		URL:      cfg.Rabbit.URL(),
		Queue:    cfg.Rabbit.Queue,
		Prefetch: cfg.Rabbit.Prefetch,
	}, log.Named("rabbit"))
	if err != nil {
		log.Warn("rabbitmq unavailable, task delivery falls back to sweeper", zap.Error(err))
		rabbit = nil
	} else {
		log.Info("job queue ready",
			zap.String("queue", cfg.Rabbit.Queue), zap.Int("prefetch", cfg.Rabbit.Prefetch))
		// 注册在这里、worker 之后才执行：defer 后进先出，
		// 意味着先停 worker（把任务收尾），再关连接。
		defer func() { _ = rabbit.Close() }()
	}

	// —— 向量模型 ——
	//
	// 两个独立判断：
	//   1. 列维度自检——无论配没配模型都要做。schema 与代码常量不一致时，
	//      每次写入都会失败，而且错误会被包成一句看不出原因的 process_failed。
	//   2. 建客户端——三件配置齐全才建；未配置是合法状态，
	//      导入照常、向量化跳过（状态 skipped），不影响现有功能。
	columnType, err := platform.VectorColumnType(ctx, db, "document_chunks", "embedding")
	if err != nil {
		return err
	}
	wantType := fmt.Sprintf("vector(%d)", knowledge.EmbeddingDimensions)
	if columnType != wantType {
		return fmt.Errorf(
			"vector column mismatch: document_chunks.embedding is %s, code expects %s; "+
				"run `go run ./cmd/migrate up`, or change knowledge.EmbeddingDimensions together with a migration",
			columnType, wantType)
	}

	var embedder *platform.Embedder
	var embedding knowledge.Embedder
	if cfg.IsEmbeddingConfigured() {
		embedder, err = platform.NewEmbedder(platform.EmbeddingOptions{
			APIBase:    cfg.Embedding.APIBase,
			APIKey:     cfg.Embedding.APIKey,
			Model:      cfg.Embedding.Model,
			Dimensions: knowledge.EmbeddingDimensions,
			Timeout:    cfg.Embedding.Timeout,
			BatchSize:  cfg.Embedding.BatchSize,
		})
		if err != nil {
			return err
		}
		// 只有确认非 nil 才装进接口：直接 `embedding = embedder` 在 embedder
		// 为 nil 时会得到一个"非 nil 的接口"，之后 s.embedder == nil 判断失效。
		embedding = embedder
		log.Info("embedding model ready",
			zap.String("model", cfg.Embedding.Model),
			zap.Int("dimensions", knowledge.EmbeddingDimensions),
			zap.Int("batch_size", cfg.Embedding.BatchSize),
			zap.Duration("timeout", cfg.Embedding.Timeout),
		)
	} else {
		log.Info("embedding model not configured, documents will be chunked without vectors")
	}

	system := handler.NewSystemHandler(
		version,
		func(ctx context.Context) error {
			return platform.DBPing(ctx, cfg.DB.PingTimeout, db)
		},
		func(ctx context.Context) (bool, error) {
			return platform.VectorExtensionPresent(ctx, db)
		},
		func(ctx context.Context) (handler.MigrationInfo, error) {
			st, err := runner.Version(ctx)
			if err != nil {
				return handler.MigrationInfo{}, err
			}
			latest, err := runner.Latest()
			if err != nil {
				return handler.MigrationInfo{}, err
			}
			return handler.MigrationInfo{
				Current: st.Version,
				Latest:  latest,
				Dirty:   st.Dirty,
			}, nil
		},
		// 运行期复查 MinIO：启动成功不代表一直可用，
		// 容器被 OOM、卷被卸载、网络分区都会让它中途变坏。
		store.Health,
	)
	settings := handler.NewSettingsHandler(cfg, embeddingProbe(embedder))

	// —— 账号与会话 ——
	//
	// 装配位置放在业务模块之前：知识、问答、模型配置都要从中间件拿到的
	// Principal 取 ownerID，auth 是它们的前置依赖。
	// Secure 只在 prod 打开：本地 http 开发时带 Secure 的 Cookie 会被浏览器丢掉。
	authRepo := auth.NewRepository(db)
	authSvc := auth.NewService(authRepo, cfg.Auth.SessionTTL, cfg.Auth.RegistrationOpen(), log.Named("auth"))
	authHandler := auth.NewHandler(authSvc, !cfg.IsDev())
	log.Info("auth ready",
		zap.Duration("session_ttl", cfg.Auth.SessionTTL),
		zap.Bool("registration_open", cfg.Auth.RegistrationOpen()))

	// —— 对话模型配置（用户配置 + 加密落库）——
	//
	// 主密钥优先取 MINDOS_MASTER_KEY；没配就自动生成到 DataDir/master.key。
	// 这一步放在启动阶段而不是第一次保存时：密钥不可用属于部署故障，
	// 应该在启动时就失败，而不是等用户填完表单才报错。
	masterKey, err := chatmodel.LoadMasterKey(cfg.Crypto.MasterKey, cfg.App.DataDir)
	if err != nil {
		return fmt.Errorf("load master key: %w", err)
	}
	modelCipher, err := chatmodel.NewCipher(masterKey)
	if err != nil {
		return fmt.Errorf("init model cipher: %w", err)
	}
	// prober 用 agent.Probe：连通性测试必须走问答用的同一个客户端，
	// "探针能过、真实调用失败"是最没用的测试结果。
	chatModelSvc := chatmodel.NewService(chatmodel.NewRepository(db), modelCipher, agent.Probe)
	log.Info("chat model config ready", zap.String("data_dir", cfg.App.DataDir))

	// —— 检索与问答 ——
	//
	// Searcher 只给 agent 的 search_knowledge 工具用：宿主不再预检索，
	// 是否检索、检索几次由模型在 ReAct 循环里决定。
	// embedding 为 nil 时 Search 直接报"未配置"，不会悄悄退化成全文检索。
	// SEARCH_BACKEND=es 时换成 ES 读模型；否则保持 PG 原生检索。
	var searcher knowledge.SearcherAPI = knowledge.NewSearcher(knowledge.NewRepository(db), embedding)
	if esClient != nil {
		searcher = knowledge.NewESSearcher(esClient, embedding, cfg.Elastic.IndexPrefix)
	}

	todoSvc := todo.NewService(todo.NewRepository(db))
	approvalSvc := approval.NewService(approval.NewRepository(db))
	checkpointStore := agent.NewCheckpointStore(db)

	agentBuilder := agent.NewBuilder(
		knowledgeRetriever{searcher: searcher},
		sqlExecutor,
		todoSvc,
		approvalSvc,
		checkpointStore,
		agent.DefaultLimits(),
		cfg.Rag.TopK,
		log.Named("agent"),
	)

	// 会话冷层：消息索引器负责写 ES，MessageSearcher 负责召回。
	// embedding 为 nil 时只走关键词，摘要仍可用。
	var messageIndexer *chat.MessageIndexer
	var chatMemory *chat.ConversationMemory
	if esClient != nil {
		conversationRepo := chat.NewConversationRepository(db)
		messageSearcher := chat.NewMessageSearcher(esClient, embedding, cfg.Elastic.IndexPrefix)
		chatMemory = chat.NewConversationMemory(conversationRepo, messageSearcher, log.Named("memory"))
		messageIndexer = chat.NewMessageIndexer(db, esClient, cfg.Elastic.IndexPrefix, embedding, log.Named("search"))
	}

	preferences := memory.NewService(memory.NewRepository(db))
	chatSvc := chat.NewService(
		chatModelSvc,
		chat.NewRepository(db),
		agentBuilder,
		chat.NewConversationRepository(db),
		chatMemory,
		approvalSvc,
		todoSvc,
		log.Named("chat"),
	).WithPreferences(preferences)

	// 任务表在业务层之前创建：knowledge 需要它判断"取消时任务排着队还是在跑"，
	// 消费端需要它写进度，两者共用同一个对象。
	taskRepo := task.NewRepository(db)

	knowledgeSvc := knowledge.NewService(
		knowledge.NewRepository(db),
		store,
		taskRepo,
		embedding,
		cfg.Storage.MaxUploadBytes,
		log.Named("knowledge"),
	)

	// —— 异步链路：outbox → RabbitMQ → 消费者 ——
	//
	// 三段各管一件事：relay 把表里的待发消息送出去，consumer 收到消息后干活
	// 并更新 tasks，补扫负责收拾"消息丢了"的残局。
	outboxRepo := outbox.NewRepository(db)

	consumer := dispatch.NewConsumer(dispatch.NewSQLStore(outboxRepo), log.Named("dispatch"), dispatch.DefaultMaxAttempts)
	handle := func(kind string) dispatch.Handler {
		return func(ctx context.Context, env platform.Envelope) error {
			var p struct {
				DocumentID int64 `json:"document_id"`
				TaskID     int64 `json:"task_id"`
			}
			if err := json.Unmarshal(env.Payload, &p); err != nil || p.DocumentID == 0 || p.TaskID == 0 {
				return fmt.Errorf("bad envelope payload: %w", err)
			}

			doc, err := knowledgeSvc.GetInternal(ctx, p.DocumentID)
			if err != nil {
				return err
			}
			// 用户已经点了取消：不执行，直接按取消收尾。
			// 收尾方式取决于任务类型——取消向量化绝不能把一份 ready 的文档标成失败。
			if doc.Status == knowledge.StatusCancelling {
				_ = taskRepo.MarkCancelled(ctx, p.TaskID)
				return knowledgeSvc.SettleCancelled(ctx, p.DocumentID, kind)
			}

			// 条件更新是执行幂等闸门：重复投递时只有一个实例能推进到 running。
			started, err := taskRepo.MarkRunning(ctx, p.TaskID)
			if err != nil {
				return err
			}
			if !started {
				return nil
			}

			// 一个文档一条消息：导入与重索引都在这里把切分与向量化做完，
			// 两者写库在同一个事务里，所以"返回 nil"等于"内容与向量都已落库"。
			var runErr error
			switch kind {
			case knowledge.KindDocumentEmbed:
				runErr = knowledgeSvc.ProcessEmbed(ctx, p.DocumentID)
			case knowledge.KindDocumentReindex:
				runErr = knowledgeSvc.ProcessReindex(ctx, p.DocumentID)
			default:
				runErr = knowledgeSvc.ProcessImport(ctx, p.DocumentID)
			}
			if runErr == nil {
				// 收尾前再看一眼取消：执行途中用户可能刚点了取消。
				// 内容确实做好了，就保留成果——"已经完成"是事实，
				// 把一份可用文档标成失败才是骗人。
				if doc, err := knowledgeSvc.GetInternal(ctx, p.DocumentID); err == nil && doc.Status == knowledge.StatusCancelling {
					_ = taskRepo.MarkCancelled(ctx, p.TaskID)
					return nil
				}
				return taskRepo.MarkSucceeded(ctx, p.TaskID)
			}

			// 结果作废不是失败：分块已被重索引整体替换，新任务正在算。
			// 标成成功而不是失败，也不占重试次数——重试只会再算一遍同一个旧结果。
			if errors.Is(runErr, knowledge.ErrStaleChunks) {
				log.Info("embedding result discarded, chunks were replaced",
					zap.Int64("document_id", p.DocumentID))
				return taskRepo.MarkSucceeded(ctx, p.TaskID)
			}

			// 永久错误重试几次都是同一个结果，直接收尾；可恢复错误才跑满次数。
			if knowledge.IsPermanent(runErr) || env.Attempt+1 >= dispatch.DefaultMaxAttempts {
				// 两张表都写：task 留技术细节（第几次、原始错误），
				// document 留用户看得见的原因。少写一处就会出现
				// "消息放弃了但页面还在转圈"。
				if kind == knowledge.KindDocumentEmbed {
					// 只写向量状态：分块是好的，文档不该变成"失败"。
					if err := knowledgeSvc.MarkEmbedFailed(ctx, p.DocumentID, runErr); err != nil {
						log.Warn("mark embedding failed",
							zap.Int64("document_id", p.DocumentID), zap.Error(err))
					}
				} else if err := knowledgeSvc.MarkProcessingFailed(ctx, p.DocumentID, runErr); err != nil {
					log.Warn("mark document failed",
						zap.Int64("document_id", p.DocumentID), zap.Error(err))
				}
				return taskRepo.MarkFailed(ctx, p.TaskID, knowledge.ErrorCode(runErr), runErr.Error())
			}
			if err := taskRepo.MarkPending(ctx, p.TaskID); err != nil {
				log.Warn("reset task to pending failed", zap.Int64("task_id", p.TaskID), zap.Error(err))
			}
			return runErr
		}
	}
	consumer.Register(string(knowledge.KindDocumentImport), handle(knowledge.KindDocumentImport))
	consumer.Register(string(knowledge.KindDocumentReindex), handle(knowledge.KindDocumentReindex))
	consumer.Register(string(knowledge.KindDocumentEmbed), handle(knowledge.KindDocumentEmbed))

	// 检索索引事件：ES 未启用时注册空处理器，避免 outbox 里的 search.*
	// 被当成未知 topic 丢弃并刷错误日志。
	decodeDocumentID := func(env platform.Envelope) (int64, error) {
		var payload struct {
			DocumentID int64 `json:"document_id"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return 0, fmt.Errorf("decode %s payload: %w", env.Topic, err)
		}
		if payload.DocumentID <= 0 {
			return 0, fmt.Errorf("%s payload has invalid document_id %d", env.Topic, payload.DocumentID)
		}
		return payload.DocumentID, nil
	}
	searchUpsert := func(ctx context.Context, env platform.Envelope) error {
		if searchIndexer == nil {
			return nil
		}
		id, err := decodeDocumentID(env)
		if err != nil {
			return err
		}
		return searchIndexer.UpsertDocument(ctx, id)
	}
	searchDelete := func(ctx context.Context, env platform.Envelope) error {
		if searchIndexer == nil {
			return nil
		}
		id, err := decodeDocumentID(env)
		if err != nil {
			return err
		}
		return searchIndexer.DeleteDocument(ctx, id)
	}
	consumer.Register(knowledge.SearchTopicDocumentUpsert, searchUpsert)
	consumer.Register(knowledge.SearchTopicDocumentDelete, searchDelete)

	decodeMessageID := func(env platform.Envelope) (int64, error) {
		var payload struct {
			MessageID int64 `json:"message_id"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return 0, fmt.Errorf("decode %s payload: %w", env.Topic, err)
		}
		if payload.MessageID <= 0 {
			return 0, fmt.Errorf("%s payload has invalid message_id %d", env.Topic, payload.MessageID)
		}
		return payload.MessageID, nil
	}
	messageUpsert := func(ctx context.Context, env platform.Envelope) error {
		if messageIndexer == nil {
			return nil
		}
		id, err := decodeMessageID(env)
		if err != nil {
			return err
		}
		return messageIndexer.Upsert(ctx, id)
	}
	messageEmbed := func(ctx context.Context, env platform.Envelope) error {
		if messageIndexer == nil {
			return nil
		}
		id, err := decodeMessageID(env)
		if err != nil {
			return err
		}
		return messageIndexer.Embed(ctx, id)
	}
	messageDelete := func(ctx context.Context, env platform.Envelope) error {
		if messageIndexer == nil {
			return nil
		}
		id, err := decodeMessageID(env)
		if err != nil {
			return err
		}
		return messageIndexer.Delete(ctx, id)
	}
	consumer.Register(chat.SearchMessageUpsert, messageUpsert)
	consumer.Register(chat.SearchMessageEmbed, messageEmbed)
	consumer.Register(chat.SearchMessageDelete, messageDelete)
	consumer.Register(chat.SearchConversationDelete, func(ctx context.Context, env platform.Envelope) error {
		if messageIndexer == nil {
			return nil
		}
		var payload struct {
			ConversationID int64 `json:"conversation_id"`
			OwnerID        int64 `json:"owner_id"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return fmt.Errorf("decode conversation delete payload: %w", err)
		}
		if payload.ConversationID <= 0 || payload.OwnerID <= 0 {
			return fmt.Errorf("conversation delete payload has invalid ids: %+v", payload)
		}
		return messageIndexer.DeleteConversation(ctx, payload.OwnerID, payload.ConversationID)
	})
	consumer.Register(chat.SummaryTopic, func(ctx context.Context, env platform.Envelope) error {
		var payload struct {
			ConversationID int64 `json:"conversation_id"`
			OwnerID        int64 `json:"owner_id"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return fmt.Errorf("decode summary payload: %w", err)
		}
		if payload.ConversationID <= 0 || payload.OwnerID <= 0 {
			return fmt.Errorf("summary payload has invalid ids: %+v", payload)
		}
		return chatSvc.SummarizeConversation(ctx, payload.OwnerID, payload.ConversationID)
	})
	consumer.Register(memory.ExtractTopic, memoryExtractHandler(memory.NewExtractor(chatSvc, chatModelSvc, preferences)))

	relay := outbox.NewRelay(outboxRepo, relayPublisher{rabbit: rabbit}, log.Named("relay"), outbox.RelayOptions{})

	// 兜底：broker 只管"送到"，管不了"消费端半路崩了"。
	// 卡在 running 的任务队列里已经没有任何消息，只有对账能把它捞回来。
	reaper := reaper.New(db, taskRepo, outboxRepo, authRepo, log.Named("reaper"), reaper.Options{
		Interval:   cfg.Reaper.Interval,
		StaleAfter: cfg.Reaper.StaleAfter,
		Retention:  cfg.Reaper.Retention,
	})

	asyncCtx, stopAsync := context.WithCancel(ctx)
	var asyncWG sync.WaitGroup
	asyncWG.Add(1)
	go func() { defer asyncWG.Done(); relay.Run(asyncCtx) }()
	asyncWG.Add(1)
	go func() { defer asyncWG.Done(); reaper.Run(asyncCtx) }()
	if rabbit != nil {
		for i := 0; i < cfg.Job.Workers; i++ {
			asyncWG.Add(1)
			go func() {
				defer asyncWG.Done()
				for asyncCtx.Err() == nil {
					if err := rabbit.ConsumeEnvelopes(asyncCtx, consumer.Handle); err != nil && asyncCtx.Err() == nil {
						log.Warn("outbox consumer stopped, reconnecting", zap.Error(err))
						select {
						case <-asyncCtx.Done():
						case <-time.After(time.Second):
						}
					}
				}
			}()
		}
	}
	// 退出顺序：先停消费（不再接新消息），再等手上处理完，最后关连接与数据库。
	defer func() {
		stopAsync()
		done := make(chan struct{})
		go func() { asyncWG.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(cfg.App.ShutdownTimeout):
			log.Warn("async workers did not stop before shutdown timeout",
				zap.Duration("timeout", cfg.App.ShutdownTimeout))
		}
	}()

	engine := newEngine(log, cfg.IsDev())
	router.Register(engine, router.Deps{
		Health:         health,
		System:         system,
		Settings:       settings,
		Auth:           authHandler,
		RequireSession: auth.NewSessionMiddleware(authSvc),
		// 可信来源来自配置：反向代理改写 Host 时，Origin 与 Host 天然不等，
		// 合法请求会被误判成跨站。日志里会打出两组地址，便于排查。
		CheckOrigin: auth.CheckOrigin(cfg.Auth.AllowedOrigins, log.Named("auth")),
		Knowledge:   knowledge.NewHandler(knowledgeSvc),
		ChatModels:  chatmodel.NewHandler(chatModelSvc),
		Ask:         chat.NewHandler(chatSvc),
		Todos:       todo.NewHandler(todoSvc),
		Memories:    memory.NewHandler(preferences),
	})

	// 【为什么不用 router.Run()】
	// router.Run() 的源码只有一行：return http.ListenAndServe(address, engine)，
	// 它把 http.Server 藏起来了——没地方配超时，也没有 Shutdown 方法可用。
	// 所以直接创建标准库的 http.Server，这是 gin 官方 README
	// "Graceful shutdown or restart" 一节给出的写法。
	srv := &http.Server{
		Addr:    cfg.App.HTTPAddr,
		Handler: engine, // *gin.Engine 实现了标准库 http.Handler 接口（有 ServeHTTP 方法），
		// 所以可以直接当处理器传进来——gin 在 Go 里就是一个普通的 HTTP 处理器，不是特殊运行时。
		ReadHeaderTimeout: 5 * time.Second,  // 防止 Slowloris 类攻击
		ReadTimeout:       15 * time.Second, // 包含读 body 的时间
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// 6. 在自己的 goroutine 里启动监听，主 goroutine 负责等待信号。
	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", zap.String("addr", srv.Addr))
		// ListenAndServe 是阻塞的，正常情况下永不返回；
		// 唯一让它返回的是 Shutdown（第 8 步），此时返回 http.ErrServerClosed——
		// 这是"正常关闭"的信号，不是错误，所以用 errors.Is 排除它。
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	// 7. 等待"退出信号"或"服务异常退出"，谁先来处理谁。
	select {
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections",
			zap.Duration("timeout", cfg.App.ShutdownTimeout))
	}

	// 8. 优雅关闭：停止接受新连接，给在途请求留出完成时间。
	//    注意必须用独立的 context：根 ctx 已经取消了，拿它去做超时会立即返回。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close() // 超时则强制关闭，丢弃剩余连接
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("mindos stopped")
	return nil
}

// embeddingProbe 把 platform 的探针适配成 handler 需要的结果类型。
//
// 与下面的 relayPublisher 同一个理由：两边都不该认识对方的类型，
// 装配层是唯一合理的转换位置。e 为 nil（未配置模型）时返回 nil，
// handler 收到 nil 就回 MODEL_NOT_CONFIGURED，而不是发一次必然失败的请求。
func embeddingProbe(e *platform.Embedder) handler.EmbeddingProbeFunc {
	if e == nil {
		return nil
	}
	return func(ctx context.Context) (handler.EmbeddingProbe, error) {
		res, err := e.Probe(ctx)
		if err != nil {
			return handler.EmbeddingProbe{}, err
		}
		return handler.EmbeddingProbe{
			Model:      res.Model,
			Dimensions: res.Dimensions,
			LatencyMS:  res.Latency.Milliseconds(),
		}, nil
	}
}

// relayPublisher 把 platform.Rabbit 适配成 outbox.Queue。
//
// 适配放在 main 而不是任一包里：两边都不该认识对方的类型，
// 装配层是唯一合理的位置。
type relayPublisher struct{ rabbit *platform.Rabbit }

func (p relayPublisher) Publish(ctx context.Context, m outbox.Message) error {
	if p.rabbit == nil {
		// 启动时没连上 broker：这是"通道不可用"，不是这条消息的问题。
		// 用哨兵错误让它走上"暂停等待"而不是"消耗重试次数"。
		return fmt.Errorf("%w: no broker connection", outbox.ErrQueueUnavailable)
	}
	payload, err := json.Marshal(map[string]any(m.Payload))
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}
	err = p.rabbit.PublishEnvelope(ctx, platform.Envelope{
		MessageID: m.MessageID,
		Topic:     m.Topic,
		MsgKey:    m.MsgKey,
		Payload:   payload,
		Attempt:   m.Attempts,
	})
	if errors.Is(err, platform.ErrQueueClosed) {
		return fmt.Errorf("%w: %v", outbox.ErrQueueUnavailable, err)
	}
	return err
}

// knowledgeRetriever 把 knowledge.Searcher 适配成 agent.Retriever。
//
// 适配放在 main 而不是任一包里：agent 不该为了一个方法认识 knowledge 的类型，
// knowledge 也不该知道有人要拿它喂模型。这层转换只有一行循环，
// 却让 agent 包可以脱离数据库测试。
type knowledgeRetriever struct{ searcher knowledge.SearcherAPI }

func (r knowledgeRetriever) Search(ctx context.Context, ownerID int64, query string, limit int) ([]agent.Hit, error) {
	result, err := r.searcher.Search(ctx, ownerID, query, limit, knowledge.DocumentFilter{})
	if err != nil {
		return nil, err
	}
	hits := make([]agent.Hit, 0, len(result.Hits))
	for _, hit := range result.Hits {
		hits = append(hits, agent.Hit{
			DocumentID:  hit.DocumentID,
			ChunkID:     hit.ChunkID,
			Seq:         hit.Seq,
			Title:       hit.Title,
			Heading:     hit.Heading,
			Content:     hit.Content,
			StartOffset: hit.StartOffset,
			EndOffset:   hit.EndOffset,
			Similarity:  hit.Similarity,
			RRFScore:    hit.RRFScore,
		})
	}
	return hits, nil
}
