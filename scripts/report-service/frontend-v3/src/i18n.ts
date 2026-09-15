import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import { resources } from './i18n-resources'

// Follow the language selected in the main new-api console when present.
const language = localStorage.getItem('i18nextLng') || 'zh'
void i18n.use(initReactI18next).init({
  resources,
  lng: language,
  fallbackLng: 'zh',
  interpolation: { escapeValue: false },
})
export default i18n
