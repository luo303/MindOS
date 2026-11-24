import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { DocumentView } from '@/api/types'
import {
  chunkFixture,
  documentFixture,
  documentListFixture,
  envelope,
  jsonResponse,
  settingsFixture,
} from '@/test/fixtures'
import { renderApp } from '@/test/render-app'

/**
 * 知识页测试：覆盖"列表 / 详情 / 分块"三条读取路径与两个写入路径。
 *
 * fetch 用统一的假实现按 URL 分发，而不是逐用例 mock 每个模块：
 * 页面用的是真实 api 客户端与真实 Query，这样信封解析、错误分类、重试
 * 这些行为也在测试范围内——它们正是最容易出问题的地方。
 */
const fetchMock = vi.fn()

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  fetchMock.mockReset()
})

function jsonOk(body: unknown, status = 200) {
  return Promise.resolve(jsonResponse(body, status))
}

function chunkList(chunks: ReturnType<typeof chunkFixture>[]) {
  return { items: chunks, total: chunks.length, limit: 100, offset: 0 }
}

async function selectUploadFile(user: ReturnType<typeof userEvent.setup>, file: File) {
  await user.click(screen.getByRole('button', { name: '上传文件' }))
  const dialog = screen.getByRole('dialog', { name: '导入文件' })
  await user.upload(within(dialog).getByLabelText('选择要导入的文件'), file)
}

/** 一个文档 + 一个分块的常规成功路径。 */
function mockHappyPath(doc: DocumentView, content: string) {
  fetchMock.mockImplementation((input: RequestInfo | URL) => {
    const url = String(input)
    if (url === '/api/v1/documents?limit=20&offset=0') {
      return jsonOk(envelope(documentListFixture([doc])))
    }
    if (url === `/api/v1/documents/${doc.id}`) {
      return jsonOk(envelope(doc))
    }
    if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
      return jsonOk(envelope(chunkList([chunkFixture({ content })])))
    }
    return jsonOk({ code: 'NOT_FOUND', message: 'unexpected url: ' + url }, 404)
  })
}

