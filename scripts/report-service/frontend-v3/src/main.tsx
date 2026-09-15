import './i18n'
import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { ThemeProvider } from './context/theme-provider'
import { APP_BASE } from './basePath'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ThemeProvider>
      <BrowserRouter basename={APP_BASE || '/'}>
        <App />
      </BrowserRouter>
    </ThemeProvider>
  </React.StrictMode>,
)
