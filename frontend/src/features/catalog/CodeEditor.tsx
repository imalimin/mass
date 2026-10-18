import { useEffect, useRef } from 'react'
import * as monaco from 'monaco-editor/editor/editor.api'
import 'monaco-editor/languages/definitions/markdown/register'
import 'monaco-editor/languages/definitions/yaml/register'
import EditorWorker from 'monaco-editor/editor/editor.worker?worker'

self.MonacoEnvironment = { getWorker: () => new EditorWorker() }
export default function CodeEditor({ value, onChange, label, language = 'markdown', original }: { value: string; onChange?: (value: string) => void; label: string; language?: string; original?: string }) {
  const root = useRef<HTMLDivElement>(null), editor = useRef<monaco.editor.IStandaloneCodeEditor | null>(null), latest = useRef(onChange)
  useEffect(() => { latest.current = onChange }, [onChange])
  useEffect(() => {
    if (!root.current) return
    if (original !== undefined) {
      const a = monaco.editor.createModel(original, language), b = monaco.editor.createModel(value, language)
      const diff = monaco.editor.createDiffEditor(root.current, { automaticLayout: true, readOnly: true, renderSideBySide: false, minimap: { enabled: false }, ariaLabel: label })
      diff.setModel({ original: a, modified: b }); return () => { diff.dispose(); a.dispose(); b.dispose() }
    }
    const instance = monaco.editor.create(root.current, { value, language, automaticLayout: true, ariaLabel: label, readOnly: !onChange, editContext: false, accessibilitySupport: 'on', minimap: { enabled: false }, wordWrap: 'on', scrollBeyondLastLine: false, fontSize: 13 })
    editor.current = instance
    const change = instance.onDidChangeModelContent(() => latest.current?.(instance.getValue()))
    return () => { change.dispose(); const model = instance.getModel(); instance.dispose(); model?.dispose(); editor.current = null }
    // Editor/model lifetime follows the document; controlled text updates happen below.
  }, [label, language, original])
  useEffect(() => { if (editor.current && editor.current.getValue() !== value) editor.current.setValue(value) }, [value])
  return <div className="code-editor" ref={root} data-editor={label} />
}
