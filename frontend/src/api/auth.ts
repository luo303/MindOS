import { apiRequest } from '@/api/client'

/**
 * 账号与会话相关的接口。
 *
 * 登录态存在 HttpOnly Cookie 里，前端**拿不到也不需要** token：
 * 这里只负责把信封里的用户信息交给查询缓存。
 */
export type AuthUser = {
  id: number
  email: string
  display_name: string
  created_at: string
  last_login_at: string | null
}

/** 登录/注册成功的响应。token 不在里面——它在 Cookie 里。 */
export type SessionInfo = {
  user: AuthUser
  expires_at: string
}

/** 查询键集中在一处：失效、重置、测试预置缓存都只认这一个常量。 */
export const authQueryKeys = {
  me: ['auth', 'me'] as const,
}

export function fetchMe(signal?: AbortSignal): Promise<AuthUser> {
  return apiRequest<AuthUser>('/auth/me', { signal })
}

export type LoginInput = {
  email: string
  password: string
}

export function login(input: LoginInput): Promise<SessionInfo> {
  return apiRequest<SessionInfo>('/auth/login', { method: 'POST', body: input })
}

export type RegisterInput = LoginInput & {
  display_name?: string
}

export function register(input: RegisterInput): Promise<SessionInfo> {
  return apiRequest<SessionInfo>('/auth/register', { method: 'POST', body: input })
}

export function logout(): Promise<{ logged_out: boolean }> {
  return apiRequest('/auth/logout', { method: 'POST' })
}

export function logoutAll(): Promise<{ logged_out_others: boolean }> {
  return apiRequest('/auth/logout-all', { method: 'POST' })
}

export type ChangePasswordInput = {
  current_password: string
  new_password: string
}

export function changePassword(input: ChangePasswordInput): Promise<{ changed: boolean }> {
  return apiRequest('/auth/password', { method: 'PUT', body: input })
}
