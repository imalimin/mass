import { execFileSync } from 'node:child_process'
import { join } from 'node:path'

const lines = execFileSync('sh', ['scripts/go.sh', 'list', '-f', '{{.ImportPath}} {{join .Imports " "}}', './internal/...'], { encoding: 'utf8' }).trim().split('\n')
const prefix = 'github.com/imalimin/mass/internal/'
for (const line of lines) {
  const [name, ...imports] = line.split(' ')
  const local = name.slice(prefix.length)
  const core = /^(domain|runtime|journal|resource)(\/|$)/.test(local)
  if (core && imports.some(value => /internal\/(adapter|transport|bootstrap)(\/|$)/.test(value))) throw new Error(`Core imports an outer adapter: ${name}`)
  if (local.startsWith('domain') && imports.some(value => value.startsWith(prefix) && !value.startsWith(`${prefix}domain`))) throw new Error(`Domain imports application packages: ${name}`)
  if (local === 'runtime') throw new Error('runtime is an organization directory, not a monolithic package')
}

const root = execFileSync('sh', ['scripts/go.sh', 'env', 'GOROOT'], { encoding: 'utf8' }).trim()
const sources = execFileSync('sh', ['scripts/go.sh', 'list', '-f', '{{.Dir}}|{{join .GoFiles ","}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}', './cmd/...', './internal/...', './db/...', './tests/...', './scripts/...'], { encoding: 'utf8' }).trim().split('\n').flatMap(line => {
  const [directory, ...groups] = line.split('|')
  return groups.flatMap(group => group.split(',').filter(Boolean).map(file => join(directory, file)))
})
const unformatted = execFileSync(join(root, 'bin/gofmt'), ['-l', ...sources], { encoding: 'utf8' }).trim()
if (unformatted) throw new Error(`Run make fmt:\n${unformatted}`)
