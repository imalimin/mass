// This validates evidence references, not test outcomes or cloud capabilities.
// Run after functional checks in verify-d09-local; never turn pending into pass.
import { readFile, mkdir, writeFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
const engineering = await readFile('docs/engineering.md', 'utf8')
const required = [...engineering.matchAll(/^\| (A\d\d)/gm)].map(m => m[1])
const evidence = JSON.parse(await readFile('scripts/acceptance-evidence.json', 'utf8'))
if (required.length !== 51 || new Set(required).size !== 51 || evidence.length !== 51) throw Error('Acceptance matrix must have exactly A01–A51')
const seen = new Set(), statuses = new Set(['local_evidence', 'pending_real', 'pending_delivery', 'historical_real', 'not_enabled'])
for (const row of evidence) {
  if (!required.includes(row.id) || seen.has(row.id) || !statuses.has(row.status) || !row.sources.length || !row.note) throw Error(`Invalid evidence row ${row.id}`)
  seen.add(row.id)
  for (const source of row.sources) {
    if (source.path.startsWith('/') || source.path.includes('..')) throw Error('Evidence must remain in the repository')
    const raw = await readFile(source.path, 'utf8')
    if (!source.anchor || !raw.includes(source.anchor)) throw Error(`Missing evidence anchor ${row.id}: ${source.path}#${source.anchor}`)
    source.sha256 = createHash('sha256').update(raw).digest('hex')
  }
}
const report = { revision: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), generated_at: new Date().toISOString(), meaning: 'Evidence references only. Local functional verification is a separate prerequisite. Real and delivery gates remain pending.', evidence }
await mkdir('.cache', { recursive: true }); await writeFile('.cache/d09-acceptance.json', JSON.stringify(report, null, 2) + '\n')
console.log(`Acceptance evidence: ${evidence.length} rows linked; pending: ${evidence.filter(r => r.status.startsWith('pending')).map(r => `${r.id}=${r.status}`).join(', ')}; A25=historical real TOS, A33=not enabled.`)
