/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { z } from 'zod'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth-store'
import { SignIn } from '@/features/auth/sign-in'
import { api } from '@/lib/api'

const searchSchema = z.object({
  redirect: z.string().optional(),
})

export const Route = createFileRoute('/(auth)/sign-in')({
  component: SignIn,
  validateSearch: searchSchema,
  beforeLoad: async ({ search }) => {
    const { auth } = useAuthStore.getState()

    if (auth.user) {
      const redirectTo = search?.redirect || '/dashboard'

      // External same-hostname redirect (cross-port SSO): fetch token first
      if (redirectTo.startsWith('http://') || redirectTo.startsWith('https://')) {
        try {
          const target = new URL(redirectTo)
          const current = new URL(window.location.href)
          if (target.hostname === current.hostname) {
            const resp = await api.get<{ success: boolean; token: string }>('/api/user/sso-token')
            if (resp.data?.success && resp.data.token) {
              const sep = redirectTo.includes('?') ? '&' : '?'
              window.location.href = `${redirectTo}${sep}sso_token=${resp.data.token}`
              return // browser is navigating away
            }
          }
        } catch {
          // SSO failed — fall through to dashboard
        }
        throw redirect({ to: '/dashboard' })
      }

      throw redirect({ to: redirectTo })
    }
  },
})
