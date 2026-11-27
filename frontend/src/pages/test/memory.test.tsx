import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { envelope, jsonResponse } from '@/test/fixtures'
import { renderApp } from '@/test/render-app'

afterEach(() => vi.unstubAllGlobals())

it('可以在待确认列表保存建议，并在记忆列表显示生效值', async () => {
  let accepted = false
  const candidate = {
    id: 1, conversation_id: 2, message_id: 3, key: 'code_example_language',
    value: 'go', evidence: '以后代码示例默认用 Go', base_version: 0,
  }
  const fetcher = vi.fn(async (input: string, init?: RequestInit) => {
    if (input.endsWith('/memory-candidates/1/decision')) {
      expect(init?.body).toBe(JSON.stringify({ decision: 'accept' }))
      accepted = true
      return jsonResponse(envelope({ decision: 'accept' }))
    }
    if (input.endsWith('/memory-candidates')) {
      return jsonResponse(envelope({ items: accepted ? [] : [candidate] }))
    }
    if (input.endsWith('/memories')) {
      return jsonResponse(envelope({ items: accepted ? [{ ...candidate, status: 'active', version: 1 }] : [] }))
    }
    throw new Error(`unexpected request: ${input}`)
  })
  vi.stubGlobal('fetch', fetcher)
  renderApp('/memory')
  expect(await screen.findByText('以后代码示例默认用 Go')).toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: '保存为记忆' }))
  await waitFor(() => expect(screen.queryByText('以后代码示例默认用 Go')).not.toBeInTheDocument())
  expect(within(screen.getByRole('region', { name: '已保存的偏好' })).getByText('Go')).toBeInTheDocument()
})
