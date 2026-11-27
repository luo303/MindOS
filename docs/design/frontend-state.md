# 前端状态与流式回答

本文记录“服务端状态、页面状态、流式状态”分别放在哪里，以及路由切换、并发会话、取消/刷新时会发生什么。

## 1. 状态分层

| 状态 | 归属 | 例子 |
|---|---|---|
| 服务端事实 | TanStack Query 缓存 | 会话列表、历史消息、文档详情、待审批 |
| 刚结束的 SSE 轮次 | 按会话缓存于 TanStack Query | 正文、运行信息、客户端错误详情；切页时无需等历史回读 |
| URL / 本机偏好 | 路由与 localStorage | 当前页面、当前会话 id、选中的模型 |
| 正在生成的回答 | 模块级 live store | `pages/chat/live.ts` |
| 纯页面展示态 | 组件 `useState` | 抽屉开关、右键菜单、筛选弹窗 |

原则：**谁的生命周期最长，状态就放在谁那里**。会话级状态不能放在页面组件里；页面卸载后它还要继续存在。

## 2. 路由切换与 HTTP 生命周期

- React 组件卸载只销毁组件状态，不会自动 abort `fetch`。
- SSE 的控制器在 `run()` 闭包里；没有清理 effect 调 `abort`，所以 SPA 内切页时连接继续。
- 后端 `context` 绑定的是 HTTP 连接：整页刷新、关标签页、浏览器断开才会触发 `context.Canceled`。

因此：

```text
SPA 切页        → 流继续，切回时直接显示已收到的正文
整页刷新/关闭   → 流真正断开，后端 run 取消
```

如果以后要支持“刷新后继续生成”，必须把 run 从 HTTP 生命周期解耦成后端异步任务，再提供状态查询/重连；仅靠前端 store 不够。

## 3. live store

`pages/chat/live.ts` 结构：

```ts
Map<conversationId, { turn, controller }>
```

- `publishLive`：每次 delta / tool / done 更新快照并通知订阅者；
- `liveTurnOf`：按会话取当前轮次，供 `useSyncExternalStore` 做快照；
- `liveControllerOf`：按会话停止；
- `clearLive(conversationId, controller)`：比较 controller 身份，防止旧请求清掉新请求。

页面通过 `useSyncExternalStore` 订阅：

```ts
const liveTurn = useSyncExternalStore(
  subscribeLive,
  () => liveTurnOf(activeId),
  () => null,
)
```

当 A 会话流式更新而当前是 B 时，B 的快照仍是 `null`，不会把 A 的内容画到 B。

打字机只平滑显示挂载后的新内容。切回会话或对话页时，`useTypewriter` 以 live 快照里已收到的正文初始化，包含离开期间收到的内容；如果从空串初始化，就会把旧正文重新播放一遍。新一轮的初始正文仍为空，因此首批 delta 照常逐字显示。

滚动属于消息视图而非会话流：`MessageViewport` 观察消息和容器尺寸，只在距底部不超过 24 像素时跟随内容增长。超过 160 像素才在输入区上方居中显示回底按钮，留出 16 像素间距，按钮显隐不改变消息区大小。生成中或开启减少动态效果时直接跳到底部，其他情况平滑滚动；切换会话时初始化新的视图位置。

## 4. 历史与 live 合并

同一条用户消息可能同时存在：

- messages 查询里的历史（user 已落库，assistant 还没有）；
- live store 里的正在生成轮次。

`withLiveTurn(turns, live)` 优先按客户端轮次 ID 或后端用户消息 ID 找同一轮：

- 找到就替换，保住历史里的 `messageId`；
- 找不到才追加。

只有 SSE 尚未给出用户消息 ID、且历史末尾有一个同文本但还未回答的用户轮次时，才用问题文本兜底。已有回答的同文问题不会被覆盖。界面直接从历史、已结束的 SSE 轮次和当前 live 快照合并，不再把三份状态各自复制进页面。

## 5. 生成状态按会话建模

禁止：

```ts
const [pending, setPending] = useState(false) // 全局，A 生成会阻塞 B
```

应该：

```ts
const generating = creating || liveTurn !== null
```

其中 `liveTurn` 只取当前 `activeId`。A 生成时切到 B，`generating` 为 false，B 正常显示历史；切回 A 时 live 内容恢复。

多个会话可以同时生成：每个 `run` 有自己的 controller 和 turn id，只更新对应会话的 live 快照。

## 6. 结束后的一致性

流结束且未暂停等待审批时：

- 先把完整轮次写入该会话的 `stream-results` 缓存，再清理 live。结束时即使页面已卸载，切回也能看到最终内容；
- messages 查询用 `refetchType: 'none'` 标脏，不立即重拉，避免闪烁/覆盖；
- 下次进入该会话时回读消息，以后端保存的成功正文为准，用 ID 合并而不是再追加一轮。结束快照只保留最近 25 轮，随查询缓存回收，不是另一份持久化历史。

失败同理：assistant 错误消息落库后标脏，失败卡片带 `detail` 和 `request_id`。

## 7. 错误可观测性

SSE `error` 事件：

```json
{
  "code": "upstream_error",
  "message": "模型服务暂时不可用",
  "retryable": true,
  "request_id": "...",
  "detail": "..."
}
```

- `detail` 截断、去空白、遮盖疑似密钥；
- 服务端 `agent run failed` 记录同一 `request_id` 和完整 `error`；
- 先看 `detail` 是工具、checkpoint、超时还是厂商错误，不要直接归因模型。

## 8. 修改这类代码前的检查

1. 这个状态属于页面、会话，还是全局？
2. 页面卸载后它还要存在吗？
3. 两个会话同时操作会不会互相覆盖？
4. 请求取消是产品行为还是客户端生命周期？
5. 流结束后，缓存是否需要标脏？
6. 失败是否能通过 `request_id` 找到服务端原始错误？

## 9. 相关提交

- `47aac61`：流式状态移到模块级 store，切页可恢复。
- `c61dec4`：生成状态按会话判断，切换会话不再空白。
- `67e6e46`：SSE 错误带 `detail` / `request_id`，失败卡片可诊断。


## 10. 审批中断的正文

写工具触发审批时，普通回答流并没有“结束”，但 HTTP 请求已经返回 `awaiting_approval`。这段正文的归属：

```text
审批前已流出的正文
  → 后端写 agent_approvals.partial_answer
  → 前端 live store 保留该 turn（不要 clearLive）
  → 审批 API 返回 partial_answer，刷新后也能恢复
```

审批通过后的 `Resume` 只产生审批后的新 delta，因此：

```text
最终 assistant 消息 = partial_answer + "\n\n" + resumed_answer
```

审批续跑也使用同一套 live store：

- 按 conversationId 发布；
- 捕获发起时的 conversationId，不读异步回调里的当前 activeId；
- 把 signal 传给 `streamApprovalDecision`；
- 切换会话/页面不 abort；
- 完成后先缓存结束快照，再清 live、标脏 messages；后端已保存的失败/取消结果也可从历史恢复。

否则会出现“审批前正文只在当前页面内存里，数据库永远没有”的问题。
