import { Link } from 'react-router-dom'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import openapiGuide from '../../../../docs/openapi-guide.md?raw'
import { spacePath } from '../../app/routes'
import { Icon } from '../../components/Icon'
import type { Schema } from '../../lib/api'

type Space = Schema['Space']

export function OpenAPIDocs({ space }: { space: Space }) {
  const backPath = spacePath(space.id, 'openapi')
  return (
    <section aria-label="OpenAPI 使用文档">
      <header className="workspace-list-toolbar">
        <nav className="breadcrumbs" aria-label="面包屑">
          <Link to={spacePath(space.id)} title={space.name}>{space.name}</Link>
          <span aria-hidden="true">/</span>
          <Link to={backPath}>OpenAPI</Link>
          <span aria-hidden="true">/</span>
          <span aria-current="page">使用文档</span>
        </nav>
        <div className="workspace-list-actions">
          <Link className="secondary-link" to={backPath}>
            <Icon name="arrowLeft" size={16} />返回 OpenAPI
          </Link>
        </div>
      </header>

      <article className="openapi-docs-article">
        <Markdown
          remarkPlugins={[remarkGfm]}
          skipHtml
          components={{
            img: () => null,
            a: ({ href, children }) =>
              href && /^https?:\/\//i.test(href)
                ? <a href={href} target="_blank" rel="noreferrer">{children}</a>
                : <span>{children}</span>,
          }}
        >
          {openapiGuide}
        </Markdown>
      </article>
    </section>
  )
}
