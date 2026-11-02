import { createInstance } from 'i18next'
import { initReactI18next } from 'react-i18next'

import { billingErrorResources } from './error-resources'
import { resources } from './i18n-resources'

const i18n = createInstance()
void i18n.use(initReactI18next).init({
  resources: Object.fromEntries(
    Object.entries(resources).map(([language, value]) => [
      language,
      {
        translation: {
          ...billingErrorResources[language],
          ...value.translation,
        },
      },
    ])
  ),
  lng: localStorage.getItem('supplier-billing-language') || 'zh',
  fallbackLng: 'en',
  supportedLngs: ['en', 'zh', 'zh-TW', 'fr', 'ja', 'ru', 'vi'],
  keySeparator: false,
  interpolation: { escapeValue: false },
})
i18n.on('languageChanged', (language) => {
  document.documentElement.lang = language
})
export default i18n
