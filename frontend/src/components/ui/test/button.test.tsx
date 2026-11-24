import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { Button } from '../button'

afterEach(() => vi.unstubAllGlobals())

describe('按钮光斑', () => {
  it('光斑随鼠标移动，取消或移出时清除，不倾斜按钮', () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true }))
    const onPointerMove = vi.fn()
    render(<Button variant="spotlight" onPointerMove={onPointerMove}>新对话</Button>)
    const button = screen.getByRole('button', { name: '新对话' })
    vi.spyOn(button, 'getBoundingClientRect').mockReturnValue(new DOMRect(10, 20, 200, 40))

    fireEvent.pointerMove(button, { pointerType: 'mouse', clientX: 60, clientY: 40 })
    expect(button).toHaveAttribute('data-lit')
    expect(button.style.getPropertyValue('--glass-x')).toBe('25%')
    expect(button.style.getPropertyValue('--glass-y')).toBe('50%')
    expect(button.style.getPropertyValue('--glass-rx')).toBe('')
    expect(button.style.getPropertyValue('--glass-ry')).toBe('')

    fireEvent.pointerMove(button, { pointerType: 'mouse', clientX: 210, clientY: 60 })
    expect(button.style.getPropertyValue('--glass-x')).toBe('100%')
    expect(button.style.getPropertyValue('--glass-y')).toBe('100%')
    expect(onPointerMove).toHaveBeenCalledTimes(2)
    fireEvent.pointerCancel(button)
    expect(button).not.toHaveAttribute('data-lit')

    fireEvent.pointerMove(button, { pointerType: 'mouse', clientX: 60, clientY: 40 })
    fireEvent.pointerLeave(button)
    expect(button).not.toHaveAttribute('data-lit')
  })

  it.each([
    { name: '触屏', matches: true, pointerType: 'touch', disabled: false },
    { name: '减少动态效果', matches: false, pointerType: 'mouse', disabled: false },
    { name: '禁用', matches: true, pointerType: 'mouse', disabled: true },
  ])('$name 时不启用光斑', ({ matches, pointerType, disabled }) => {
    vi.stubGlobal('matchMedia', () => ({ matches }))
    render(<Button variant="spotlight" disabled={disabled}>上传文件</Button>)
    const button = screen.getByRole('button', { name: '上传文件' })
    vi.spyOn(button, 'getBoundingClientRect').mockReturnValue(new DOMRect(10, 20, 200, 40))

    fireEvent.pointerMove(button, { pointerType, clientX: 60, clientY: 40 })
    expect(button).not.toHaveAttribute('data-lit')
  })
})
