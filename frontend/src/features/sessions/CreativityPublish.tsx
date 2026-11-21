import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { APIError, list, request, type Schema } from '../../lib/api'

type PublishManifest = Schema['CreativityPublishManifest']
const actions = { create: '新建并发布', update: '编辑并发新版', reference: '仅引用' }
const results = {
  published: '已发布',
  referenced: '仅引用',
  draft_saved: '草稿已保存，未发布',
  failed: '失败',
  unprocessed: '本次未处理',
  unknown: '结果待核实',
}

function entryLabel(entry: Schema['CreativityPublishEntry']) {
  const label = entry.status ? results[entry.status] : actions[entry.action]
  return entry.label ? `${label} · ${entry.label}` : label
}

export function CreativityPublish({ space, sessionID, busy, snapshot, onPublishing }: {
  space: string; sessionID: string; busy: boolean; snapshot: string; onPublishing: (pending: boolean) => void
}) {
  const [params, setParams] = useSearchParams()
  const selected = params.get('publish') ?? ''
  const openButton = useRef<HTMLButtonElement>(null)
  const select = (id: string) => {
    const next = new URLSearchParams(params)
    if (id) next.set('publish', id)
    else next.delete('publish')
    setParams(next, { replace: true })
    if (!id) requestAnimationFrame(() => openButton.current?.focus())
  }
  return <>
    <button ref={openButton} type="button" disabled={busy || !snapshot} onClick={() => select(snapshot)}
      title={!snapshot ? '最近一轮 Run 提交工作区快照后才能发布' : undefined}>去发布</button>
    {selected && <PublishDialog key={selected} space={space} sessionID={sessionID} selected={selected}
      available={!busy && selected === snapshot} close={() => select('')} onPublishing={onPublishing} />}
  </>
}

function PublishDialog({ space, sessionID, selected, available, close, onPublishing }: {
  space: string; sessionID: string; selected: string; available: boolean; close: () => void; onPublishing: (pending: boolean) => void
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const client = useQueryClient()
  const base = `/spaces/${space}/creativity/sessions/${sessionID}/publish`
  const [modelID, setModelID] = useState('')
  const preview = useQuery({
    queryKey: ['private', space, sessionID, 'publish', selected, modelID],
    queryFn: ({ signal }) => request<PublishManifest>(`${base}?${new URLSearchParams({
      workspace_snapshot_id: selected, ...(modelID ? { model_id: modelID } : {}),
    })}`, 'GET', undefined, signal),
    retry: false,
  })
  const confirm = useMutation({
    mutationFn: (choice: string) => request<PublishManifest>(base, 'POST', {
      workspace_snapshot_id: selected, ...(choice ? { model_id: choice } : {}),
    }),
    onMutate: () => onPublishing(true),
    onSettled: (data, error) => {
      onPublishing(false)
      if (!data && !publishResult(error)) return
      void client.invalidateQueries({ queryKey: ['private', space, 'agent', 'list'] })
      void client.invalidateQueries({ queryKey: ['private', space, 'skill', 'list'] })
    },
  })
  useEffect(() => { dialog.current?.showModal() }, [])
  const partial = publishResult(confirm.error)
  const manifest = confirm.data ?? partial ?? preview.data
  const modelRequired = hasErrorCode(preview.error, 'publish_model_required')
  const modelConflict = [preview.error, confirm.error].some(error => hasErrorCode(error, 'publish_model_locked'))
  const locked = !!manifest?.model_locked
  const chooseModel = (modelRequired || !!modelID) && !locked && !modelConflict
  const models = useQuery({
    queryKey: ['private', space, 'catalog-models'],
    queryFn: ({ signal }) => list<Schema['Model']>(`/spaces/${space}/models?available=true`, signal),
    enabled: chooseModel || locked,
  })
  const useLockedModel = () => {
    confirm.reset()
    setModelID('')
    void client.invalidateQueries({ queryKey: ['private', space, sessionID, 'publish', selected] })
  }
  return <dialog ref={dialog} className="run-details-dialog creativity-publish-dialog" aria-labelledby="publish-heading"
    onCancel={event => { event.preventDefault(); if (!confirm.isPending) close() }}>
    <h2 id="publish-heading">发布 Agent 和 Skills</h2>
    <p className="muted">发布内容来自此轮 Run 的工作区快照。要改内容，请回到会话修改后再打开发布。</p>
    {chooseModel && <div>
      <p>{modelID ? '所选模型仅用于本次发布，不会修改工作区快照。' : '此 Agent 尚未绑定模型，请选择当前空间的模型，再检查发布清单。'}</p>
      <label>发布模型
        <select value={modelID} disabled={confirm.isPending || !available || !models.data?.length} onChange={event => {
          confirm.reset()
          setModelID(event.target.value)
        }}>
          <option value="">选择可用模型</option>
          {models.data?.map(model => <option key={model.id} value={model.id}>{model.name}</option>)}
        </select>
      </label>
      {models.isPending && <p role="status">正在读取可用模型…</p>}
      {models.isError && <p role="alert">{models.error.message} <button type="button" onClick={() => { void models.refetch() }}>重试读取模型</button></p>}
      {models.data?.length === 0 && <p>当前空间没有可用模型，请先在「模型配置」中添加或启用模型。</p>}
    </div>}
    {locked && <p>本次发布已固定模型：{models.data?.find(model => model.id === manifest.model_id)?.name ?? manifest.model_id}</p>}
    {preview.isPending && <p role="status">正在读取发布清单…</p>}
    {preview.isError && !modelRequired && <p role="alert">{preview.error.message}</p>}
    {manifest && <ul>
      <li>Agent · {manifest.agent.name} — {entryLabel(manifest.agent)}</li>
      {manifest.skills.map((skill, index) => <li key={`${skill.id ?? skill.name}-${index}`}>Skill · {skill.name} — {entryLabel(skill)}</li>)}
    </ul>}
    {confirm.isError && <p role="alert">{confirm.error.message} {partial
      ? '已成功发布的版本和已保存的草稿不会回滚。'
      : '未收到逐项结果，重试时会重新核对。'}</p>}
    {confirm.isSuccess && <p role="status">发布完成，可在管理页查看。</p>}
    {!available && !confirm.isSuccess && <p className="muted">当前有运行中的任务，或此快照已不是最近一轮。请在会话结束后重新打开。</p>}
    <div className="dialog-actions">
      <button type="button" className="secondary" disabled={confirm.isPending} onClick={close}>关闭</button>
      {modelConflict && available && <button type="button" onClick={useLockedModel}>使用已确认的模型</button>}
      {preview.isError && !modelRequired && !modelConflict && available && <button type="button" onClick={() => { void preview.refetch() }}>重试加载</button>}
      {!confirm.isSuccess && <button type="button" disabled={!preview.isSuccess || !available || confirm.isPending || modelConflict}
        onClick={() => confirm.mutate(locked ? '' : modelID)}>{confirm.isPending ? '正在发布…' : confirm.isError ? '重试发布' : '确认发布'}</button>}
    </div>
  </dialog>
}

function hasErrorCode(error: Error | null, code: string) {
  return error instanceof APIError && error.code === code
}

function publishResult(error: Error | null): PublishManifest | undefined {
  if (error instanceof APIError) return error.details?.result as PublishManifest | undefined
}
