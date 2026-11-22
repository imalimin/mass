import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { spacePath } from '../../app/routes'
import { FormDialog } from '../../components/FormDialog'
import { Icon } from '../../components/Icon'
import { WorkspaceListTable, WorkspaceListToolbar } from '../../components/WorkspaceList'
import { list, request, type Schema } from '../../lib/api'
import {
  defaultCreateExpiry,
  defaultRenewExpiry,
  formatLocalDateTime,
  openAPITokenStatusLabel,
  parseDateTimeLocalToISO,
  sortOpenAPITokens,
  toDateTimeLocalValue,
  type OpenAPIToken,
} from './openapiTokens'

type Space = Schema['Space']

const columns = ['token名', 'Token', '创建时间', '到期时间', '续期', '吊销']

async function writeClipboard(text: string): Promise<boolean> {
  if (!navigator.clipboard?.writeText) return false
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

export function OpenAPI({ space }: { space: Space }) {
  if (!space.is_owner) {
    return (
      <section aria-label="OpenAPI">
        <WorkspaceListToolbar space={space} name="OpenAPI">{null}</WorkspaceListToolbar>
        <section className="panel">
          <h2>无权访问 OpenAPI 管理</h2>
          <p className="muted">仅当前空间所有者可以查看、签发、续期或吊销 OpenAPI Bearer Token。</p>
          <Link className="text-link" to={spacePath(space.id)}>返回空间首页</Link>
        </section>
      </section>
    )
  }
  return <OwnerOpenAPI space={space} />
}

function OwnerOpenAPI({ space }: { space: Space }) {
  const client = useQueryClient()
  const base = `/spaces/${space.id}/openapi/tokens`
  const queryKey = ['private', space.id, 'openapi-tokens'] as const

  const [creating, setCreating] = useState(false)
  const [renewing, setRenewing] = useState<OpenAPIToken | null>(null)
  const [revoking, setRevoking] = useState<OpenAPIToken | null>(null)
  const [revealed, setRevealed] = useState<{ id: string; name: string; token: string; copied: boolean } | null>(null)
  const [copiedId, setCopiedId] = useState<string | null>(null)
  const [notice, setNotice] = useState('')
  const [copyError, setCopyError] = useState('')
  const copyTimer = useRef<number | null>(null)

  useEffect(() => () => {
    if (copyTimer.current !== null) window.clearTimeout(copyTimer.current)
  }, [])

  const tokens = useQuery({
    queryKey,
    queryFn: ({ signal }) => list<OpenAPIToken>(base, signal),
    select: sortOpenAPITokens,
  })

  const refresh = () => {
    void client.invalidateQueries({ queryKey })
  }

  const markCopied = (id: string) => {
    setCopiedId(id)
    if (copyTimer.current !== null) window.clearTimeout(copyTimer.current)
    copyTimer.current = window.setTimeout(() => {
      setCopiedId(current => current === id ? null : current)
    }, 2000)
  }

  const copyToken = useMutation({
    mutationFn: async (token: OpenAPIToken) => {
      const secret = await request<Schema['OpenAPITokenSecret']>(`${base}/${token.id}`, 'GET')
      const copied = await writeClipboard(secret.token)
      return { id: token.id, name: token.name, token: secret.token, copied }
    },
    onMutate: () => {
      setCopyError('')
      setNotice('')
    },
    onSuccess: result => {
      if (result.copied) {
        markCopied(result.id)
      } else {
        setRevealed({ id: result.id, name: result.name, token: result.token, copied: false })
        setNotice(`浏览器未授权自动写入剪贴板，请手动复制「${result.name}」的 Token。`)
      }
    },
    onError: error => {
      setCopyError(error.message)
      refresh()
    },
  })

  const handleCopyRevealed = async () => {
    if (!revealed) return
    const copied = await writeClipboard(revealed.token)
    setRevealed({ ...revealed, copied })
    if (copied) {
      markCopied(revealed.id)
      setNotice('')
    } else {
      setNotice('自动复制失败，请手动选中 Token 复制。')
    }
  }

  return (
    <section aria-label="OpenAPI">
      <WorkspaceListToolbar space={space} name="OpenAPI">
        <Link className="secondary-link" to={spacePath(space.id, 'openapi/docs')}>
          <Icon name="file" size={16} />文档
        </Link>
        <button type="button" onClick={() => { setNotice(''); setCopyError(''); setCreating(true) }}>
          <Icon name="plus" />创建 Token
        </button>
      </WorkspaceListToolbar>

      {notice && <p className="openapi-notice" role="status">{notice}</p>}
      {copyError && <p role="alert">{copyError}</p>}
      {tokens.error && <p role="alert">{tokens.error.message}</p>}

      {revealed && (
        <div className="openapi-token-reveal" role="region" aria-label="Token 原文">
          <div className="openapi-token-reveal-head">
            <strong>{revealed.name}</strong>
            <div className="actions">
              <button type="button" className="secondary" onClick={() => { void handleCopyRevealed() }}>
                <Icon name={revealed.copied ? 'check' : 'copy'} size={15} />{revealed.copied ? '已复制' : '复制 Token'}
              </button>
              <button type="button" className="secondary" onClick={() => setRevealed(null)}>关闭</button>
            </div>
          </div>
          <code className="openapi-token-code">{revealed.token}</code>
        </div>
      )}

      {tokens.isPending && <p role="status">正在读取 Token 列表…</p>}
      {tokens.data?.length === 0 && <p className="muted">尚未签发 OpenAPI Token，点击右上角创建第一个 Token。</p>}

      {!!tokens.data?.length && (
        <WorkspaceListTable
          label="OpenAPI Token 列表"
          columns={columns}
          className="openapi-table"
          columnGroup={
            <colgroup>
              <col />
              <col className="openapi-token-column" />
              <col className="openapi-date-column" span={2} />
              <col className="agent-action-column" span={2} />
            </colgroup>
          }
        >
          {tokens.data.map(token => {
            const copying = copyToken.isPending && copyToken.variables?.id === token.id
            const copied = copiedId === token.id
            return (
              <tr key={token.id}>
                <td>
                  <div className="openapi-name-cell">
                    <span className="workspace-list-name">{token.name}</span>
                    <span className={`openapi-status openapi-status-${token.status}`}>{openAPITokenStatusLabel(token.status)}</span>
                  </div>
                </td>
                <td>
                  <div className="openapi-token-cell">
                    <code className="openapi-token-mask">••••••••••••</code>
                    {token.status === 'active' && (
                      <button
                        type="button"
                        className="icon-button openapi-copy-icon"
                        disabled={copyToken.isPending}
                        aria-label={`复制 ${token.name} 的 Token`}
                        title={copied ? '已复制' : '复制 Token'}
                        aria-busy={copying}
                        onClick={() => copyToken.mutate(token)}
                      >
                        <Icon name={copied ? 'check' : 'copy'} size={15} />
                      </button>
                    )}
                  </div>
                </td>
                <td>
                  <time className="definition-date" dateTime={token.created_at}>{formatLocalDateTime(token.created_at)}</time>
                </td>
                <td>
                  <time className="definition-date" dateTime={token.expires_at}>{formatLocalDateTime(token.expires_at)}</time>
                </td>
                <td>
                  {token.status !== 'revoked' ? (
                    <button
                      type="button"
                      className="icon-button agent-action"
                      aria-label={`续期 ${token.name}`}
                      title="续期"
                      onClick={() => { setNotice(''); setCopyError(''); setRenewing(token) }}
                    >
                      <Icon name="refresh" size={18} />
                    </button>
                  ) : (
                    <span className="muted">—</span>
                  )}
                </td>
                <td>
                  {token.status !== 'revoked' ? (
                    <button
                      type="button"
                      className="icon-button agent-action openapi-revoke-action"
                      aria-label={`吊销 ${token.name}`}
                      title="吊销"
                      onClick={() => { setNotice(''); setCopyError(''); setRevoking(token) }}
                    >
                      <Icon name="ban" size={18} />
                    </button>
                  ) : (
                    <span className="muted">—</span>
                  )}
                </td>
              </tr>
            )
          })}
        </WorkspaceListTable>
      )}

      {creating && (
        <CreateTokenDialog
          base={base}
          onClose={() => setCreating(false)}
          onCreated={created => {
            setCreating(false)
            setRevealed({ id: created.id, name: created.name, token: created.token, copied: false })
            setNotice(`已创建「${created.name}」，可立即复制 Token。`)
            refresh()
          }}
        />
      )}

      {renewing && (
        <RenewTokenDialog
          base={base}
          token={renewing}
          onClose={() => setRenewing(null)}
          onRenewed={updated => {
            setRenewing(null)
            setNotice(`已将「${updated.name}」续期至 ${formatLocalDateTime(updated.expires_at)}。`)
            refresh()
          }}
        />
      )}

      {revoking && (
        <RevokeTokenDialog
          base={base}
          token={revoking}
          onClose={() => setRevoking(null)}
          onRevoked={target => {
            setRevoking(null)
            setRevealed(current => current?.id === target.id ? null : current)
            setNotice(`已吊销「${target.name}」。`)
            refresh()
          }}
        />
      )}
    </section>
  )
}

function CreateTokenDialog({ base, onClose, onCreated }: {
  base: string
  onClose: () => void
  onCreated: (created: Schema['OpenAPITokenCreated']) => void
}) {
  const [name, setName] = useState('')
  const [expiresLocal, setExpiresLocal] = useState(() => toDateTimeLocalValue(defaultCreateExpiry()))
  const [validationError, setValidationError] = useState('')

  const create = useMutation({
    mutationFn: (payload: Schema['OpenAPITokenCreateInput']) =>
      request<Schema['OpenAPITokenCreated']>(base, 'POST', payload),
    onSuccess: onCreated,
  })

  const submit = () => {
    const trimmed = name.trim()
    if (!trimmed) {
      setValidationError('请填写 Token 名称。')
      return
    }
    const iso = parseDateTimeLocalToISO(expiresLocal)
    if (!iso || Date.parse(iso) <= Date.now()) {
      setValidationError('到期时间必须晚于当前时间。')
      return
    }
    setValidationError('')
    create.mutate({ name: trimmed, expires_at: iso })
  }

  return (
    <FormDialog title="创建 OpenAPI Token" pending={create.isPending} close={onClose}>
      {dismiss => (
        <form className="openapi-form" onSubmit={event => { event.preventDefault(); submit() }}>
          <label>
            名称
            <input
              value={name}
              onChange={event => setName(event.target.value)}
              maxLength={100}
              required
              placeholder="例如：客服系统集成"
              disabled={create.isPending}
            />
          </label>
          <label>
            到期时间（默认 180 天后）
            <input
              type="datetime-local"
              value={expiresLocal}
              onChange={event => setExpiresLocal(event.target.value)}
              required
              disabled={create.isPending}
            />
          </label>
          {validationError && <p role="alert">{validationError}</p>}
          {create.error && <p role="alert">{create.error.message}</p>}
          <div className="dialog-actions">
            <button type="button" className="secondary" disabled={create.isPending} onClick={dismiss}>取消</button>
            <button disabled={create.isPending || !name.trim()}>{create.isPending ? '正在创建…' : '创建'}</button>
          </div>
        </form>
      )}
    </FormDialog>
  )
}

function RenewTokenDialog({ base, token, onClose, onRenewed }: {
  base: string
  token: OpenAPIToken
  onClose: () => void
  onRenewed: (updated: OpenAPIToken) => void
}) {
  const [expiresLocal, setExpiresLocal] = useState(() => toDateTimeLocalValue(defaultRenewExpiry(token)))
  const [validationError, setValidationError] = useState('')

  const renew = useMutation({
    mutationFn: (payload: Schema['OpenAPITokenRenewInput']) =>
      request<OpenAPIToken>(`${base}/${token.id}/renew`, 'POST', payload),
    onSuccess: onRenewed,
  })

  const submit = () => {
    const iso = parseDateTimeLocalToISO(expiresLocal)
    if (!iso || Date.parse(iso) <= Date.now()) {
      setValidationError('新的到期时间必须晚于当前时间。')
      return
    }
    setValidationError('')
    renew.mutate({ expires_at: iso })
  }

  return (
    <FormDialog title={`续期「${token.name}」`} pending={renew.isPending} close={onClose}>
      {dismiss => (
        <form className="openapi-form" onSubmit={event => { event.preventDefault(); submit() }}>
          <p className="muted">
            当前到期时间：{formatLocalDateTime(token.expires_at)}。
            {token.status === 'active'
              ? '默认在原到期时间上延长 180 天，续期沿用原 Token。'
              : '默认从当前时间起延长 180 天，续期后原 Token 恢复有效。'}
          </p>
          <label>
            新的到期时间
            <input
              type="datetime-local"
              value={expiresLocal}
              onChange={event => setExpiresLocal(event.target.value)}
              required
              disabled={renew.isPending}
            />
          </label>
          {validationError && <p role="alert">{validationError}</p>}
          {renew.error && <p role="alert">{renew.error.message}</p>}
          <div className="dialog-actions">
            <button type="button" className="secondary" disabled={renew.isPending} onClick={dismiss}>取消</button>
            <button disabled={renew.isPending}>{renew.isPending ? '正在续期…' : '确认续期'}</button>
          </div>
        </form>
      )}
    </FormDialog>
  )
}

function RevokeTokenDialog({ base, token, onClose, onRevoked }: {
  base: string
  token: OpenAPIToken
  onClose: () => void
  onRevoked: (target: OpenAPIToken) => void
}) {
  const revoke = useMutation({
    mutationFn: () => request<void>(`${base}/${token.id}`, 'DELETE'),
    onSuccess: () => onRevoked(token),
  })

  return (
    <FormDialog title={`吊销「${token.name}」`} pending={revoke.isPending} close={onClose}>
      {dismiss => (
        <div className="openapi-form">
          <p className="muted">吊销后将立即阻止使用该 Token 的新请求并关闭已有 SSE 连接。已吊销记录会保留在列表中，不可恢复或续期。</p>
          {revoke.error && <p role="alert">{revoke.error.message}</p>}
          <div className="dialog-actions">
            <button type="button" className="secondary" disabled={revoke.isPending} onClick={dismiss}>取消</button>
            <button type="button" disabled={revoke.isPending} onClick={() => revoke.mutate()}>
              {revoke.isPending ? '正在吊销…' : '确认吊销'}
            </button>
          </div>
        </div>
      )}
    </FormDialog>
  )
}
