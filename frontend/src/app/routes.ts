export const workspaceHomePath = '/spaces'

export const workspaceSections = [
  { path: '', label: '创意', icon: 'sparkle', description: '通过对话把想法变成 Agent。', group: '工作空间' },
  { path: 'agents', label: 'Agents', icon: 'bot', description: '配置助手的模型、指令与工具，发布可复用的版本。', group: '定义与资源' },
  { path: 'skills', label: 'Skills', icon: 'layers', description: '编写方法与资源，供 Agent 绑定和复用。', group: '定义与资源' },
  { path: 'evaluation/datasets', label: '评测集', icon: 'list', description: '管理版本化评测集，编排每条用例的单轮和多轮输入。', group: '评测' },
  { path: 'evaluation/experiments', label: '实验', icon: 'flask', description: '选择评测集和 Agent 版本发起实验，查看进度、指标及会话回放。', group: '评测' },
  { path: 'models', label: '模型配置', icon: 'chip', description: '管理可用模型、上下文容量与生成参数。', group: '空间管理' },
  { path: 'members', label: '空间成员', icon: 'user', description: '查看协作成员，管理成员资格。', group: '空间管理' },
  { path: 'openapi', label: 'OpenAPI', icon: 'key', description: '签发和管理空间级 Bearer Token，供外部服务调用会话接口。', group: '空间管理', ownerOnly: true },
] as const

export function spacePath(space: string, section = '') {
  return `/spaces/${encodeURIComponent(space)}${section ? `/${section}` : ''}`
}
export function agentSessionsPath(space: string, agent: string, session = '') {
  const base = spacePath(space, `agents/${encodeURIComponent(agent)}/sessions`)
  return session ? `${base}/${encodeURIComponent(session)}` : base
}
export function creativitySessionPath(space: string, session: string) {
  return spacePath(space, `creativity/sessions/${encodeURIComponent(session)}`)
}
export function legacySpacePath(search: string) {
  const params = new URLSearchParams(search), space = params.get('space'), session = params.get('session')
  const run = params.get('run')
  return space ? session ? `${spacePath(space, 'sessions')}/${encodeURIComponent(session)}${run ? `?run=${encodeURIComponent(run)}` : ''}` : spacePath(space) : null
}
