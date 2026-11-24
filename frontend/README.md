# MindOS 前端

React 单页应用，开发期跑在 5173，`/api` 请求代理到本地 Go 服务 8080。生产同源托管暂未实现。

## 技术栈

| 层 | 选型 | 说明 |
|---|---|---|
| 构建 | Vite 8 | 无 SSR，纯静态产物 |
| 框架 | React 19 + TypeScript | `strict` 开启，`verbatimModuleSyntax` 强制 `import type` |
| 样式 | Tailwind CSS v4 | 无 `tailwind.config.js`，主题写在 `src/index.css` 的 `@theme` |
| 组件 | shadcn/ui（复制式） | Button / Card / Badge / Skeleton / Select / Toast / Drawer / Modal 等在 `src/components/ui/`；通用 Markdown 在 `src/components/markdown.tsx` |
| 路由 | React Router 8 | `createBrowserRouter`，路由表在 `src/app/router.tsx` |
| 数据 | TanStack Query 5 | 服务端状态一律走 Query，不复制进本地 state |
| 测试 | Vitest 5 + Testing Library | jsdom 环境，`src/test/` 放 setup 与工具 |

**环境**：Node 20+（本项目在 WSL 内用 nvm 的 v24）。nvm 只在交互式 shell 生效，IDE 任务或 `bash -c` 里需要显式加 PATH：

```bash
export PATH=$HOME/.nvm/versions/node/v24.22.1/bin:$PATH
```

## 目录结构

```text
frontend/
├── src/
│   ├── api/            统一客户端、错误模型、接口类型镜像
│   │   └── test/       对应模块的测试（约定：测试只放 test/ 子目录）
│   ├── app/            App 组合根、路由表、应用外壳、模块元数据
│   │   └── test/
│   ├── components/     业务无关组件（空态、错误态、状态标签）
│   │   └── ui/         shadcn/ui 基础组件
│   ├── lib/            cn() 等纯工具
│   ├── pages/          六个页面 + 404
│   │   └── test/
│   └── test/           共享测试设施：setup、fixtures、renderApp、位置约定自检
└── vite.config.ts      Tailwind 插件、@ 别名、/api 代理、Vitest 配置
```

## 命令

```bash
npm install          # 首次
npm run dev          # 开发服务器 http://localhost:5173
npm run lint         # ESLint（扁平配置）
npm run test         # Vitest 单次运行
npm run test:watch   # 监听模式
npm run build        # tsc --noEmit && vite build，产物在 dist/
```

三项门禁（lint / test / build）必须同时通过才算阶段完成。

## 开发约定

**编码规范（导出、渲染纪律、类型、命名、提交前自查）见 [CONVENTIONS.md](CONVENTIONS.md)。** 下面只写本项目特有的约定：数据怎么取、错误怎么表达、页面怎么加。

**交互与组件**

- 基础控件只用 `src/components/ui/`：Button、Input、Textarea、Select、Toast、Drawer、Modal 等，不另写一套。
- 右侧抽屉 `Drawer` 用于持续编辑（写笔记）；`Modal` 用于短流程（上传文件、筛选、确认）。
- 知识库的「上传文件」只打开导入弹框；在弹框内点「选择文件」后才打开系统文件选择器。
- 文档、待办、会话和对话模型共用 `DeleteConfirmDialog`，确认前展示删除目标与后果。知识库分类与标签在弹框中编辑，取消不保存。
- 任务页新建和编辑待办复用写笔记的右侧抽屉，表单填满抽屉内容区，描述随高度伸展。删除在弹框中确认。操作失败时在原抽屉或弹框中显示原因，可直接重试。
- 日期和时间输入使用共享 `Input`，支持点击整框打开浏览器选择器，并保留原生键盘与图标操作。
- 对话区离底部超过 160 像素时在输入框上方居中显示「回到底部」，与输入区保留 16 像素间距。上翻阅读时不跟随流式回复，回到底部后恢复跟随。
- 会话/文档列表的当前项统一用 `SlidingPill` 玻璃胶囊。
- 正文统一用 `components/markdown.tsx`；对话和知识库共用，HTML 默认转义。
- 流式状态、按会话判断生成中、路由切换的边界见 [docs/design/frontend-state.md](../docs/design/frontend-state.md)。

**数据**

- 服务端状态只走 TanStack Query；查询键集中在 `src/api/system.ts` 的 `queryKeys`，不散写。
- 接口类型在 `src/api/types.ts` 手工镜像后端 JSON，字段保持 snake_case，不引入 camelCase 转换层。
- 后端接口没实现就显示"尚未接入"，**不摆模拟数据、假图表**。
- 失败态必须写出原因并提供重试入口；空态与失败态分开表达。

