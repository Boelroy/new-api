import React from 'react'
import ReactDOM from 'react-dom/client'
import { I18nextProvider } from 'react-i18next'

import { ThemeProvider } from '../context/theme-provider'

import '../index.css'
import App from './App'
import i18n from './i18n'

const root = document.querySelector('#root')
if (!root) throw new Error('Application root is missing')
ReactDOM.createRoot(root).render(
  <React.StrictMode>
    <I18nextProvider i18n={i18n}>
      <ThemeProvider>
        <App />
      </ThemeProvider>
    </I18nextProvider>
  </React.StrictMode>
)
