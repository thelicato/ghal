import type { CSSProperties } from "react"

const paths = {
  shield: <><path d="M12 3 3 7v5c0 5 6 8 9 10 3-2 9-5 9-10V7z" /><path d="m8 12 3 3 5-6" /></>,
  grid: <><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></>,
  key: <><circle cx="8" cy="8" r="5" /><path d="m12 12 9 9m-5-5 3-3m-6 0 3-3" /></>,
  plus: <path d="M12 5v14M5 12h14" />,
  refresh: <><path d="M20 11a8 8 0 0 0-14-5L3 9m0-6v6h6M4 13a8 8 0 0 0 14 5l3-3m0 6v-6h-6" /></>,
  search: <><circle cx="10.5" cy="10.5" r="6.5" /><path d="m16 16 5 5" /></>,
  arrow: <><path d="M7 17 17 7M7 7h10v10" /></>,
  chevron: <path d="m9 5 7 7-7 7" />,
  check: <path d="m5 12 4 4L19 6" />,
  alert: <><path d="m10 4-8 14a2 2 0 0 0 2 3h16a2 2 0 0 0 2-3L14 4a2 2 0 0 0-4 0Z" /><path d="M12 9v4m0 4h.01" /></>,
  close: <path d="m6 6 12 12M6 18 18 6" />,
  trash: <><path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7m4-7v7" /></>,
  lock: <><rect x="5" y="10" width="14" height="11" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3m-4 5v2" /></>,
  repo: <><path d="M4 19V5a2 2 0 0 1 2-2h14v18H6a2 2 0 0 1 0-4h14M8 7h8m-8 4h5" /></>,
  github: <><path d="M9 19c-4 1-4-2-6-2m12 5v-4a3 3 0 0 0-1-2c3 0 6-1 6-6a5 5 0 0 0-1-3 5 5 0 0 0 0-4s-1-1-4 1a13 13 0 0 0-6 0C6 2 5 3 5 3a5 5 0 0 0 0 4 5 5 0 0 0-1 3c0 5 3 6 6 6a3 3 0 0 0-1 2v4" /></>,
  globe: <><circle cx="12" cy="12" r="9" /><ellipse cx="12" cy="12" rx="4" ry="9" /><path d="M3 12h18" /></>,
  download: <><path d="M12 3v12m-4-4 4 4 4-4M4 16v5h16v-5" /></>,
  clock: <><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>,
  package: <><path d="m12 3 9 5v9l-9 5-9-5V8zM3 8l9 5 9-5m-9 5v9M7 5l10 6" /></>,
} as const

export function Icon({ name, className = "", style }: { name: keyof typeof paths; className?: string; style?: CSSProperties }) {
  return <svg aria-hidden="true" className={className} style={style} width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">{paths[name]}</svg>
}
