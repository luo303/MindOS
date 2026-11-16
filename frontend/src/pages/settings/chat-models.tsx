import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, Loader2, PlugZap, Plus, Star, Trash2, XCircle } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import {
  chatModelKeys,
  createChatModel,
  deleteChatModel,
  fetchChatModels,
  setDefaultChatModel,
  testChatModel,
  updateChatModel,
} from '@/api/chat-models'
import { describeError } from '@/api/errors'
import type { ChatModel, ChatModelInput } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'

type Draft = {
  name: string
  base_url: string
  model: string
  /** 编辑态留空表示"不改"；新增态留空表示该端点不需要 Key */
  api_key: string
  temperature: string
  max_tokens: string
}

const emptyDraft: Draft = {
  name: '',
  base_url: '',
  model: '',
  api_key: '',
  temperature: '',
  max_tokens: '',
}

/**
 * 对话模型管理。
 *
 * 为什么单独抽一个文件：设置页本身已经有系统状态与运行配置两块，
 * 再塞一个带表单的 CRUD 会让那个文件迅速越过 500 行。
 */
export function ChatModelsCard() {
  const queryClient = useQueryClient()
  const modelsQuery = useQuery({
    queryKey: chatModelKeys.all,
    queryFn: ({ signal }) => fetchChatModels(signal),
  })
  const models = modelsQuery.data ?? []

  const [editing, setEditing] = useState<ChatModel | 'new' | null>(null)
  const [draft, setDraft] = useState<Draft>(emptyDraft)
  const [clearKey, setClearKey] = useState(false)
  const [confirmingDelete, setConfirmingDelete] = useState<number | null>(null)
  const [probe, setProbe] = useState<{ id: number; ok: boolean; text: string } | null>(null)

  const invalidate = () => queryClient.invalidateQueries({ queryKey: chatModelKeys.all })

  const createMutation = useMutation({ mutationFn: createChatModel })
  const updateMutation = useMutation({
    mutationFn: (vars: { id: number; input: ChatModelInput }) => updateChatModel(vars.id, vars.input),
  })
  const deleteMutation = useMutation({ mutationFn: deleteChatModel })
  const defaultMutation = useMutation({ mutationFn: setDefaultChatModel })
  const testMutation = useMutation({ mutationFn: (id: number) => testChatModel(id) })

  const openCreate = () => {
    setEditing('new')
    setDraft(emptyDraft)
    setClearKey(false)
  }

  const openEdit = (model: ChatModel) => {
    setEditing(model)
    setDraft({
      name: model.name,
      base_url: model.base_url,
      model: model.model,
      api_key: '',
      temperature: model.temperature === null ? '' : String(model.temperature),
      max_tokens: model.max_tokens === null ? '' : String(model.max_tokens),
    })
    // 关键：绝不把 hint 回填进密码框，否则一保存就把真 Key 覆盖成四个星号。
    setClearKey(false)
  }

  const closeForm = () => {
    setEditing(null)
    setDraft(emptyDraft)
    setClearKey(false)
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()

    let apiKey: string | undefined
    if (clearKey) {
      apiKey = ''
    } else if (draft.api_key.trim() !== '') {
      apiKey = draft.api_key.trim()
    }

    const input: ChatModelInput = {
      name: draft.name.trim(),
      base_url: draft.base_url.trim(),
      model: draft.model.trim(),
      temperature: draft.temperature.trim() === '' ? null : Number(draft.temperature),
      max_tokens: draft.max_tokens.trim() === '' ? null : Number(draft.max_tokens),
      ...(apiKey === undefined ? {} : { api_key: apiKey }),
    }

    const onSuccess = () => {
      closeForm()
      void invalidate()
    }
    if (editing === 'new') {
      createMutation.mutate({ ...input, is_default: models.length === 0 }, { onSuccess })
    } else if (editing) {
      updateMutation.mutate({ id: editing.id, input }, { onSuccess })
    }
  }

  const saving = createMutation.isPending || updateMutation.isPending
  const formError = createMutation.error ?? updateMutation.error

  return (
    <Card>
      <CardHeader>
        <CardTitle>对话模型</CardTitle>
        <CardDescription>
          问答用哪个模型由你决定，配置存在数据库里，改完下一个问题就生效，不需要重启。
          密钥加密存储，界面只显示末四位。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {modelsQuery.isPending ? (
          <div className="space-y-2">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : modelsQuery.error ? (
          <p role="alert" className="text-destructive text-sm">
            读取失败：{describeError(modelsQuery.error)}
          </p>
        ) : models.length === 0 ? (
          <div className="rounded-lg border border-dashed px-3 py-6 text-center">
            <p className="text-sm font-medium">还没有配置</p>
            <p className="text-muted-foreground mx-auto mt-1 max-w-md text-xs">
              填一个 OpenAI 兼容的地址与模型名即可，例如讯飞星辰
              <code className="bg-muted mx-1 rounded px-1">https://maas-api.cn-huabei-1.xf-yun.com/v2</code>
              + <code className="bg-muted rounded px-1">spark-x2.6</code>。
              地址填到版本号为止，<strong>不要带 /chat/completions</strong>。
            </p>
          </div>
        ) : (
          <ul className="divide-y">
            {models.map((model) => (
              <li key={model.id} className="flex flex-wrap items-start justify-between gap-2 py-3 first:pt-0">
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="text-sm font-medium">{model.name}</span>
                    {model.is_default ? (
                      <Badge variant="outline" className="gap-1">
                        <Star className="size-3" aria-hidden="true" />
                        默认
                      </Badge>
                    ) : null}
                    {model.api_key_configured ? (
                      <Badge variant="outline">Key ****{model.api_key_hint || '（短）'}</Badge>
                    ) : (
                      <Badge variant="outline">未填 Key</Badge>
                    )}
                  </div>
                  <p className="text-muted-foreground text-xs break-all">
                    {model.model} · {model.base_url}
                  </p>
                  {probe && probe.id === model.id ? (
                    <p
                      role="status"
                      className={
                        probe.ok ? 'text-emerald-600 dark:text-emerald-400 text-xs' : 'text-destructive text-xs'
                      }
                    >
                      {probe.text}
                    </p>
                  ) : null}
                </div>
                <div className="flex flex-wrap items-center gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => {
                      testMutation.mutate(model.id, {
                        onSuccess: (result) =>
                          setProbe({
                            id: model.id,
                            ok: true,
                            text: `连通 · ${result.model} · ${result.latency_ms} ms · ${result.reply}`,
                          }),
                        onError: (error) =>
                          setProbe({ id: model.id, ok: false, text: `失败：${describeError(error)}` }),
                      })
                    }}
                    disabled={testMutation.isPending && testMutation.variables === model.id}
                  >
                    {testMutation.isPending && testMutation.variables === model.id ? (
                      <Loader2 className="animate-spin" aria-hidden="true" />
                    ) : (
                      <PlugZap aria-hidden="true" />
                    )}
                    测试
                  </Button>
                  {model.is_default ? null : (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => defaultMutation.mutate(model.id, { onSuccess: () => void invalidate() })}
                      disabled={defaultMutation.isPending}
                    >
                      设为默认
                    </Button>
                  )}
                  <Button variant="ghost" size="sm" onClick={() => openEdit(model)}>
                    编辑
                  </Button>
                  {confirmingDelete === model.id ? (
                    <>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="text-destructive"
                        onClick={() =>
                          deleteMutation.mutate(model.id, {
                            onSuccess: () => {
                              setConfirmingDelete(null)
                              setProbe(null)
                              void invalidate()
                            },
                          })
                        }
                        disabled={deleteMutation.isPending}
                      >
                        确认删除
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => setConfirmingDelete(null)}>
                        取消
                      </Button>
                    </>
                  ) : (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setConfirmingDelete(model.id)}
                      aria-label={`删除 ${model.name}`}
                    >
                      <Trash2 aria-hidden="true" />
                    </Button>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}

        {editing ? (
          <form onSubmit={submit} className="bg-muted/30 space-y-3 rounded-lg border p-3">
            <p className="text-sm font-medium">
              {editing === 'new' ? '新增对话模型' : `编辑：${editing.name}`}
            </p>
            <div className="grid gap-2 sm:grid-cols-2">
              <Field label="名称（唯一）">
                <Input
                  value={draft.name}
                  onChange={(event) => setDraft({ ...draft, name: event.target.value })}
                  placeholder="例如：讯飞 Spark"
                  required
                  maxLength={64}
                />
              </Field>
              <Field label="模型 ID">
                <Input
                  value={draft.model}
                  onChange={(event) => setDraft({ ...draft, model: event.target.value })}
                  placeholder="spark-x2.6"
                  required
                  maxLength={128}
                />
              </Field>
              <Field label="Base URL（到版本号为止，不要带 /chat/completions）" className="sm:col-span-2">
                <Input
                  value={draft.base_url}
                  onChange={(event) => setDraft({ ...draft, base_url: event.target.value })}
                  placeholder="https://maas-api.cn-huabei-1.xf-yun.com/v2"
                  required
                />
              </Field>
              <Field label="API Key">
                <Input
                  type="password"
                  value={draft.api_key}
                  onChange={(event) => setDraft({ ...draft, api_key: event.target.value })}
                  placeholder={
                    editing === 'new' ? '该端点不需要就留空' : '留空表示不修改'
                  }
                  disabled={clearKey}
                  autoComplete="off"
                />
              </Field>
              <div className="flex flex-col justify-end gap-1">
                {editing === 'new' ? null : (
                  <label className="flex items-center gap-2 text-xs">
                    <input
                      type="checkbox"
                      checked={clearKey}
                      onChange={(event) => setClearKey(event.target.checked)}
                    />
                    清除已保存的 Key
                  </label>
                )}
                <p className="text-muted-foreground text-xs">
                  {editing !== 'new' && editing.api_key_configured
                    ? `已保存：****${editing.api_key_hint || '（短）'}`
                    : '密钥只存在本地数据库，加密保存'}
                </p>
              </div>
              <Field label="temperature（可空）">
                <Input
                  value={draft.temperature}
                  onChange={(event) => setDraft({ ...draft, temperature: event.target.value })}
                  placeholder="0 ~ 2，留空用厂商默认"
                  inputMode="decimal"
                />
              </Field>
              <Field label="max_tokens（可空）">
                <Input
                  value={draft.max_tokens}
                  onChange={(event) => setDraft({ ...draft, max_tokens: event.target.value })}
                  placeholder="留空用厂商默认"
                  inputMode="numeric"
                />
              </Field>
            </div>
            {formError ? (
              <p role="alert" className="text-destructive text-xs break-words">
                {describeError(formError)}
              </p>
            ) : null}
            <div className="flex items-center gap-2">
              <Button type="submit" size="sm" disabled={saving}>
                {saving ? <Loader2 className="animate-spin" aria-hidden="true" /> : <CheckCircle2 aria-hidden="true" />}
                {saving ? '保存中' : '保存'}
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={closeForm}>
                <XCircle aria-hidden="true" />
                取消
              </Button>
            </div>
          </form>
        ) : (
          <Button variant="outline" size="sm" onClick={openCreate}>
            <Plus aria-hidden="true" />
            添加模型
          </Button>
        )}
      </CardContent>
    </Card>
  )
}

function Field({
  label,
  children,
  className,
}: {
  label: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <label className={`space-y-1 ${className ?? ''}`}>
      <span className="text-muted-foreground block text-xs">{label}</span>
      {children}
    </label>
  )
}
