import { Select } from '@base-ui/react/select'
import { Check, ChevronsUpDown } from 'lucide-react'
import { ProviderOption } from './ProviderMark'

type Provider = { id: string; label: string; type: number }
export function ProviderSelect<T extends Provider>(props: {
  value: string
  options: T[]
  onChange: (value: T) => void
  label: string
}) {
  return (
    <Select.Root
      value={props.value}
      onValueChange={value => {
        const provider = props.options.find(option => option.id === value)
        if (provider) props.onChange(provider)
      }}
    >
      <Select.Trigger
        aria-label={props.label}
        className="flex h-10 w-full items-center justify-between gap-2 rounded-lg border border-input bg-background px-3 text-sm outline-none focus-visible:ring-3 focus-visible:ring-ring/30"
      >
        <Select.Value>
          {() => {
            const current = props.options.find(option => option.id === props.value)
            return current ? <ProviderOption type={current.type} label={current.label} size={20} /> : null
          }}
        </Select.Value>
        <Select.Icon>
          <ChevronsUpDown className="size-4 text-muted-foreground" aria-hidden="true" />
        </Select.Icon>
      </Select.Trigger>
      <Select.Portal>
        <Select.Positioner alignItemWithTrigger={false} sideOffset={6} className="z-[80] outline-none">
          <Select.Popup className="max-h-[var(--available-height)] w-[var(--anchor-width)] overflow-auto rounded-xl border border-border bg-popover p-1 text-popover-foreground shadow-lg">
            <Select.List>
              {props.options.map(option => (
                <Select.Item
                  key={option.id}
                  value={option.id}
                  aria-label={option.label}
                  className="flex cursor-default items-center gap-2 rounded-lg px-3 py-2 text-sm outline-none data-highlighted:bg-accent data-selected:font-medium"
                >
                  <Select.ItemText>
                    <ProviderOption type={option.type} label={option.label} size={20} />
                  </Select.ItemText>
                  <Select.ItemIndicator className="ml-auto">
                    <Check className="size-4" aria-hidden="true" />
                  </Select.ItemIndicator>
                </Select.Item>
              ))}
            </Select.List>
          </Select.Popup>
        </Select.Positioner>
      </Select.Portal>
    </Select.Root>
  )
}
