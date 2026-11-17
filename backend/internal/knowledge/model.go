// Package knowledge 是知识模块：把文件、笔记、网页变成可检索的分块。
//
// 分层与项目约定一致：
//
//	handler.go    HTTP 绑定、参数校验、错误码映射
//	service.go    业务规则：类型/大小校验、事务边界、落盘与清理
//	repository.go GORM 数据访问（只收 context + 参数）
//	parser.go     原文 → 结构块（标题 / 段落 / 代码块），保留字节偏移
//	chunker.go    结构块 → 分块，保留码点偏移
//	model.go      documents / document_chunks 的 Go 视图（本文件）
//
// 模块边界：这里不认识 gin 的 Context、不拼 HTTP 响应；错误用本包的
// 哨兵错误（ErrNotFound 等）表达，由 handler 翻译成 httpx 错误码。
package knowledge

import "time"

// 文档来源类型，对应 documents.source_type 的 CHECK 约束。
const (
	SourceFile = "file"
	SourceNote = "note"
	SourceWeb  = "web" // Phase 5 起使用
)

// 文档状态，对应 documents.status 的 CHECK 约束。
//
// Phase 4 是同步导入：解析与切分在请求内完成，成功即 ready，失败整笔事务回滚，
// 所以这一阶段只会看到 ready。其余状态留给 Phase 6 的异步任务（pending/parsing/
// indexing/failed/cancelling）；先把常量定义齐，是为了让状态机的完整含义有落点。
const (
	StatusPending    = "pending"
	StatusParsing    = "parsing"
	StatusIndexing   = "indexing"
	StatusReady      = "ready"
	StatusFailed     = "failed"
	StatusCancelling = "cancelling"
)

// EmbeddingDimensions 是向量列的维度。
//
// 它必须与迁移 0008 里的 vector(1024) 严格一致：pgvector 的列维度在 DDL 时定死，
// 写入长度不符会直接报错。启动时 platform.VectorColumnType 会核对两者，
// 不一致就拒绝启动——否则错误会在用户第一次导入时才冒出来，
// 而且会被包成一句看不出原因的 "process_failed"。
//
// 为什么是 1024 而不是 1536：默认选 bge-m3（中文效果好、本机 Ollama 与
// 硅基流动都能免费跑），它输出 1024 维；1024 也在 pgvector 的 HNSW 上限
// （2000 维）之内，索引才建得起来。
const EmbeddingDimensions = 1024

// 向量化状态，对应 documents.embed_status 的 CHECK 约束。
//
// 与 documents.status 是两件事：一个文档可以 status=ready（内容可用）
// 但 embed_status=failed（语义检索用不了）。合成一个字段就没法如实表达
// "内容没问题，只是向量没做出来"。
const (
	// EmbedStatusPending 该做还没做。
	EmbedStatusPending = "pending"
	// EmbedStatusRunning 正在调模型。
	EmbedStatusRunning = "running"
	// EmbedStatusDone 已按 embed_model 记录的模型算完。
	EmbedStatusDone = "done"
	// EmbedStatusFailed 失败，原因在 embed_error_code / embed_error_message。
	EmbedStatusFailed = "failed"
	// EmbedStatusSkipped 未配置向量模型，本次跳过。
	//
	// 与 pending 分开：前者要用户去配 Key，后者只要等。
	EmbedStatusSkipped = "skipped"
)

