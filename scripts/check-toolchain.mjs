import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'

const node = readFileSync('.node-version', 'utf8').trim()
if (process.version !== `v${node}`) throw new Error(`Use Node ${node}; got ${process.version}`)
const npm = JSON.parse(readFileSync('frontend/package.json', 'utf8')).packageManager.split('@')[1]
if (execFileSync('npm', ['--version'], { encoding: 'utf8' }).trim() !== npm) throw new Error(`Use npm ${npm}`)
