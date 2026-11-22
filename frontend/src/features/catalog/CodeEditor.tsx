import { useEffect, useRef } from 'react'
import * as monaco from 'monaco-editor/editor/editor.api'
import 'monaco-editor/languages/definitions/markdown/register'
import 'monaco-editor/languages/definitions/yaml/register'
import 'monaco-editor/languages/definitions/javascript/register'
import 'monaco-editor/languages/definitions/typescript/register'
import 'monaco-editor/languages/definitions/python/register'
import 'monaco-editor/languages/features/json/register'
// Keep shared Monaco services in the editor's lazy load, before the first editor initializes them.
import 'monaco-editor/languages/features/json/jsonMode'
import 'monaco-editor/languages/definitions/shell/register'
import 'monaco-editor/languages/definitions/html/register'
import 'monaco-editor/languages/definitions/css/register'
import 'monaco-editor/languages/definitions/sql/register'
import 'monaco-editor/languages/definitions/go/register'
import EditorWorker from 'monaco-editor/editor/editor.worker?worker'
import JSONWorker from 'monaco-editor/languages/features/json/json.worker?worker'

self.MonacoEnvironment = { getWorker: (_, label) => label === 'json' ? new JSONWorker() : new EditorWorker() }
export default function CodeEditor({ value, onChange, label, language = 'markdown', original, readOnly }: { value: string; onChange?: (value: string) => void; label: string; language?: string; original?: string; readOnly?: boolean }) {
  const root = useRef<HTMLDivElement>(null), editor = useRef<monaco.editor.IStandaloneCodeEditor | null>(null), latest = useRef(onChange)
  useEffect(() => { latest.current = onChange }, [onChange])
  useEffect(() => {
    if (!root.current) return
    if (original !== undefined) {
      const a = monaco.editor.createModel(original, language), b = monaco.editor.createModel(value, language)
      const diff = monaco.editor.createDiffEditor(root.current, { theme: 'vs-dark', automaticLayout: true, readOnly: true, renderSideBySide: false, minimap: { enabled: false }, ariaLabel: label })
      diff.setModel({ original: a, modified: b }); return () => { diff.dispose(); a.dispose(); b.dispose() }
    }
    const instance = monaco.editor.create(root.current, { value, language, theme: 'vs-dark', automaticLayout: true, ariaLabel: label, readOnly: readOnly ?? !onChange, editContext: false, accessibilitySupport: 'on', minimap: { enabled: false }, wordWrap: 'on', scrollBeyondLastLine: false, fontSize: 13 })
    editor.current = instance
    const change = instance.onDidChangeModelContent(() => latest.current?.(instance.getValue()))
    return () => { change.dispose(); const model = instance.getModel(); instance.dispose(); model?.dispose(); editor.current = null }
    // Editor/model lifetime follows the document; controlled text updates happen below.
  }, [label, language, original])
  useEffect(() => { if (editor.current && editor.current.getValue() !== value) editor.current.setValue(value) }, [value])
  useEffect(() => { editor.current?.updateOptions({ readOnly: readOnly ?? !onChange }) }, [label, language, onChange, readOnly])
  return <div className="code-editor" ref={root} data-editor={label} />
}
