import { useState } from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { SidePanel } from '../SidePanel'
import { ConfirmHost, confirmDialog } from '../feedback'
import { FormSection } from '../FormSection'

function Panel(props: { busy?: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button onClick={() => setOpen(true)}>Open upload</button>
      {open && (
        <SidePanel
          title="Batch upload"
          busy={props.busy}
          onClose={() => setOpen(false)}
          footer={<button>Submit</button>}
        >
          <label>
            Name
            <input />
          </label>
        </SidePanel>
      )}
    </>
  )
}
describe('management dialogs', () => {
  it('moves focus inside the drawer, closes with Escape and restores the opening button', async () => {
    const user = userEvent.setup()
    render(<Panel />)
    const trigger = screen.getByRole('button', { name: 'Open upload' })
    await user.click(trigger)
    const dialog = screen.getByRole('dialog', { name: 'Batch upload' })
    await waitFor(() => expect(dialog).toContainElement(document.activeElement as HTMLElement))
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(trigger).toHaveFocus()
  })
  it('keeps a submitting drawer open and freezes fields for Escape and close-button actions', async () => {
    const user = userEvent.setup()
    render(<Panel busy />)
    await user.click(screen.getByRole('button', { name: 'Open upload' }))
    expect(screen.getByRole('textbox', { name: 'Name' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Close dialog' })).toBeDisabled()
    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
  it('resolves destructive confirmation as cancelled on Escape', async () => {
    const user = userEvent.setup()
    const result = vi.fn()
    render(
      <>
        <button
          onClick={() => {
            void confirmDialog({
              title: 'Delete channels',
              message: 'Delete selected channels?',
              danger: true,
            }).then(result)
          }}
        >
          Delete
        </button>
        <ConfirmHost />
      </>,
    )
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    expect(screen.getByRole('dialog', { name: 'Delete channels' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(result).toHaveBeenCalledWith(false))
  })
  it('keeps advanced fields available behind an expandable section', async () => {
    const user = userEvent.setup()
    render(
      <FormSection advanced>
        <label>
          Models
          <input defaultValue="model-one" />
        </label>
      </FormSection>,
    )
    const summary = screen.getByText('Advanced Settings')
    expect(summary.closest('details')).not.toHaveAttribute('open')
    await user.click(summary)
    expect(summary.closest('details')).toHaveAttribute('open')
    expect(screen.getByRole('textbox', { name: 'Models' })).toHaveValue('model-one')
  })
})
