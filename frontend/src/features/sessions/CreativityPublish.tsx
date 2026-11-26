import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient, type MutationKey } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { agentSessionsPath } from '../../app/routes'
import { Icon } from '../../components/Icon'
import { APIError, list, newRequestKey, request, type Schema } from '../../lib/api'

type PublishManifest = Schema['CreativityPublishManifest']
const actions = { create: '新建并发布', update: '编辑并发新版', reference: '仅引用' }
const debugActions = { create: '保存新草稿', update: '保存修改到草稿', reference: '引用已发布版本' }
const results = {
  published: '已发布',
  referenced: '仅引用',
  draft_saved: '草稿已保存，未发布',
  failed: '失败',
  unprocessed: '本次未处理',
  unknown: '结果待核实',
}

function ManifestEntries({ manifest, debug = false }: { manifest: PublishManifest; debug?: boolean }) {
  function entryLabel(entry: Schema['CreativityPublishEntry']) {
    const label = entry.status ? results[entry.status] : (debug ? debugActions : actions)[entry.action]
    return entry.label ? `${label} · ${entry.label}` : label
  }
  return <ul className="manifest-entries" aria-label="发布清单">
    {[manifest.agent, ...manifest.skills].map((entry, index) => <li key={`${entry.id ?? entry.name}-${index}`}>
      <span className="manifest-name"><Icon name={index ? 'layers' : 'bot'} />{index ? 'Skill' : 'Agent'} · {entry.name}</span>
      <span className="manifest-status" data-status={entry.status}>{entryLabel(entry)}</span>
    </li>)}
  </ul>
}

export function CreativityPublish({ space, sessionID, busy, snapshot, mutationKey }: {
  space: string; sessionID: string; busy: boolean; snapshot: string; mutationKey: MutationKey
}) {
  const [params, setParams] = useSearchParams()
  const selected = params.get('publish') ?? ''
  const publishButton = useRef<HTMLButtonElement>(null)
  const debugButton = useRef<HTMLButtonElement>(null)
  const debugKey = useRef<{ snapshot: string; key: string } | null>(null)
  const [chooseDebugModel, setChooseDebugModel] = useState(false)
  const navigate = useNavigate()
  const client = useQueryClient()
  const debug = useMutation({
    mutationKey,
    mutationFn: ({ snapshotID, modelID, key }: { snapshotID: string; modelID: string; key: string }) =>
      request<Schema['RuntimeSession']>(`/spaces/${space}/creativity/sessions/${sessionID}/draft-debug`, 'POST', {
        workspace_snapshot_id: snapshotID, idempotency_key: key, ...(modelID ? { model_id: modelID } : {}),
      }),
    onSettled: (session, error) => {
      if (!session && !publishResult(error)) return
      void client.invalidateQueries({ queryKey: ['private', space, 'agent', 'list'] })
      void client.invalidateQueries({ queryKey: ['private', space, 'skill', 'list'] })
      void client.invalidateQueries({ queryKey: ['private', space, sessionID, 'publish'] })
    },
  })
  const tryDraft = (modelID = '') => {
    if (busy || !snapshot || debug.isPending) return
    if (debugKey.current?.snapshot !== snapshot) debugKey.current = { snapshot, key: newRequestKey() }
    debug.mutate({ snapshotID: snapshot, modelID, key: debugKey.current.key }, {
      onError: error => {
        if (hasErrorCode(error, 'publish_model_required')) setChooseDebugModel(true)
      },
      onSuccess: session => {
        client.setQueryData(['private', space, session.id, 'session'], session)
        void client.invalidateQueries({ queryKey: ['private', space, 'sessions', session.definition_id] })
        navigate(agentSessionsPath(space, session.definition_id, session.id), { replace: true, state: { creativitySessionID: sessionID } })
      },
    })
  }
  const closeDebug = () => {
    setChooseDebugModel(false)
    debug.reset()
    requestAnimationFrame(() => debugButton.current?.focus())
  }
  const select = (id: string) => {
    const next = new URLSearchParams(params)
    next.delete('publish')
    if (id) next.set('publish', id)
    setParams(next, { replace: true })
    if (!id) requestAnimationFrame(() => publishButton.current?.focus())
  }
  return <>
    <button ref={debugButton} type="button" className="secondary" disabled={busy || !snapshot || debug.isPending} onClick={() => tryDraft()}
      title={busy ? '请等待当前运行结束' : !snapshot ? '最近一轮 Run 提交工作区快照后才能调试' : undefined}>
      <Icon name={debug.isPending ? 'loader' : 'play'} className={debug.isPending ? 'icon-spin' : undefined} />{debug.isPending ? '正在打开…' : '去试试'}
    </button>
    <button ref={publishButton} type="button" disabled={busy || !snapshot || debug.isPending} onClick={() => select(snapshot)}
      title={busy ? '请等待当前运行结束' : !snapshot ? '最近一轮 Run 提交工作区快照后才能发布' : undefined}>
      <Icon name="rocket" />去发布
    </button>
    {selected && <PublishDialog key={selected} space={space} sessionID={sessionID} selected={selected}
      available={!busy && !debug.isPending && selected === snapshot} close={() => select('')} mutationKey={mutationKey} />}
    {!debug.isIdle && <DraftDebugDialog space={space} chooseModel={chooseDebugModel}
      available={!busy && debug.variables?.snapshotID === snapshot} pending={debug.isPending || debug.isSuccess}
      error={debug.error} modelID={debug.variables?.modelID ?? ''} start={tryDraft} close={closeDebug} />}
  </>
}

