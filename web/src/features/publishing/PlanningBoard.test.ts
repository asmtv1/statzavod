import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { createElement } from 'react'
import { monthRange, PublicationIdentity } from './PlanningBoard'

describe('planning calendar month boundaries', () => {
  it('uses exclusive UTC month boundaries across year end', () => {
    expect(monthRange(new Date(Date.UTC(2026, 11, 20)))).toEqual({ from:'2026-12-01T00:00:00.000Z', until:'2027-01-01T00:00:00.000Z' })
  })
  it('keeps leap February bounded without a local timezone shift', () => {
    expect(monthRange(new Date(Date.UTC(2028, 1, 3)))).toEqual({ from:'2028-02-01T00:00:00.000Z', until:'2028-03-01T00:00:00.000Z' })
  })
})

describe('successful publication identity', () => {
  it('renders the canonical backend permalink in a protected new tab', () => {
    render(createElement(PublicationIdentity, { target:{ platform:'YOUTUBE', status:'SUCCEEDED', externalId:'AbCdEf_1234', externalUrl:'https://www.youtube.com/shorts/AbCdEf_1234' }, t:() => 'Open publication' }))
    const link = screen.getByRole('link', { name:'Open publication' })
    expect(link).toHaveAttribute('href', 'https://www.youtube.com/shorts/AbCdEf_1234')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('does not render a poisoned provider URL even for a claimed success', () => {
    const { container } = render(createElement(PublicationIdentity, { target:{ platform:'YOUTUBE', status:'SUCCEEDED', externalUrl:'javascript:alert(1)?X-Amz-Signature=secret' }, t:(key: string) => key }))
    expect(container).toBeEmptyDOMElement()
  })
})
