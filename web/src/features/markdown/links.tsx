import { createContext, useContext, useEffect, useMemo, useState, type AnchorHTMLAttributes, type ImgHTMLAttributes, type ReactNode } from 'react'
import { classifyMarkdownHref, type MarkdownTarget } from '../../lib/localPath'

export type MarkdownLinks = {
  cwd: string
  openFile?: (target: Extract<MarkdownTarget, { kind: 'local' }>) => void
  loadFile?: (path: string) => Promise<Blob>
}

const MarkdownLinksContext = createContext<MarkdownLinks | null>(null)

export function MarkdownLinkProvider({ cwd, openFile, loadFile, children }: MarkdownLinks & { children: ReactNode }) {
  const value = useMemo<MarkdownLinks>(() => ({ cwd, openFile, loadFile }), [cwd, openFile, loadFile])
  return <MarkdownLinksContext.Provider value={value}>{children}</MarkdownLinksContext.Provider>
}

export function useMarkdownLinks(): MarkdownLinks | null {
  return useContext(MarkdownLinksContext)
}

type AnchorProps = AnchorHTMLAttributes<HTMLAnchorElement> & { node?: unknown }

export function MarkdownLink({ node: _node, href = '', title, children }: AnchorProps) {
  const links = useMarkdownLinks()
  const target = classifyMarkdownHref(href, links?.cwd ?? '')
  if (target.kind === 'invalid') return <>{children}</>
  if (target.kind === 'anchor') return <a href={target.hash}>{children}</a>
  if (target.kind === 'external') {
    return <a href={target.url} title={title} target="_blank" rel="noreferrer">{children}</a>
  }
  const open = links?.openFile
  if (!open) return <>{children}</>
  return (
    <button type="button" className="md-local-link" data-testid="md-local-link" data-path={target.path} title={title ?? target.path} onClick={() => open(target)}>
      {children}
    </button>
  )
}

type ImageProps = ImgHTMLAttributes<HTMLImageElement> & { node?: unknown }

function MissingImage({ label, title }: { label?: string; title?: string }) {
  if (!label && !title) return null
  return <span className="md-local-image-missing" data-testid="md-local-image-missing" title={title}>{label || title}</span>
}

export function MarkdownImage({ node: _node, src = '', alt, title, ...rest }: ImageProps) {
  const links = useMarkdownLinks()
  const loader = links?.loadFile
  const target = links ? classifyMarkdownHref(src, links.cwd) : null
  const local = target?.kind === 'local' ? target.path : ''
  const [url, setURL] = useState('')
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (!local || !loader) return
    const controller = new AbortController()
    let objectURL = ''
    setFailed(false)
    setURL('')
    void loader(local)
      .then(blob => {
        if (controller.signal.aborted) return
        objectURL = URL.createObjectURL(blob)
        setURL(objectURL)
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true)
      })
    return () => {
      controller.abort()
      if (objectURL) URL.revokeObjectURL(objectURL)
    }
  }, [local, loader])

  if (!links) {
    return /^(https?:|data:|\/)/i.test(src)
      ? <img alt={alt ?? ''} title={title} loading="lazy" decoding="async" src={src} {...rest} />
      : <MissingImage label={alt} title={src || title} />
  }
  if (target?.kind === 'external') return <img alt={alt ?? ''} title={title} loading="lazy" decoding="async" src={src} {...rest} />
  if (!local) return <MissingImage label={alt} title={src || title} />
  if (failed) return <MissingImage label={alt} title={local} />
  if (!url) return <span className="md-local-image-loading" data-testid="md-local-image-loading" aria-hidden />
  return <img alt={alt ?? ''} title={title ?? local} loading="lazy" decoding="async" src={url} data-path={local} {...rest} />
}
