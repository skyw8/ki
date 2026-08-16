import type { ReactNode } from 'react'

export function Icon({ children }: { children: ReactNode }) {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      {children}
    </svg>
  )
}

export const IPlus = () => <Icon><path d="M8 3v10M3 8h10" /></Icon>
export const IPanel = () => <Icon><rect x="2.5" y="2.5" width="11" height="11" rx="2" /><path d="M6.5 3v10" /></Icon>
export const ISun = () => <Icon><circle cx="8" cy="8" r="2.4" /><path d="M8 2.5v1.4M8 12.1v1.4M2.5 8h1.4M12.1 8h1.4M4.1 4.1l1 1M10.9 10.9l1 1M4.1 11.9l1-1M10.9 5.1l1-1" /></Icon>
export const IMoon = () => <Icon><path d="M10.5 2.8A5.5 5.5 0 1 0 13.2 10 4.3 4.3 0 0 1 10.5 2.8Z" /></Icon>
export const IGear = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" aria-hidden>
    <path fillRule="evenodd" d="M6.35 1.7h3.3l.4 1.58c.4.13.78.33 1.13.58l1.55-.52 1.65 2.86-1.26 1.02c.07.4.12.8.12 1.18s-.05.78-.12 1.18l1.26 1.02-1.65 2.86-1.55-.52a5.1 5.1 0 0 1-1.13.58l-.4 1.58h-3.3l-.4-1.58a5.1 5.1 0 0 1-1.13-.58l-1.55.52-1.65-2.86 1.26-1.02A5.7 5.7 0 0 1 2.85 8c0-.38.05-.78.12-1.18L1.71 5.8l1.65-2.86 1.55.52c.35-.25.73-.45 1.13-.58l.4-1.58ZM8 5.85A2.15 2.15 0 1 0 8 10.15 2.15 2.15 0 0 0 8 5.85Z" />
  </svg>
)
export const ISend = () => <Icon><path d="M3 8h10M9 4.5 13 8l-4 3.5" /></Icon>
export const IStop = () => <Icon><rect x="4.5" y="4.5" width="7" height="7" rx="1.2" fill="currentColor" stroke="none" /></Icon>
export const IFork = () => <Icon><circle cx="4.5" cy="4" r="1.4" /><circle cx="4.5" cy="12" r="1.4" /><circle cx="11.5" cy="8" r="1.4" /><path d="M4.5 5.4v5.2M4.5 8h5.4" /></Icon>
export const ISearch = () => <Icon><circle cx="7" cy="7" r="3.4" /><path d="m12.5 12.5-2.4-2.4" /></Icon>
export const IClose = () => <Icon><path d="m4 4 8 8M12 4 4 12" /></Icon>
export const IWrench = () => (
  <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
    <path d="M14 3.3a3.8 3.8 0 0 1-4.8 4.8l-5.1 5.1a1.6 1.6 0 1 1-2.3-2.3l5.1-5.1A3.8 3.8 0 0 1 11.7 1l-2.3 2.3 2.3 2.3L14 3.3Z" />
  </svg>
)
export const ISpark = () => (
  <svg width="13" height="13" viewBox="0 0 16 16" fill="currentColor" aria-hidden>
    <path d="M8 1.4 9.2 6.1 14 8 9.2 9.9 8 14.6 6.8 9.9 2 8l4.8-1.9L8 1.4Z" />
  </svg>
)
export const IUser = () => (
  <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden>
    <circle cx="8" cy="5.5" r="2.3" />
    <path d="M3.4 13c.7-2.3 2.4-3.4 4.6-3.4s3.9 1.1 4.6 3.4" />
  </svg>
)
export const ICompact = () => (
  <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
    <path d="m2.5 2.5 3.75 3.75M3 6.25h3.25V3" />
    <path d="m13.5 2.5-3.75 3.75M13 6.25H9.75V3" />
    <path d="m2.5 13.5 3.75-3.75M3 9.75h3.25V13" />
    <path d="m13.5 13.5-3.75-3.75M13 9.75H9.75V13" />
  </svg>
)
export const ICopy = () => (
  <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
    <rect x="5.5" y="5.5" width="8" height="8" rx="1.4" />
    <path d="M10.5 5.4V4.2A1.2 1.2 0 0 0 9.3 3H3.7A1.2 1.2 0 0 0 2.5 4.2v5.6A1.2 1.2 0 0 0 3.7 11h1.2" />
  </svg>
)
export const IEdit = () => (
  <Icon>
    <path d="M9.1 3.2 12.8 6.9 6.2 13.5H2.5v-3.7L9.1 3.2Z" />
    <path d="m8 4.3 3.7 3.7" />
  </Icon>
)
export const IRegen = () => (
  <Icon>
    <path d="M13.2 8A5.2 5.2 0 1 1 11.3 3.9" />
    <path d="M11.2 2.2v2.6h2.6" />
  </Icon>
)
export const IClock = () => (
  <Icon>
    <circle cx="8" cy="8" r="5.2" />
    <path d="M8 5v3.2l2.2 1.3" />
  </Icon>
)
export const IFold = () => (
  <Icon>
    <path d="M3 4.2h10M3 8h7M3 11.8h10" />
    <path d="m11.2 6.4 2.4 1.6-2.4 1.6" />
  </Icon>
)
export const ITail = () => (
  <Icon>
    <path d="M8 2.8v8" />
    <path d="m4.8 8.4 3.2 3.2 3.2-3.2" />
    <path d="M3.6 13.2h8.8" />
  </Icon>
)
export const IChev = ({ open }: { open?: boolean }) => (
  <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" style={{ transform: open ? 'rotate(90deg)' : undefined, transition: 'transform .15s' }} aria-hidden>
    <path d="m6 3.5 5 4.5-5 4.5" />
  </svg>
)