// Document 对应 documents 表。
//
// 只声明本阶段真正读写的列。表里还有 source_url / error_code / error_message，
// 它们分别属于网页抓取（已移出主线）与 Phase 6 的异步任务失败信息，
// 现在加到结构体里只会得到"永远是零值"的字段，读代码的人还要多判断一次
// "这个字段到底从哪来"。需要时再加，加字段不需要改表。
type Document struct {
	ID int64 `gorm:"column:id;primaryKey"`
	// OwnerID 是文档归属的用户 id，隔离的唯一依据。
	// 所有面向用户的查询都必须带它；异步链路可以按 id 直查（id 来自内部消息）。
	OwnerID   int64  `gorm:"column:owner_id"`
	Title     string `gorm:"column:title"`
	MIME      string `gorm:"column:mime"`
	SizeBytes int64  `gorm:"column:size_bytes"`
	// ContentHash 是 SHA-256 十六进制串，重复导入的判定依据（唯一索引）。
	ContentHash string `gorm:"column:content_hash"`
	SourceType  string `gorm:"column:source_type"`
	// StoragePath 是相对 STORAGE_DIR 的路径，null 表示没有原始文件（笔记）。
	StoragePath *string `gorm:"column:storage_path"`
	// Content 只对笔记有值；文件的正文不重复入库，需要时从 StoragePath 读。
	Content *string `gorm:"column:content"`
	// Collection 是单值分类，nil 表示未分类。不做成外键表：单用户场景下
	// 分类只是一层筛选标签，独立建表换来的只有一次 JOIN。
	Collection *string `gorm:"column:collection"`
	// Tags 对应 text[]。必须写下 type:text[]，否则 GORM 无法推断列类型
	// （见 string_array.go 顶部的实测记录）。
	Tags       StringArray `gorm:"column:tags;type:text[]"`
	Status     string      `gorm:"column:status"`
	ChunkCount int         `gorm:"column:chunk_count"`
	// ErrorCode / ErrorMessage 只在处理最终失败时写入（Phase 6 异步任务）。
	// 分开存是因为前者给程序判断（例如 source_missing 可以引导用户重新上传），
	// 后者给人看；混成一个字段就得靠字符串匹配来做判断。
	ErrorCode    *string `gorm:"column:error_code"`
	ErrorMessage *string `gorm:"column:error_message"`
	// —— 向量化状态（迁移 0008）——
	// 与上面的 error_* 分开：一份文档可能"内容处理成功、向量化失败"，
	// 混用一套错误字段就分不清下一步该重切还是重算向量。
	// default:pending 不是可有可无的装饰：GORM 插入时会把零值字段一起写进 SQL，
	// 于是 embed_status 会变成空串 ''，撞上 CHECK 约束。
	// 声明默认值后 GORM 会跳过零值字段，交给数据库用 DEFAULT 'pending'。
	EmbedStatus       string     `gorm:"column:embed_status;default:pending"`
	EmbedModel        *string    `gorm:"column:embed_model"`
	EmbedDimensions   *int       `gorm:"column:embed_dimensions"`
	EmbeddedChunks    int        `gorm:"column:embedded_chunks;default:0"`
	EmbedErrorCode    *string    `gorm:"column:embed_error_code"`
	EmbedErrorMessage *string    `gorm:"column:embed_error_message"`
	EmbeddedAt        *time.Time `gorm:"column:embedded_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

// TableName 显式声明表名。GORM 的默认复数化对 document 恰好正确，
// 但显式写出来可以避免以后改结构体名时悄悄换表。
func (Document) TableName() string { return "documents" }

// Chunk 对应 document_chunks 表。
//
// 偏移是**码点**偏移，不是字节。原文可以用它精确切片：
//
//	runes := []rune(text); runes[c.StartOffset:c.EndOffset]
//
// 为什么不在结构体里放 Embedding：向量只写不读（检索阶段才会读），
// 1024 维 float32 每次查询会白白多搬 4KB/行。写入走 repository 里
// 专门的逐行 UPDATE，既避免 GORM 把自定义类型当关联结构体解析，
// 也让"哪一行没更新到"这个信号可以直接拿到（见 SaveEmbeddings 的新鲜度闸门）。
type Chunk struct {
	ID         int64  `gorm:"column:id;primaryKey"`
	DocumentID int64  `gorm:"column:document_id"`
	Seq        int    `gorm:"column:seq"`
	Content    string `gorm:"column:content"`
	// Heading 是分块所在章节的最近一级标题，文首（第一个标题之前）为 nil。
	Heading     *string   `gorm:"column:heading"`
	StartOffset int       `gorm:"column:start_offset"`
	EndOffset   int       `gorm:"column:end_offset"`
	TokenCount  int       `gorm:"column:token_count"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// TableName 见 Document.TableName 的说明。
func (Chunk) TableName() string { return "document_chunks" }