function DraftDebugDialog({ space, chooseModel, available, pending, error, modelID, start, close }: {
  space: string; chooseModel: boolean; available: boolean; pending: boolean; error: Error | null;
  modelID: string; start: (modelID: string) => void; close: () => void
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const modelRequired = hasErrorCode(error, 'publish_model_required')
  const modelLocked = hasErrorCode(error, 'publish_model_locked')
  const nameConflict = hasErrorCode(error, 'definition_name_conflict')
  const models = useAvailableModels(space, chooseModel && !modelLocked)
  const partial = publishResult(error)
  useEffect(() => { dialog.current?.showModal() }, [])
  return <dialog ref={dialog} className="run-details-dialog creativity-publish-dialog" aria-labelledby="debug-heading"
    onCancel={event => { event.preventDefault(); if (!pending) close() }}>
    <header><h2 id="debug-heading" className="icon-link"><Icon name="play" size={23} />草稿调试</h2></header>
    {chooseModel && !modelLocked && <div>
      <p>此 Agent 尚未绑定模型。选择模型后将直接打开调试会话。</p>
      <label>调试模型
        <select value={modelID} disabled={pending || !available || !models.data?.length} onChange={event => {
          if (event.target.value) start(event.target.value)
        }}>
          <option value="">选择可用模型</option>
          {models.data?.map(model => <option key={model.id} value={model.id}>{model.name}</option>)}
        </select>
      </label>
      {models.isPending && <p role="status">正在读取可用模型…</p>}
      {models.isError && <p role="alert">{models.error.message} <button type="button" onClick={() => { void models.refetch() }}><Icon name="refresh" />重试读取模型</button></p>}
      {models.data?.length === 0 && <p>当前空间没有可用模型，请先在「模型配置」中添加或启用模型。</p>}
    </div>}
    {pending && <p className="dialog-status" role="status"><span className="working-dot" aria-hidden="true" />正在校验并准备调试会话…</p>}
    {error && !modelRequired && <p role="alert">{error.message}</p>}
    {partial && <ManifestEntries manifest={partial} debug />}
    {!available && <p className="muted">当前有运行中的任务，或此快照已不是最近一轮。请在会话结束后重新打开。</p>}
    <div className="dialog-actions">
      <button type="button" className="secondary" disabled={pending} onClick={close}><Icon name="close" />关闭</button>
      {error && !modelRequired && !nameConflict && <button type="button" disabled={!available || pending}
        onClick={() => start(modelLocked ? '' : modelID)}><Icon name={modelLocked ? 'check' : 'refresh'} />{modelLocked ? '使用已确认的模型' : '重试'}</button>}
    </div>
  </dialog>
}

function useAvailableModels(space: string, enabled: boolean) {
  return useQuery({
    queryKey: ['private', space, 'catalog-models'],
    queryFn: ({ signal }) => list<Schema['Model']>(`/spaces/${space}/models?available=true`, signal),
    enabled,
  })
}

function PublishDialog({ space, sessionID, selected, available, close, mutationKey }: {
  space: string; sessionID: string; selected: string; available: boolean; close: () => void; mutationKey: MutationKey
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const client = useQueryClient()
  const url = `/spaces/${space}/creativity/sessions/${sessionID}/publish`
  const [modelID, setModelID] = useState('')
  const preview = useQuery({
    queryKey: ['private', space, sessionID, 'publish', selected, modelID],
    queryFn: ({ signal }) => request<PublishManifest>(`${url}?${new URLSearchParams({
      workspace_snapshot_id: selected, ...(modelID ? { model_id: modelID } : {}),
    })}`, 'GET', undefined, signal),
    retry: false,
  })
  const confirm = useMutation({
    mutationKey,
    mutationFn: (choice: string) => request<PublishManifest>(url, 'POST', {
      workspace_snapshot_id: selected, ...(choice ? { model_id: choice } : {}),
    }),
    onSettled: (data, error) => {
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
  const nameConflict = hasErrorCode(confirm.error, 'definition_name_conflict')
  const locked = !!manifest?.model_locked
  const chooseModel = (modelRequired || !!modelID) && !locked && !modelConflict
  const models = useAvailableModels(space, chooseModel || locked)
  const useLockedModel = () => {
    confirm.reset()
    setModelID('')
    void client.invalidateQueries({ queryKey: ['private', space, sessionID, 'publish', selected] })
  }
  return <dialog ref={dialog} className="run-details-dialog creativity-publish-dialog" aria-labelledby="publish-heading"
    onCancel={event => { event.preventDefault(); if (!confirm.isPending) close() }}>
    <header><h2 id="publish-heading" className="icon-link"><Icon name="rocket" size={23} />发布 Agent 和 Skills</h2></header>
    {!confirm.isSuccess && !confirm.isError && <p className="muted">发布内容来自此轮 Run 的工作区快照。要改内容，请回到会话修改后再打开发布。</p>}
    {confirm.isError && <div className="publish-feedback" role="alert">
      <strong className="dialog-status"><Icon name="alert" />发布未完成</strong>
      <p>{confirm.error.message} {partial ? '已成功发布的版本和已保存的草稿均已保留。' : '未收到逐项结果，重试时会重新核对。'}</p>
    </div>}
    {confirm.isSuccess && <p className="dialog-status publish-success" role="status"><Icon name="check" size={22} />发布完成，可在管理页查看。</p>}
    {chooseModel && <div>
      <p>{modelID ? '所选模型将写入 Agent 草稿，用于发布。' : '此 Agent 尚未绑定模型，请选择当前空间的模型，再检查发布清单。'}</p>
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
      {models.isError && <p role="alert">{models.error.message} <button type="button" onClick={() => { void models.refetch() }}><Icon name="refresh" />重试读取模型</button></p>}
      {models.data?.length === 0 && <p>当前空间没有可用模型，请先在「模型配置」中添加或启用模型。</p>}
    </div>}
    {locked && <p className="muted">本次发布已固定模型：{models.data?.find(model => model.id === manifest.model_id)?.name ?? manifest.model_id}</p>}
    {preview.isPending && <p role="status">正在读取发布清单…</p>}
    {preview.isError && !modelRequired && <p role="alert">{preview.error.message}</p>}
    {manifest && <><h3>发布清单</h3><ManifestEntries manifest={manifest} /></>}
    {!available && !confirm.isSuccess && <p className="muted">当前有运行中的任务，或此快照已不是最近一轮。请在会话结束后重新打开。</p>}
    <div className="dialog-actions">
      <button type="button" className="secondary" disabled={confirm.isPending} onClick={close}><Icon name="close" />关闭</button>
      {modelConflict && available && <button type="button" onClick={useLockedModel}><Icon name="check" />使用已确认的模型</button>}
      {preview.isError && !modelRequired && !modelConflict && available && <button type="button" onClick={() => { void preview.refetch() }}><Icon name="refresh" />重试加载</button>}
      {!confirm.isSuccess && !nameConflict && <button type="button" disabled={!preview.isSuccess || !available || confirm.isPending || modelConflict}
        onClick={() => confirm.mutate(locked ? '' : modelID)}><Icon name={confirm.isPending ? 'loader' : confirm.isError ? 'refresh' : 'check'} className={confirm.isPending ? 'icon-spin' : undefined} />
        {confirm.isPending ? '正在发布…' : confirm.isError ? '重试发布' : '确认发布'}
      </button>}
    </div>
  </dialog>
}

function hasErrorCode(error: Error | null, code: string) {
  return error instanceof APIError && error.code === code
}

function publishResult(error: Error | null): PublishManifest | undefined {
  if (error instanceof APIError) return error.details?.result as PublishManifest | undefined
}
