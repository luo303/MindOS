import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { Input } from '../input'

function setupPicker(type: string, overrides: Partial<React.ComponentProps<typeof Input>> = {}) {
  render(<Input type={type} aria-label="选择日期" {...overrides} />)
  const input = screen.getByLabelText('选择日期')
  const showPicker = vi.fn()
  Object.defineProperty(input, 'showPicker', { configurable: true, value: showPicker })
  return { input, showPicker }
}

describe('日期输入框', () => {
  it.each(['date', 'datetime-local', 'month', 'time', 'week'])('点击 %s 输入框本身即可唤起原生选择器', (type) => {
    const { input, showPicker } = setupPicker(type)
    fireEvent.click(input)
    expect(showPicker).toHaveBeenCalledOnce()
  })

  it('普通文本输入保留调用方的点击事件，不调用选择器', () => {
    const onClick = vi.fn()
    const { input, showPicker } = setupPicker('text', { onClick })
    fireEvent.click(input)
    expect(onClick).toHaveBeenCalledOnce()
    expect(showPicker).not.toHaveBeenCalled()
  })

  it('不为只读日期框打开选择器', () => {
    const { input, showPicker } = setupPicker('date', { readOnly: true })
    fireEvent.click(input)
    expect(showPicker).not.toHaveBeenCalled()
  })

  it('浏览器拒绝脚本打开时不取消原生点击', () => {
    const { input, showPicker } = setupPicker('date')
    showPicker.mockImplementation(() => { throw new DOMException('no user activation', 'NotAllowedError') })
    expect(fireEvent.click(input)).toBe(true)
  })
})
