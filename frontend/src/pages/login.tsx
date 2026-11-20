import { useState, type FormEvent } from 'react'
import { Link, Navigate, useSearchParams } from 'react-router'

import { describeError } from '@/api/errors'
import { safeNext, useLogin, useMe } from '@/app/auth'
import { AuthShell } from '@/pages/auth-shell'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

/**
 * 登录页。
 *
 * 表单用受控组件手写校验，不引入表单库：这里只有两个字段，
 * 引一个依赖换来的只是多一层概念。
 */
export function LoginPage() {
  const [params] = useSearchParams()
  const next = safeNext(params.get('next'))
  const me = useMe()
  const loginMutation = useLogin()

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')

  // 已经登录的人直接进目标页：避免"登录页闪一下再跳走"。
  if (me.data) {
    return <Navigate to={next} replace />
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    loginMutation.mutate({ email, password })
  }

  return (
    <AuthShell
      title="登录"
      footer={
        <>
          还没有账号？
          <Link className="text-foreground underline-offset-4 hover:underline" to={'/register?next=' + encodeURIComponent(next)}>
            注册
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
          <label className="text-sm font-medium" htmlFor="password">
            密码
          </label>
          <Input
            id="password"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </div>

        {loginMutation.error ? (
          <p className="text-destructive text-sm" role="alert">
            {describeError(loginMutation.error)}
          </p>
        ) : null}

        <Button className="w-full" type="submit" disabled={loginMutation.isPending}>
          {loginMutation.isPending ? '登录中…' : '登录'}
        </Button>
      </form>
    </AuthShell>
  )
}
