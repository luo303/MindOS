import type { AskCitationItem, AskDonePayload, AskRetrievalItem, AskVerdict } from '@/api/types'

/**
 * 一轮问答在界面上的阶段。
 *
 * 与后端 verdict 分开：verdict 是"库里的裁决"，phase 是"屏幕该显示什么"。
 * 两者不是一一对应——ungrounded 在界面上是"撤回"，refused 是"没依据"，
 * 而 streaming/settled 是纯前端概念（后端只在结束时给 verdict）。
 */
export type TurnPhase = 'streaming' | 'settled' | 'refused' | 'withdrawn' | 'failed'

export type Turn = {
  id: string
  /** 后端 user message id；审批按它挂到对应轮次。 */
  messageId?: number
  question: string
  /** 已收到的正文。撤回时会被替换成提示文案。 */
  answer: string
  phase: TurnPhase
  verdict?: AskVerdict
  /** 后端给的裁决原因（no_evidence / no_citation / unknown_citation…） */
  reason?: string
  hits: AskRetrievalItem[]
  citations: AskCitationItem[]
  error?: { code: string; message: string; detail?: string; requestId?: string }
  done?: AskDonePayload
}

/** 库里没有依据时的固定文案。与后端 qa_runs.answer 的措辞保持一致。 */
export const REFUSAL_TEXT = '知识库里没有找到可以回答这个问题的依据。可以换个问法，或者先把相关资料导入知识库。'

/** 模型没给有效引用时的提示：比拒答多一句"为什么"，否则用户会以为是自己问错了。 */
export const WITHDRAWN_TEXT = '这次回答没有给出可核对的引用，已撤回。可以换个问法再试。'

/** 用户主动停止。 */
export const STOPPED_TEXT = '已停止生成。'

/**
 * 依据面板里一条片段的标签。
 *
 * seq 是 0 基的段落序号，展示时 +1：人对"第 0 段"没有直觉。
 */
export function sectionLabel(seq: number): string {
  return `第 ${seq + 1} 段`
}

/** 相似度显示成两位小数。0 表示这条只有全文命中、没有向量分数。 */
export function similarityLabel(similarity: number): string {
  if (similarity <= 0) {
    return '仅字面命中'
  }
  return `相似度 ${similarity.toFixed(2)}`
}

/** 跳回知识页的深链：doc 定位文档，chunk 定位段落。 */
export function knowledgeHref(documentId: number, seq: number): string {
  return `/knowledge?doc=${documentId}&chunk=${seq}`
}
