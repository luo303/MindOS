import type { PreferenceKey } from '@/api/memory'

export const preferenceKeys = ['answer_language', 'answer_detail', 'code_example_language'] as const

export const preferenceLabels: Record<PreferenceKey, string> = {
  answer_language: '回复语言', answer_detail: '回答详略', code_example_language: '代码示例语言',
}

export const preferenceValues: Record<PreferenceKey, { value: string; label: string }[]> = {
  answer_language: [{ value: 'zh', label: '中文' }, { value: 'en', label: '英语' }, { value: 'ja', label: '日语' }],
  answer_detail: [{ value: 'concise', label: '简洁' }, { value: 'detailed', label: '详细' }],
  code_example_language: [
    { value: 'go', label: 'Go' }, { value: 'rust', label: 'Rust' }, { value: 'python', label: 'Python' },
    { value: 'typescript', label: 'TypeScript' }, { value: 'javascript', label: 'JavaScript' },
    { value: 'java', label: 'Java' }, { value: 'cpp', label: 'C++' }, { value: 'csharp', label: 'C#' },
  ],
}

export function preferenceValueLabel(key: string, value: string) {
  return preferenceValues[key as PreferenceKey]?.find((option) => option.value === value)?.label ?? value
}

export const memoryCategories = [
  { value: 'profile', label: '个人画像' }, { value: 'preference', label: '偏好' },
  { value: 'habit', label: '习惯' }, { value: 'rule', label: '协作规则' },
  { value: 'project', label: '项目' }, { value: 'goal', label: '目标' },
  { value: 'state', label: '近期状态' }, { value: 'other', label: '其他' },
]

export function categoryLabel(category: string) {
  return memoryCategories.find((option) => option.value === category)?.label ?? category ?? '偏好'
}

export function memorySourceLink(conversationId: number, messageId: number) {
  return `/chat?conversation=${conversationId}&message=${messageId}`
}
