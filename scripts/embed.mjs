import { cp, mkdir, readdir, rm } from 'node:fs/promises'
import { resolve } from 'node:path'

const source = resolve('frontend/dist')
const target = resolve('internal/transport/http/ui/dist')
if (!(await readdir(source)).includes('index.html')) throw new Error('frontend build is missing index.html')
await rm(target, { recursive: true, force: true })
await mkdir(target, { recursive: true })
await cp(source, target, { recursive: true })
