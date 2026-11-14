declare module 'js-yaml' {
  export const CORE_SCHEMA: object
  export function load(input: string, options: {
    schema: object
    listener: (event: 'open' | 'close', node: { anchor: string | null }) => void
  }): unknown
}
