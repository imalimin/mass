export const workspaceHomePath = '/spaces'

export const workspaceSections = [
  { path: '', label: '空间概览', description: '查看空间入口，开始任务或管理协作资源。', group: '工作空间' },
  { path: 'agents', label: 'Agents', description: '配置助手的模型、指令与工具，发布可复用的版本。', group: '定义与资源' },
  { path: 'skills', label: 'Skills', description: '编写方法与资源，供 Agent 绑定和复用。', group: '定义与资源' },
  { path: 'models', label: '模型配置', description: '管理可用模型、上下文容量与生成参数。', group: '空间管理' },
  { path: 'members', label: '空间成员', description: '查看协作成员，管理成员资格。', group: '空间管理' },
] as const

export function spacePath(space: string, section = '') {
  return `/spaces/${encodeURIComponent(space)}${section ? `/${section}` : ''}`
}
export function agentSessionsPath(space: string, agent: string, session = '') {
  const base = spacePath(space, `agents/${encodeURIComponent(agent)}/sessions`)
  return session ? `${base}/${encodeURIComponent(session)}` : base
}
export function legacySpacePath(search: string) {
  const params = new URLSearchParams(search), space = params.get('space'), session = params.get('session')
  const run = params.get('run')
  return space ? session ? `${spacePath(space, 'sessions')}/${encodeURIComponent(session)}${run ? `?run=${encodeURIComponent(run)}` : ''}` : spacePath(space) : null
}
