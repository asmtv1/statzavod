import type { ButtonHTMLAttributes, PropsWithChildren } from 'react'
import styles from './Button.module.scss'
export function Button({ children, className = '', ...props }: PropsWithChildren<ButtonHTMLAttributes<HTMLButtonElement>>) { return <button className={`${styles.button} ${className}`} {...props}>{children}</button> }
