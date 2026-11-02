import { useTranslation } from 'react-i18next'

import type { Supplier, Vendor } from './api'
import { DataTable, RequestCount } from './components'

export default function SupplierRelationships(props: {
  heads: Supplier[]
  vendors: Vendor[]
}) {
  const { t } = useTranslation()
  return (
    <div className='space-y-6'>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Each supplier belongs to a supplier head. Daily usage follows this relationship.'
        )}
      </p>
      {props.heads.map((head) => {
        const vendors = props.vendors.filter(
          (vendor) => vendor.supplier_id === head.id
        )
        return (
          <section key={head.id} className='space-y-3'>
            <h2 className='text-sm font-semibold'>{head.name}</h2>
            <DataTable headings={['Supplier', 'Keys']} empty={!vendors.length}>
              {vendors.map((vendor) => (
                <tr key={vendor.id}>
                  <td>{vendor.name}</td>
                  <td>
                    <RequestCount value={String(vendor.key_count)} />
                  </td>
                </tr>
              ))}
            </DataTable>
          </section>
        )
      })}
    </div>
  )
}
