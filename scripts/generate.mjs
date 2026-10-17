import { readFileSync, existsSync } from 'node:fs'
import { spawnSync } from 'node:child_process'

const files = ['frontend/src/lib/api.generated.ts', 'api/events/fixtures/go-events.json', 'api/events/fixtures/go-events.sse']
const check = process.argv.includes('--check')
const before = files.map(file => existsSync(file) ? readFileSync(file) : null)
for (const [command, args] of [['npm', ['--prefix', 'frontend', 'run', 'generate']], ['sh', ['scripts/go.sh', 'run', './scripts/agui-fixtures']]]) {
  const result = spawnSync(command, args, { stdio: 'inherit' })
  if (result.status !== 0) process.exit(result.status ?? 1)
}
if (check && files.some((file, index) => !before[index]?.equals(readFileSync(file)))) throw new Error('Generated contract files drifted; run make generate and review the changes')
