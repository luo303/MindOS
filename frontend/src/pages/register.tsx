import { useState, type FormEvent } from 'react'
import { Link, Navigate, useSearchParams } from 'react-router'

import { describeError } from '@/api/errors'
import { safeNext, useMe, useRegister } from '@/app/auth'
import { AuthShell } from '@/pages/auth-shell'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

/**
 * 注册页。
 *
 * 密码下限与服务端一致（8 字节），但**不在这里做复杂度规则**：
 * 前后端各写一套强度规则，最后一定有一边说得不一样。
 * 真正的长度校验在服务端，这里只是提前提示。
 */
export function RegisterPage() {
  const [params] = useSearchParams()
  const next = safeNext(params.get('next'))
  const me = useMe()
  const registerMutation = useRegister()

  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [localError, setLocalError] = useState('')

  if (me.data) {
    return <Navigate to={next} replace />
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setLocalError('')
    if (password !== confirm) {
      setLocalError('两次输入的密码不一致')
      return
    }
    registerMutation.mutate({
      email,
      password,
      ...(displayName.trim() ? { display_name: displayName.trim() } : {}),
    })
  }

  return (
    <AuthShell
      title="注册"
      description="创建一个账号。知识库、问答记录与模型配置只属于这个账号。"
      footer={
        <>
          已有账号？
          <Link className="text-foreground underline-offset-4 hover:underline" to={'/login?next=' + encodeURIComponent(next)}>
            登录
          </Link>
        </>
      }
    >
      <form className="space-y-4" onSubmit={submit}>
        <div className="space-y-1.5">
          <label className="text-sm font-medium" htmlFor="email">
            邮箱
          </label>
          <Input
            id="email"
            type="email"
            autoComplete="email"
            required
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium" htmlFor="display-name">
            昵称（可选）
          </label>
          <Input
            id="display-name"
            autoComplete="nickname"
            value={displayName}
            onChange={(event) => setDisplayName(event.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium" htmlFor="password">
            密码
          </label>
          <Input
            id="password"
            type="password"
            autoComplete="new-password"
            required
            minLength={8}
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
          <p className="text-muted-foreground text-xs">至少 8 个字符。</p>
        </div>
        <div className="space-y-1.5">
          <label className="text-sm font-medium" htmlFor="confirm-password">
            确认密码
          </label>
          <Input
            id="confirm-password"
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(event) => setConfirm(event.target.value)}
          />
        </div>

        {localError ? (
          <p className="text-destructive text-sm" role="alert">
            {localError}
          </p>
        ) : null}
        {registerMutation.error ? (
          <p className="text-destructive text-sm" role="alert">
            {describeError(registerMutation.error)}
          </p>
        ) : null}

        <Button className="w-full" type="submit" disabled={registerMutation.isPending}>
          {registerMutation.isPending ? '创建中…' : '创建账号'}
        </Button>
      </form>
    </AuthShell>
  )
}
