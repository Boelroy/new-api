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
import { useNavigate } from '@tanstack/react-router'
import i18n from 'i18next'
import { useAuthStore } from '@/stores/auth-store'
import { getSelf, api } from '@/lib/api'
import type { User } from '@/features/users/types'
import { saveUserId } from '../lib/storage'

function getSavedLanguage(user: User): string | undefined {
  const userData = user as Record<string, unknown>
  if (typeof userData.language === 'string') {
    return userData.language
  }

  if (typeof userData.setting !== 'string') {
    return undefined
  }

  try {
    const setting = JSON.parse(userData.setting) as { language?: unknown }
    return typeof setting.language === 'string' ? setting.language : undefined
  } catch {
    return undefined
  }
}

/**
 * Hook for handling authentication redirects and user data management
 */
export function useAuthRedirect() {
  const navigate = useNavigate()
  const { auth } = useAuthStore()

  /**
   * Handle successful login
   * @param userData - Optional user data from login response
   * @param redirectTo - Redirect path after login
   */
  const handleLoginSuccess = async (
    userData?: { id?: number } | null,
    redirectTo?: string
  ) => {
    // Save user ID if available
    if (userData?.id) {
      saveUserId(userData.id)
    }

    // Fetch and set user data
    try {
      const self = await getSelf()
      if (self?.success && self.data) {
        const user = self.data as User
        auth.setUser(user)

        // Update user ID if not already set
        if (user.id) {
          saveUserId(user.id)
        }

        // Restore saved language preference
        const savedLang = getSavedLanguage(user)
        if (savedLang && savedLang !== i18n.language) {
          i18n.changeLanguage(savedLang)
        }
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to fetch user data:', error)
    }

    // Navigate to target page
    const targetPath = redirectTo || '/dashboard'

    // External URL (cross-port SSO): only allow same hostname, different port
    if (targetPath.startsWith('http://') || targetPath.startsWith('https://')) {
      let allowed = false
      try {
        const target = new URL(targetPath)
        const current = new URL(window.location.href)
        // Only allow same hostname (IP or domain), port may differ
        allowed = target.hostname === current.hostname
      } catch {
        allowed = false
      }

      if (!allowed) {
        // Reject external redirect — fall back to dashboard
        navigate({ to: '/dashboard', replace: true })
        return
      }

      try {
        const resp = await api.get<{ success: boolean; token: string }>('/api/user/sso-token')
        if (resp.data?.success && resp.data.token) {
          const sep = targetPath.includes('?') ? '&' : '?'
          window.location.href = `${targetPath}${sep}sso_token=${resp.data.token}`
          return
        }
      } catch {
        // SSO not configured — do not redirect with empty token
      }
      // SSO failed: go to dashboard, don't leak anything
      navigate({ to: '/dashboard', replace: true })
      return
    }

    navigate({ to: targetPath, replace: true })
  }

  /**
   * Redirect to 2FA page
   */
  const redirectTo2FA = () => {
    navigate({ to: '/otp', replace: true })
  }

  /**
   * Redirect to login page
   */
  const redirectToLogin = () => {
    navigate({ to: '/sign-in', replace: true })
  }

  /**
   * Redirect to register page
   */
  const redirectToRegister = () => {
    navigate({ to: '/sign-up', replace: true })
  }

  return {
    handleLoginSuccess,
    redirectTo2FA,
    redirectToLogin,
    redirectToRegister,
  }
}