**错误**

- 所有请求经 `apiRequest`，它在唯一一处解信封 `{ code, message, data, request_id }`。
- 失败抛 `ApiError`，`kind` 区分 `network` / `timeout` / `aborted` / `http` / `business` / `malformed`，不要靠 `message` 字符串判断。
- 展示用 `describeError(error)`，有 `request_id` 时会附在文案里，方便对后端日志。

**页面与路由**

- 导航项的路径、名称和图标集中在 `src/app/modules.ts`，工作台也复用这份入口定义。
- 页面标题只放名称和操作；用列表、分隔线与原生表单表达层级。空态只说当前状态，诊断信息按需展开，失败原因留在操作附近。
- 新增页面：`modules.ts` 加一条 → `src/pages/` 建组件 → `router.tsx` 挂到 `AppShell` 的 children → 在 `app.test.tsx` 里补一条可见性用例。
- 桌面导航位于顶栏，窄屏（`< md`）占独立一行；当前项仍由路由的 `aria-current` 决定，胶囊只是视觉层。
- 磨砂面板共用 `workspace.css` 的 `.glass-surface`。工作台入口的 `Card interactive` 与对话、知识库侧栏顶部按钮共用跟随光标的光斑和局部边框高光；侧栏按钮在亮色主题用白底，且不倾斜。触屏、禁用按钮或开启「减少动态效果」时不启用光斑。
- 知识页是第一个真实业务页：上传走 `FormData`（客户端只在 `apiRequest` 一处判断，不手写 `Content-Type`），分块偏移是**码点**而不是 UTF-16 下标；需要高亮原文时用 `Array.from(text).slice(start, end)`，不要用 `String.slice`。
- 问答页（`/chat`）的 SSE **不用 EventSource**：它只支持 GET、带不了 JSON body，而 `POST /ask` 的提问在 body 里。所以 `src/api/ask.ts` 用 fetch + ReadableStream 手写解析——按空行切事件、先去掉 `\r`（有的代理会改写换行）、坏事件跳过、流结束时把缓冲里最后一条也分发。这是对"原生 EventSource"直觉的一处有意偏离，理由就写在这里与 `ask.ts` 头部注释。
- 对话助手 Lore 用正文检索回答知识库问题，按配置可查询文档元数据和待办。正文里的 `[n]` 只有收到匹配的 citation 映射才渲染成链接（`/knowledge?doc=&chunk=` 深链）；没有映射的编号保持纯文本，不触发整段撤回。Lore 提交的待办变更由界面审批，聊天里回复「批准」不会执行变更。

## 测试

```bash
npm run test                                  # 全部
npx vitest run src/pages/test/settings.test.tsx   # 单个文件
```

- **测试只放对应目录的 `test/` 子目录**，不与业务文件混放（规则见 `CONVENTIONS.md` 第 9 节，由 `src/test/placement.test.ts` 强制执行）。
- `src/test/render-app.tsx` 用真实路由表 + 独立 QueryClient 渲染整页，路径由参数指定。
- `src/test/fixtures.ts` 提供信封、状态与配置的固定数据，避免每个用例手写一大坨 JSON。
- 请求用 `vi.stubGlobal('fetch', ...)` 替换，不启真实后端；`afterEach` 里 `vi.unstubAllGlobals()`。
- 覆盖重点：成功解析、六类失败分支、加载态、空态、重试后恢复。
- `src/app/test/workspace-motion.test.tsx` 离线验证胶囊关键帧、按路由定位及卡片指针交互。视觉验收用现有浏览器打开开发服务器（`localhost:5173`），登录后检查桌面和手机宽度、快速切换与后退导航、卡片四角悬停和移出；用浏览器开发者工具模拟 `prefers-reduced-motion: reduce` 后再检查一次。

## 当前限制

- 设置页的运行配置块只读（改 `backend/.env` 并重启）；**对话模型卡片除外**——它读写数据库里的 `chat_models`，改完立即生效。
- 问答是多轮会话：历史落数据库，待审批操作跨刷新保留；SPA 内切回会话或对话页时，live store 里的已有正文直接显示，只逐字追加新内容。**整页刷新、关闭浏览器或直接改地址栏仍会断开 SSE**，后端 run 会取消。
- 知识页支持导入、筛选、删除与重建向量；分块一次最多显示 100 条。Markdown/笔记按正文渲染，纯文本保留原始换行。
- 亮/暗主题已实现，存在 `localStorage.mindos.theme`；深色为中性黑灰。
- 生产构建产物尚未由后端托管。
