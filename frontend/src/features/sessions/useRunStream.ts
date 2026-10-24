import { useEffect, useState } from 'react'
import { AguiRunClient, type StreamValue } from './AguiRunClient'
export function useRunStream(space: string, session: string, run: string, generation: number) {
  const scope = `${space}/${session}/${run}/${generation}`
  const empty: StreamValue = { state: null, cursor: '0', error: '', connected: false }
  const [current, setCurrent] = useState({ scope, value: empty })
  useEffect(() => {
    const key = `${space}/${session}/${run}/${generation}`
    setCurrent({ scope: key, value: { state: null, cursor: '0', error: '', connected: false } })
    if (!run) return
    const client = new AguiRunClient(space, session, run, value => setCurrent({ scope: key, value }))
    void client.start()
    return () => client.stop()
  }, [space, session, run, generation])
  return current.scope === scope ? current.value : empty
}
