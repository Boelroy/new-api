import { useState } from 'react'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { BatchKeyInput } from '../BatchKeyInput'

function Editor(props: { initial?: string; disabled?: boolean }) {
  const [value, setValue] = useState(props.initial ?? '')
  return <BatchKeyInput value={value} onChange={setValue} disabled={props.disabled} />
}
describe('batch Key editor', () => {
  it('retains a note without quota when switching text to table and back', async () => {
    const user = userEvent.setup()
    render(<Editor initial="key-one note without quota" />)
    await user.click(screen.getByRole('button', { name: 'Table' }))
    expect(screen.getByRole('textbox', { name: 'Remark 1' })).toHaveValue('note without quota')
    await user.type(screen.getByRole('spinbutton', { name: 'Quota 1' }), '12.5')
    await user.click(screen.getByRole('button', { name: 'Text' }))
    expect(screen.getByRole('textbox')).toHaveValue('key-one 12.5 note without quota')
  })
  it('previews masked keys and removes duplicates only after an explicit action', async () => {
    const user = userEvent.setup()
    render(<Editor initial={'sk-long-secret-one 10 first\nsk-long-secret-one 20 second\nshort'} />)
    expect(screen.getByRole('status')).toHaveTextContent('Duplicate keys: 1')
    await user.click(screen.getByRole('button', { name: 'Preview' }))
    expect(screen.queryByText('sk-long-secret-one')).not.toBeInTheDocument()
    expect(screen.queryByText('short', { exact: true })).not.toBeInTheDocument()
    expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(4)
    await user.click(screen.getByRole('button', { name: 'Remove Duplicates' }))
    expect(screen.getByRole('status')).toHaveTextContent('Duplicate keys: 0')
    await user.click(screen.getByRole('button', { name: 'Text' }))
    expect(screen.getByRole('textbox')).toHaveValue('sk-long-secret-one 10 first\nshort')
  })
  it('supports empty table rows and keeps the remaining row after deleting an entry', async () => {
    const user = userEvent.setup()
    render(<Editor />)
    await user.click(screen.getByRole('button', { name: 'Table' }))
    await user.type(screen.getByRole('textbox', { name: 'Key 1' }), 'first')
    await user.click(screen.getByRole('button', { name: 'Add' }))
    await user.type(screen.getByRole('textbox', { name: 'Key 2' }), 'second')
    await user.click(screen.getByRole('button', { name: 'Delete 1' }))
    await user.click(screen.getByRole('button', { name: 'Text' }))
    expect(screen.getByRole('textbox')).toHaveValue('second')
  })
  it('disables input and mode changes while submitting', () => {
    render(<Editor disabled />)
    expect(screen.getByRole('textbox')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Table' })).toBeDisabled()
  })
})