describe('知识库页面', () => {
  it('显示文档列表与分块偏移', async () => {
    const doc = documentFixture({ chunk_count: 1 })
    mockHappyPath(doc, '# 标题\n\n第一段正文。')

    renderApp('/knowledge')

    expect(await screen.findByText('集成测试笔记')).toBeInTheDocument()
    // Markdown 文档的分块按正文渲染，不再显示原始 # 标记。
    expect(await screen.findByRole('heading', { name: '标题' })).toBeInTheDocument()
    expect(screen.getByText('第一段正文。')).toBeInTheDocument()
    expect(screen.getByText('已向量化')).toBeInTheDocument()
    expect(screen.getByText(/\[0, 12\)/)).toBeInTheDocument()
  })

  it('点击左侧列表后右侧展示对应文档', async () => {
    mockHappyPath(documentFixture({ title: '左侧选中的文档' }), '正文')
    const user = userEvent.setup()
    renderApp('/knowledge')

    const list = screen.getByRole('region', { name: '文档列表' })
    const row = await within(list).findByRole('button', { name: /左侧选中的文档/ })
    await user.click(row)

    const detail = screen.getByRole('region', { name: '文档详情' })
    expect(within(detail).getByRole('heading', { name: '左侧选中的文档' })).toBeInTheDocument()
  })

  it('重建向量走 embed 接口，不触发重新切分', async () => {
    const user = userEvent.setup()
    const doc = documentFixture({ chunk_count: 1 })
    const calls: string[] = []
    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      calls.push(`${init?.method ?? 'GET'} ${url}`)
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([doc])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(doc))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture({})])))
      }
      if (url === `/api/v1/documents/${doc.id}/embed`) {
        return jsonOk(envelope({ ...doc, message_id: 'msg-1' }), 202)
      }
      return jsonOk({ code: 'NOT_FOUND', message: 'unexpected url: ' + url }, 404)
    })

    renderApp('/knowledge')

    // 已向量化时按钮文案是"重建向量"：换模型只该重算向量，不该重切分块。
    await user.click(await screen.findByRole('button', { name: '重建向量' }))
    await waitFor(() => {
      expect(calls).toContain(`POST /api/v1/documents/${doc.id}/embed`)
    })
    expect(calls.some((call) => call.includes('/reindex'))).toBe(false)
  })

  it('未配置向量模型时如实显示状态并给出去处', async () => {
    const doc = documentFixture({
      embedding: {
        status: 'skipped',
        model: '',
        dimensions: 0,
        chunks: 0,
        embedded_at: null,
        error_code: 'not_configured',
        error_message: '未配置向量模型，本次只做切分',
      },
    })
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([doc])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(doc))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([])))
      }
      if (url === '/api/v1/settings') {
        return jsonOk(
          envelope(
            settingsFixture({
              embedding: { api_base: '', model: '', api_key_masked: '', api_key_configured: false },
            }),
          ),
        )
      }
      return jsonOk({ code: 'NOT_FOUND', message: 'unexpected url: ' + url }, 404)
    })

    renderApp('/knowledge')

    expect(await screen.findByText('未配置向量模型')).toBeInTheDocument()
  })

  it('空列表显示空态而不是空白面板', async () => {
    fetchMock.mockImplementation(() => jsonOk(envelope(documentListFixture([]))))

    renderApp('/knowledge')

    expect(await screen.findByText('还没有文档')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '文档详情' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '上传文件' })).toBeEnabled()
  })

  it('列表请求失败时给出原因与重试', async () => {
    fetchMock.mockImplementation(() =>
      jsonOk({ code: 'INTERNAL', message: 'database unreachable', request_id: 'req-500' }, 500),
    )

    renderApp('/knowledge')

    expect(await screen.findByText('读取文档列表失败')).toBeInTheDocument()
    expect(screen.getByText(/database unreachable（request_id: req-500）/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
  })

  it('新建笔记提交正文并在成功后刷新列表', async () => {
    const user = userEvent.setup()
    const created = documentFixture({ id: 9, title: '临时会议记录' })
    let listCalls = 0

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/v1/documents?limit=20&offset=0') {
        listCalls += 1
        return jsonOk(envelope(documentListFixture(listCalls === 1 ? [] : [created])))
      }
      if (url === '/api/v1/documents/note') {
        return jsonOk(envelope(created), 201)
      }
      if (url === '/api/v1/documents/9') {
        return jsonOk(envelope(created))
      }
      if (url === '/api/v1/documents/9/chunks?limit=100&offset=0') {
        return jsonOk(envelope(chunkList([chunkFixture({ content: '# 临时会议记录\n\n正文。' })])))
      }
      throw new Error(`unexpected url: ${url} ${init?.method ?? 'GET'}`)
    })

    renderApp('/knowledge')
    await screen.findByText('还没有文档')

    await user.click(screen.getByRole('button', { name: '写笔记' }))
    await user.type(screen.getByLabelText('笔记标题'), '临时会议记录')
    await user.type(screen.getByLabelText('笔记正文'), '# 临时会议记录\n\n正文。')
    await user.click(screen.getByRole('button', { name: '保存笔记' }))

    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/documents/note',
        expect.objectContaining({ method: 'POST' }),
      )
    })
    const noteCall = fetchMock.mock.calls.find(([url]) => String(url) === '/api/v1/documents/note')
    expect(noteCall).toBeDefined()
    expect(JSON.parse(String(noteCall?.[1]?.body))).toEqual({
      title: '临时会议记录',
      content: '# 临时会议记录\n\n正文。',
    })
    // 成功后列表失效并重新请求，新文档出现在列表里。
    // 用 region 限定范围：详情卡片里也有同样的标题，全局查找会命中两个。
    const list = screen.getByRole('region', { name: '文档列表' })
    expect(await within(list).findByText('临时会议记录')).toBeInTheDocument()
  })

  it('打开导入弹框时不触发系统选择器，只有点击选择文件才触发', async () => {
    const user = userEvent.setup()
    mockHappyPath(documentFixture(), '正文')
    const openPicker = vi.spyOn(HTMLInputElement.prototype, 'click')
    renderApp('/knowledge')

    await user.click(await screen.findByRole('button', { name: '上传文件' }))
    const dialog = within(screen.getByRole('dialog', { name: '导入文件' }))
    expect(dialog.getByRole('button', { name: '开始导入' })).toBeDisabled()
    expect(openPicker).not.toHaveBeenCalled()

    await user.click(dialog.getByRole('button', { name: '选择文件' }))
    expect(openPicker).toHaveBeenCalledOnce()
    expect(openPicker.mock.contexts[0]).toBe(dialog.getByLabelText('选择要导入的文件'))
    openPicker.mockRestore()
  })

  it('上传文件走 multipart，大小写扩展名交给后端判断', async () => {
    const user = userEvent.setup()
    mockHappyPath(documentFixture(), '正文')

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const file = new File(['# 笔记\n\n内容'], 'note.md', { type: 'text/markdown' })
    await selectUploadFile(user, file)
    // 选完文件先落到确认面板，不立刻上传。
    expect(await screen.findByText('note.md')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '开始导入' }))

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, init]) => String(url) === '/api/v1/documents' && init?.method === 'POST',
      )
      expect(call).toBeDefined()
      expect(call?.[1]?.body).toBeInstanceOf(FormData)
      expect((call?.[1]?.body as FormData).get('file')).toBeInstanceOf(File)
      // 没填分类标签时不该凭空多出这两个字段。
      expect((call?.[1]?.body as FormData).get('collection')).toBeNull()
      expect((call?.[1]?.body as FormData).get('tags')).toBeNull()
    })
  })

  it('上传前可以填分类与标签，随 multipart 一起提交', async () => {
    const user = userEvent.setup()
    mockHappyPath(documentFixture(), '正文')

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const file = new File(['# 笔记\n\n内容'], 'note.md', { type: 'text/markdown' })
    await selectUploadFile(user, file)
    await user.type(await screen.findByLabelText('文件分类'), '工作')
    await user.type(screen.getByLabelText('文件标签'), 'go, 检索')
    // 标签按逗号切分并去空白，与服务端规则一致。
    await user.click(screen.getByRole('button', { name: '开始导入' }))

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, init]) => String(url) === '/api/v1/documents' && init?.method === 'POST',
      )
      expect(call).toBeDefined()
      const body = call?.[1]?.body as FormData
      expect(body.get('collection')).toBe('工作')
      expect(body.get('tags')).toBe('go,检索')
    })
  })

  it('上传受理后提示后台解析并显示待处理状态', async () => {
    const user = userEvent.setup()
    const pendingDoc = documentFixture({ id: 8, title: '大文件', status: 'pending', chunk_count: 0 })

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/v1/documents' && init?.method === 'POST') {
        // 202 的含义：受理成功，但内容还没处理。
        return jsonOk(
          envelope({ ...pendingDoc, existed: false, message_id: 'msg-202' }),
          202,
        )
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([pendingDoc])))
      }
      if (url === `/api/v1/documents/${pendingDoc.id}`) {
        return jsonOk(envelope(pendingDoc))
      }
      if (url === `/api/v1/documents/${pendingDoc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope({ items: [], total: 0, limit: 100, offset: 0 }))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('大文件')

    const file = new File(['# 大文件'], 'big.md', { type: 'text/markdown' })
    await selectUploadFile(user, file)
    await user.click(await screen.findByRole('button', { name: '开始导入' }))

    // 文案不能再说"已导入"：分块还没生成，说导入完成是在骗用户。
    expect(await screen.findByText(/已受理《大文件》，正在后台解析/)).toBeInTheDocument()
    expect(screen.getAllByText('待处理').length).toBeGreaterThan(0)
  })

  // 回归用例：导入提示说"完成后会自动刷新"，那就必须真的自动出现分块。
  // 之前只有详情在轮询，分块停在旧结果，用户得切走再切回才看得到。
  it('后台处理完成后自动刷新分块，不需要手动切换文档', async () => {
    const pending = documentFixture({ id: 12, status: 'pending', chunk_count: 0 })
    const ready = { ...pending, status: 'ready' as const, chunk_count: 1 }
    let processed = false

    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      const doc = processed ? ready : pending
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([doc])))
      }
      if (url === '/api/v1/documents/12') {
        return jsonOk(envelope(doc))
      }
      if (url === '/api/v1/documents/12/chunks?limit=100&offset=0') {
        return jsonOk(
          envelope(
            processed
              ? chunkList([chunkFixture({ content: '轮询拿到的分块内容' })])
              : { items: [], total: 0, limit: 100, offset: 0 },
          ),
        )
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')

    expect(await screen.findByText('待处理')).toBeInTheDocument()
    expect(screen.queryByText(/轮询拿到的分块内容/)).not.toBeInTheDocument()

    // 后台这一刻处理完了；页面应当在下一轮轮询里自己拿到分块。
    processed = true
    expect(
      await screen.findByText(/轮询拿到的分块内容/, undefined, { timeout: 5000 }),
    ).toBeInTheDocument()
  })

  it('处理中的文档可以取消，请求打到 cancel 接口', async () => {
    const user = userEvent.setup()
    const pending = documentFixture({ id: 11, status: 'pending', chunk_count: 0 })
    const cancelled = { ...pending, status: 'failed' as const, error_code: 'cancelled', error_message: '处理已取消' }

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/v1/documents/11/cancel' && init?.method === 'POST') {
        return jsonOk(envelope(cancelled))
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([pending])))
      }
      if (url === '/api/v1/documents/11') {
        return jsonOk(envelope(pending))
      }
      if (url === '/api/v1/documents/11/chunks?limit=100&offset=0') {
        return jsonOk(envelope({ items: [], total: 0, limit: 100, offset: 0 }))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    await user.click(await screen.findByRole('button', { name: '取消处理' }))

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(([url]) => String(url) === '/api/v1/documents/11/cancel')
      expect(call).toBeDefined()
    })
  })

  it('取消后关闭面板且不发导入请求', async () => {
    const user = userEvent.setup()
    mockHappyPath(documentFixture(), '正文')

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const file = new File(['# 笔记'], 'drop.md', { type: 'text/markdown' })
    await selectUploadFile(user, file)
    await user.click(await screen.findByRole('button', { name: '取消' }))

    expect(screen.queryByText('drop.md')).not.toBeInTheDocument()
    const posted = fetchMock.mock.calls.some(
      ([url, init]) => String(url) === '/api/v1/documents' && init?.method === 'POST',
    )
    expect(posted).toBe(false)
  })

  it('标签超出上限时给出提示并挡住提交', async () => {
    const user = userEvent.setup()
    mockHappyPath(documentFixture(), '正文')

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const file = new File(['# 笔记\n\n内容'], 'note.md', { type: 'text/markdown' })
    await selectUploadFile(user, file)
    // 21 个标签：后端会拒，前端先挡住，不用等一次往返。
    const many = Array.from({ length: 21 }, (_, i) => `t${i}`).join(',')
    await user.type(await screen.findByLabelText('文件标签'), many)

    expect(screen.getByText('最多 20 个标签，当前 21 个')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '开始导入' })).toBeDisabled()
  })
})

/**
 * Phase 5 的文档管理能力：幂等提示、分类标签、筛选、编辑、重索引、删除。
 *
 * 这组用例同样走真实 api 客户端与真实 Query，只把 fetch 换掉：
 * 要验证的正是"点一下之后到底发了什么请求、列表有没有跟着变"。
 */
describe('知识库页面的文档管理', () => {
  it('详情保留分类、标签与可展开的文件信息', async () => {
    const user = userEvent.setup()
    const doc = documentFixture({ collection: '工作', tags: ['go', '检索'] })
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([doc])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(doc))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')

    const detail = within(await screen.findByRole('region', { name: '文档详情' }))
    await user.click(await detail.findByText('文件信息'))
    expect(detail.getByText(/^sha256:/)).toBeVisible()
    expect(detail.getByText('工作')).toBeInTheDocument()
    expect(detail.getByText('go')).toBeInTheDocument()
    expect(detail.getByText('检索')).toBeInTheDocument()
  })

  it('按标签筛选时把条件带进查询参数', async () => {
    const user = userEvent.setup()
    const tagged = documentFixture({ id: 5, title: '带 go 标签', tags: ['go'] })
    const requested: string[] = []
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      requested.push(url)
      if (url === '/api/v1/documents?limit=20&offset=0' || url === '/api/v1/documents?limit=20&offset=0&tag=go') {
        return jsonOk(envelope(documentListFixture([tagged])))
      }
      if (url === `/api/v1/documents/${tagged.id}`) {
        return jsonOk(envelope(tagged))
      }
      if (url === `/api/v1/documents/${tagged.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('带 go 标签')

    await user.click(screen.getByRole('button', { name: '筛选' }))
    await user.type(screen.getByLabelText('按标签筛选'), 'go')
    await user.click(screen.getByRole('button', { name: '应用筛选' }))

    await vi.waitFor(() => {
      expect(requested).toContain('/api/v1/documents?limit=20&offset=0&tag=go')
    })
  })

  it('在弹框编辑分类和标签，取消丢弃草稿，保存只提交改动字段', async () => {
    const user = userEvent.setup()
    const doc = documentFixture({ collection: '开发', tags: ['go'] })
    let current = doc
    const patch = vi.fn()
      .mockImplementationOnce(() => jsonOk({ code: 'INTERNAL_ERROR', message: '暂时无法保存' }, 500))
      .mockImplementationOnce(() => {
        current = { ...doc, tags: ['go', '检索'] }
        return jsonOk(envelope(current))
      })

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === `/api/v1/documents/${doc.id}` && init?.method === 'PATCH') {
        return patch(JSON.parse(String(init.body)))
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([current])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(current))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const editButton = await screen.findByRole('button', { name: '编辑分类与标签' })
    expect(screen.queryByLabelText('文档分类')).not.toBeInTheDocument()
    await user.click(editButton)
    const firstDialog = within(screen.getByRole('dialog', { name: '编辑分类与标签' }))
    expect(firstDialog.getByLabelText('文档标题')).toHaveValue(doc.title)
    expect(firstDialog.getByLabelText('文档标签')).toHaveValue('go')
    const collection = firstDialog.getByLabelText('文档分类')
    expect(collection).toHaveValue('开发')
    await user.type(collection, '草稿')
    await user.click(firstDialog.getByRole('button', { name: '取消' }))
    expect(patch).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    await user.click(editButton)
    const dialog = within(screen.getByRole('dialog', { name: '编辑分类与标签' }))
    expect(dialog.getByLabelText('文档分类')).toHaveValue('开发')
    const tagInput = dialog.getByLabelText('文档标签')
    await user.clear(tagInput)
    await user.type(tagInput, 'go, 检索')
    const save = dialog.getByRole('button', { name: '保存' })
    await user.click(save)
    expect(await dialog.findByRole('alert')).toHaveTextContent('暂时无法保存')
    expect(tagInput).toHaveValue('go, 检索')
    await user.click(save)

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(patch.mock.calls).toEqual([[{ tags: ['go', '检索'] }], [{ tags: ['go', '检索'] }]])
    const detail = screen.getByRole('region', { name: '文档详情' })
    expect(within(detail).getByText('检索')).toBeVisible()
  })

  it('重新切分调用 reindex 接口', async () => {
    const user = userEvent.setup()
    const doc = documentFixture()

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === `/api/v1/documents/${doc.id}/reindex` && init?.method === 'POST') {
        return jsonOk(envelope({ ...doc, chunk_count: 3 }))
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([doc])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(doc))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    await user.click(await screen.findByRole('button', { name: '重新切分' }))

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, init]) => String(url) === `/api/v1/documents/${doc.id}/reindex` && init?.method === 'POST',
      )
      expect(call).toBeDefined()
    })
  })

  it('删除需要二次确认，确认后发出 DELETE 并清空选中', async () => {
    const user = userEvent.setup()
    const doc = documentFixture()
    let deleted = false

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === `/api/v1/documents/${doc.id}` && init?.method === 'DELETE') {
        deleted = true
        return jsonOk(envelope({ id: doc.id }))
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture(deleted ? [] : [doc])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(doc))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const deleteButton = await screen.findByRole('button', { name: '删除' })
    await user.click(deleteButton)
    const dialog = screen.getByRole('dialog', { name: '删除文档' })
    expect(dialog).toHaveTextContent(doc.title)
    expect(dialog).toHaveTextContent('原始文件会一并移除')
    expect(within(dialog).getByRole('button', { name: '取消' })).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(deleteButton).toHaveFocus()
    expect(deleted).toBe(false)

    await user.click(deleteButton)
    await user.click(within(screen.getByRole('dialog', { name: '删除文档' })).getByRole('button', { name: '确认删除' }))

    await vi.waitFor(() => {
      expect(deleted).toBe(true)
    })
    // 删掉当前文档后不能继续显示它——否则会立刻触发一次注定 404 的详情请求。
    expect(await screen.findByText('还没有文档')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '文档详情' })).not.toBeInTheDocument()
  })

  it('上传命中既有文档时提示没有重复入库', async () => {
    const user = userEvent.setup()
    const existing = documentFixture({ id: 3, title: '已有内容' })

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/v1/documents' && init?.method === 'POST') {
        return jsonOk(envelope({ ...existing, existed: true }))
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([existing])))
      }
      if (url === `/api/v1/documents/${existing.id}`) {
        return jsonOk(envelope(existing))
      }
      if (url === `/api/v1/documents/${existing.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('已有内容')

    const file = new File(['# 标题'], 'copy.md', { type: 'text/markdown' })
    await selectUploadFile(user, file)
    await user.click(await screen.findByRole('button', { name: '开始导入' }))

    expect(await screen.findByText(/已复用《已有内容》，没有重复入库/)).toBeInTheDocument()
  })
})
