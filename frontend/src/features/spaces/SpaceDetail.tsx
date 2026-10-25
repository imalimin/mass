import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { list, request, type Schema } from '../../lib/api'

type Space = Schema['Space']; type Credential = Schema['Credential']; type Model = Schema['Model']
export function Members({ space, user, onLeave }: { space: Space; user: Schema['User']; onLeave: () => void }) {
  const client = useQueryClient(), base = `/spaces/${space.id}`, [username, setUsername] = useState('')
  const members = useQuery({ queryKey: ['private', space.id, 'members'], queryFn: ({ signal }) => list<Schema['Member']>(`${base}/members`, signal), refetchInterval: q => q.state.error ? false : 5000 })
  const refresh = () => { void client.invalidateQueries({ queryKey: ['private', space.id] }); void client.invalidateQueries({ queryKey: ['spaces'] }) }
  const add = useMutation({ mutationFn: () => request(`${base}/members`, 'POST', { username }), onSuccess: () => { setUsername(''); refresh() } })
  const remove = useMutation({ mutationFn: (id: string) => request(`${base}/members/${id}`, 'DELETE'), onSuccess: (_, id) => { if (id === user.id) { client.removeQueries({ queryKey: ['private', space.id] }); onLeave() }; refresh() } })
  return <><section className="panel"><h2>成员列表</h2><p className="muted">管理员可邀请已有账号加入空间。普通成员可查看成员，也可退出空间。</p>
    {members.data?.map(m => <div className="resource-row" key={m.id}><span>{m.username} <small>{m.is_owner ? '管理员 · 不可移除' : '成员'}</small></span>{!m.is_owner && (space.is_owner || user.id === m.id) && <button className="secondary" disabled={remove.isPending} onClick={() => remove.mutate(m.id)}>{user.id === m.id ? '退出空间' : `移除 ${m.username}`}</button>}</div>)}
    {space.is_owner && <form className="inline-form" onSubmit={e => { e.preventDefault(); add.mutate() }}><label>已有账号用户名<input value={username} onChange={e => setUsername(e.target.value)} required /></label><button disabled={add.isPending}>添加成员</button></form>}
    {[members.error, add.error, remove.error].filter(Boolean).map((e, i) => <p key={i} role="alert">{e?.message}</p>)}</section>
    </>
}
export function Credentials({ space }: { space: Space }) {
  const client = useQueryClient(), base = `/spaces/${space.id}/credentials`
  const query = useQuery({ queryKey: ['private', space.id, 'credentials'], queryFn: ({ signal }) => list<Credential>(base, signal) })
  const [editing, setEditing] = useState<Credential | null>(null)
  return <section className="panel"><h2>凭证列表</h2><p className="muted">凭证加密保存，保存后不会再次显示密钥。</p>{query.data?.map(c => <div className="resource-row" key={c.id}><span>{c.name} <small>{c.enabled ? '已启用' : '已禁用'}</small></span><button className="secondary" onClick={() => setEditing(c)}>管理 {c.name}</button></div>)}{query.error && <p role="alert">{query.error.message}</p>}
    <CredentialForm key={editing ? `${editing.id}-${editing.revision}` : 'new'} base={base} editing={editing} onSaved={() => { setEditing(null); void client.invalidateQueries({ queryKey: ['private', space.id] }) }} onCancel={() => setEditing(null)} />
  </section>
}
function CredentialForm({ base, editing, onSaved, onCancel }: { base: string; editing: Credential | null; onSaved: () => void; onCancel: () => void }) {
  const [name, setName] = useState(editing?.name ?? ''), [secret, setSecret] = useState(''), [enabled, setEnabled] = useState(editing?.enabled ?? true)
  const save = useMutation({ mutationFn: () => request(base + (editing ? `/${editing.id}` : ''), editing ? 'PUT' : 'POST', { name, secret, enabled, ...(editing ? { revision: editing.revision } : {}) } satisfies Schema['CredentialInput']), onSuccess: () => { setSecret(''); setName(''); onSaved() } })
  return <form className="form-grid" onSubmit={e => { e.preventDefault(); save.mutate() }}><h3>{editing ? '修改凭证' : '新增凭证'}</h3><label>凭证名称<input value={name} onChange={e => setName(e.target.value)} maxLength={100} required /></label><label>{editing ? '替换密钥（留空保留）' : 'API 密钥'}<input type="password" autoComplete="new-password" value={secret} onChange={e => setSecret(e.target.value)} required={!editing} maxLength={8192} /></label><label className="checkbox"><input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />启用凭证</label><div className="actions"><button disabled={save.isPending}>{editing ? '保存凭证' : '创建凭证'}</button>{editing && <button className="secondary" type="button" onClick={onCancel}>取消修改</button>}</div>{save.error && <p role="alert">{save.error.message}</p>}</form>
}
export function Models({ space }: { space: Space }) {
  const client = useQueryClient(), base = `/spaces/${space.id}`
  const query = useQuery({ queryKey: ['private', space.id, 'models'], queryFn: ({ signal }) => list<Model>(`${base}/models${space.is_owner ? '' : '?available=true'}`, signal) })
  const credentials = useQuery({ queryKey: ['private', space.id, 'credentials'], queryFn: ({ signal }) => list<Credential>(`${base}/credentials`, signal), enabled: space.is_owner })
  const capabilities = useQuery({ queryKey: ['private', space.id, 'capabilities'], queryFn: ({ signal }) => request<Schema['ModelCapabilities']>(`${base}/model-capabilities`, 'GET', undefined, signal) })
  const [editing, setEditing] = useState<Model | null>(null)
  const disable = useMutation({ mutationFn: (m: Model) => request(`${base}/models/${m.id}`, 'DELETE', { revision: m.revision }), onSuccess: () => { setEditing(null); void client.invalidateQueries({ queryKey: ['private', space.id, 'models'] }) } })
  return <section className="panel"><h2>{space.is_owner ? '模型列表' : '可用模型'}</h2><p className="muted">Ark · 文本输入 · 工具调用 · 流式输出。填写已开通且支持这些能力的模型 ID；实际调用在运行阶段验证。</p>
    {query.data?.map(m => <div className="resource-row" key={m.id}><span>{m.name}<small>{m.model_id} · {m.enabled ? '已启用' : '已停用'}</small></span>{space.is_owner && <div className="actions"><button className="secondary" onClick={() => setEditing(m)}>编辑 {m.name}</button>{m.enabled && <button className="secondary" disabled={disable.isPending} onClick={() => disable.mutate(m)}>停用 {m.name}</button>}</div>}</div>)}
    {query.data?.length === 0 && <p className="muted">尚无可用模型配置。</p>}{[query.error, credentials.error, capabilities.error, disable.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{e?.message}</p>)}
    {space.is_owner && capabilities.data && <ModelForm key={editing ? `${editing.id}-${editing.revision}` : 'new'} base={base} editing={editing} credentials={credentials.data ?? []} capabilities={capabilities.data} onSaved={() => { setEditing(null); void client.invalidateQueries({ queryKey: ['private', space.id, 'models'] }) }} onCancel={() => setEditing(null)} />}</section>
}
function ModelForm({ base, editing, credentials, capabilities, onSaved, onCancel }: { base: string; editing: Model | null; credentials: Credential[]; capabilities: Schema['ModelCapabilities']; onSaved: () => void; onCancel: () => void }) {
  const [name, setName] = useState(editing?.name ?? ''), [modelID, setModelID] = useState(editing?.model_id ?? ''), [credentialID, setCredentialID] = useState(editing?.credential_id ?? '')
  const [context, setContext] = useState(editing?.context_window ?? 32768), [output, setOutput] = useState(editing?.max_output_tokens ?? 4096), [temperature, setTemperature] = useState(editing?.temperature ?? 0.7), [enabled, setEnabled] = useState(editing?.enabled ?? true)
  const save = useMutation({ mutationFn: () => request(`${base}/models${editing ? `/${editing.id}` : ''}`, editing ? 'PUT' : 'POST', { name, model_id: modelID, credential_id: credentialID, provider: capabilities.provider, endpoint: capabilities.endpoint, context_window: context, max_output_tokens: output, temperature, enabled, ...(editing ? { revision: editing.revision } : {}) } satisfies Schema['ModelInput']), onSuccess: () => { setName(''); setModelID(''); onSaved() } })
  return <form className="form-grid" onSubmit={e => { e.preventDefault(); save.mutate() }}><h3>{editing ? '修改模型' : '新增模型'}</h3><label>模型名称<input value={name} onChange={e => setName(e.target.value)} required maxLength={100} /></label><label>Ark 模型 ID<input value={modelID} onChange={e => setModelID(e.target.value)} required maxLength={128} placeholder="已开通的模型或推理接入点 ID" /></label><label>使用凭证<select value={credentialID} onChange={e => setCredentialID(e.target.value)} required><option value="">选择凭证</option>{credentials.map(c => <option value={c.id} key={c.id} disabled={!c.enabled}>{c.name}{!c.enabled && '（已禁用）'}</option>)}</select></label><label>上下文容量<input type="number" min={2} max={2000000} value={context} onChange={e => setContext(Number(e.target.value))} required /></label><label>最大输出 Token<input type="number" min={1} max={context - 1} value={output} onChange={e => setOutput(Number(e.target.value))} required /></label><label>温度<input type="number" min={0} max={2} step={0.1} value={temperature} onChange={e => setTemperature(Number(e.target.value))} required /></label><label className="checkbox"><input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />启用模型</label><p className="muted full-width">批准端点：{capabilities.endpoint}</p><div className="actions"><button disabled={save.isPending || !credentials.some(c => c.enabled)}>{editing ? '保存模型' : '创建模型'}</button>{editing && <button type="button" className="secondary" onClick={onCancel}>取消修改</button>}</div>{save.error && <p role="alert">{save.error.message}</p>}</form>
}
