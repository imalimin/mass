const paths = {
  arrowLeft: 'm12 19-7-7 7-7M5 12h14',
  arrowUp: 'M12 19V5m-7 7 7-7 7 7',
  arrowDown: 'M12 5v14m-7-7 7 7 7-7',
  plus: 'M12 5v14M5 12h14',
  close: 'm6 6 12 12M18 6 6 18',
  check: 'm5 12 4 4L19 6',
  chevron: 'm9 6 6 6-6 6',
  chat: 'M20 4H4a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h2v3l4-3h10a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2ZM8 11.5h8',
  bot: 'M12 7V5m-2 7h.01m3.99 0h.01M7 7h10a4 4 0 0 1 4 4v5a3 3 0 0 1-3 3H6a3 3 0 0 1-3-3v-5a4 4 0 0 1 4-4ZM13.5 3.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0Z',
  copy: 'M10 8h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-8a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2Zm6 0V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2',
  attach: 'm21 11.5-8.8 8.8a5 5 0 0 1-7.1-7.1l9.3-9.3a3.5 3.5 0 0 1 5 5l-9.3 9.3a2 2 0 0 1-2.8-2.8l8.5-8.5',
  activity: 'M3 10v4m4-7v10m5-14v18m5-14v10m4-7v4',
  folder: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z',
  file: 'M13 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V10Zm0 0v7h7',
  panel: 'M5 4h14a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2Zm10 0v16',
  code: 'm8 6-6 6 6 6m8-12 6 6-6 6',
  download: 'M12 3v12m-5-5 5 5 5-5M4 16v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3',
  upload: 'M12 15V3m-5 5 5-5 5 5M4 16v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3',
  play: 'M6 4v16l14-8Z',
  stop: 'M7 6h10a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1Z',
  rocket: 'M12 15 9 12m3 3 4-2a9 9 0 0 0 5-10 9 9 0 0 0-10 5l-2 4m3 3v4l-3 2-1-5-5-1 2-3h4m-3 5-3 3m14-12h.01',
  refresh: 'M20 9a8 8 0 0 0-14-3L3 9m0-5v5h5m-4 6a8 8 0 0 0 14 3l3-3m0 5v-5h-5',
  alert: 'm10.3 4.8-8 14A1.5 1.5 0 0 0 3.6 21h16.8a1.5 1.5 0 0 0 1.3-2.2l-8-14a2 2 0 0 0-3.4 0ZM12 9v4m0 4h.01',
  sparkle: 'M12 3c0 5-4 9-9 9 5 0 9 4 9 9 0-5 4-9 9-9-5 0-9-4-9-9Z',
  layers: 'm12 3 9 5-9 5-9-5Zm-9 9 9 5 9-5M3 16l9 5 9-5',
  grid: 'M4 4h6v6H4Zm10 0h6v6h-6ZM4 14h6v6H4Zm10 0h6v6h-6Z',
  list: 'M4 5h16v14H4Zm4 5h8m-8 4h6',
  flask: 'M9 3h6m-5 0v7l-6 9a1 1 0 0 0 1 2h14a1 1 0 0 0 1-2l-6-9V3M7 15h10',
  chip: 'M7 6h10a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1Zm2-3v3m6-3v3M9 18v3m6-3v3M3 9h3m-3 6h3m12-6h3m-3 6h3',
  user: 'M4 21v-2a4 4 0 0 1 4-4h8a4 4 0 0 1 4 4v2M16 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0Z',
  branch: 'M6 6v12m0-4c0-5 12-1 12-8M8 4a2 2 0 1 1-4 0 2 2 0 0 1 4 0Zm12 0a2 2 0 1 1-4 0 2 2 0 0 1 4 0ZM8 20a2 2 0 1 1-4 0 2 2 0 0 1 4 0Z',
  settings: 'm9.2 3-.6 2.2-1.4.8L5 5.5 2.3 10l1.6 1.7v1.6L2.3 15 5 19.5l2.2-.5 1.4.8.6 2.2h5.2l.6-2.2 1.4-.8 2.2.5 2.7-4.5-1.6-1.7v-1.6l1.6-1.7-2.7-4.5-2.2.5-1.4-.8-.6-2.2ZM15 12.5a3.2 3.2 0 1 1-6.4 0 3.2 3.2 0 0 1 6.4 0Z',
  logout: 'M10 4H5a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h5m5-3 5-5-5-5m-6 5h11',
} as const

export function Icon({ name, size = 18, className }: { name: keyof typeof paths; size?: number; className?: string }) {
  return <svg className={className ? `icon ${className}` : 'icon'} width={size} height={size} viewBox="0 0 24 24"
    fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d={paths[name]} />
  </svg>
}
