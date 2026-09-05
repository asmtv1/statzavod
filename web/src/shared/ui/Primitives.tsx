import { createPortal } from 'react-dom'
import { useId, useRef, type ButtonHTMLAttributes, type HTMLAttributes, type ReactNode } from 'react'
import { useDialogFocus } from './dialogFocus'
import styles from './Primitives.module.scss'

export function PageHeader({ eyebrow, title, description, actions }: { eyebrow?: ReactNode; title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return <header className={styles.pageHeader}><div>{eyebrow ? <p>{eyebrow}</p> : null}<h1>{title}</h1>{description ? <span>{description}</span> : null}</div>{actions ? <div className={styles.headerActions}>{actions}</div> : null}</header>
}

export function Panel({ className = '', ...props }: HTMLAttributes<HTMLElement>) {
  return <section className={`${styles.panel} ${className}`} {...props} />
}

export function Field({ label, hint, error, children }: { label: ReactNode; hint?: ReactNode; error?: ReactNode; children: ReactNode }) {
  return <label className={styles.field}><span>{label}</span>{children}{hint ? <small>{hint}</small> : null}{error ? <small className={styles.fieldError}>{error}</small> : null}</label>
}

export function Tabs({ label, items, value, onChange }: { label: string; items: { value: string; label: ReactNode }[]; value: string; onChange: (value: string) => void }) {
  return <div className={styles.tabs} role="tablist" aria-label={label}>{items.map(item => <button key={item.value} type="button" role="tab" aria-selected={value === item.value} tabIndex={value === item.value ? 0 : -1} onClick={() => onChange(item.value)}>{item.label}</button>)}</div>
}

export function StatusBadge({ tone = 'neutral', children }: { tone?: 'neutral' | 'success' | 'warning' | 'danger'; children: ReactNode }) {
  return <span className={`${styles.badge} ${styles[tone]}`}>{children}</span>
}

export function DangerButton({ className = '', ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button className={`${styles.dangerButton} ${className}`} {...props} />
}

export function Modal({ open, title, description, closeLabel, onClose, children }: { open: boolean; title: ReactNode; description?: ReactNode; closeLabel: string; onClose: () => void; children: ReactNode }) {
  const titleID = useId()
  const descriptionID = useId()
  const dialogRef = useRef<HTMLDivElement>(null)
  useDialogFocus(open, dialogRef, onClose)
  if (!open) return null
  return createPortal(<div className={styles.overlay} onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}><div ref={dialogRef} className={styles.modal} role="dialog" aria-modal="true" aria-labelledby={titleID} aria-describedby={description ? descriptionID : undefined} tabIndex={-1}><header><div><h2 id={titleID}>{title}</h2>{description ? <p id={descriptionID}>{description}</p> : null}</div><button type="button" onClick={onClose} aria-label={closeLabel}>×</button></header>{children}</div></div>, document.body)
}
