import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import CompanyStatus from './CompanyStatus'

it('separates company approval from verified publication and links the actual ticket', () => {
  const ticket = { id: 'ORDER-42', url: 'https://company.example/orders/42', changeId: 'change-7', changeVersion: 'v2' }
  const { rerender } = render(<CompanyStatus operation={{ state: 'WAITING_PUBLICATION', ticket }} />)
  expect(screen.getByText('公司审批通过，等待自动发布')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'ORDER-42' })).toHaveAttribute('href', ticket.url)
  expect(screen.queryByText(/核验通过/)).not.toBeInTheDocument()
  rerender(<CompanyStatus operation={{ state: 'PUBLISHED', ticket, observation: { state: 'PUBLISHED', observedAt: '2026-11-17', effectiveVersion: 'published-v2', publication: { id: 'release-42', state: 'PUBLISHED' } } }} />)
  expect(screen.getByText('已发布，测试环境版本与配置值核验通过')).toBeInTheDocument()
  expect(screen.getByText('测试环境生效版本：published-v2')).toBeInTheDocument()
})

it.each(['javascript:alert(1)', 'https://user:secret@company.example/order', 'http://company.example/order'])('does not render an unsafe ticket link: %s', url => {
  render(<CompanyStatus operation={{ state: 'SUBMISSION_UNKNOWN', errorCode: 'ticket_outcome_unknown', ticket: { id: 'ORDER-42', url, changeId: 'change-7', changeVersion: 'v2' } }} />)
  expect(screen.queryByRole('link')).not.toBeInTheDocument()
  expect(screen.getByText('正在按原请求查询工单，不会重复提单。')).toBeInTheDocument()
})
