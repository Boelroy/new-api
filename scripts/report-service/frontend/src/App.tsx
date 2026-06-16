import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import Login from './pages/Login'
import Report from './pages/Report'
import KeyCapacity from './pages/KeyCapacity'
import AllKeys from './pages/AllKeys'
import KeyTester from './pages/KeyTester'

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<Report />} />
        <Route path="/keys" element={<KeyCapacity />} />
        <Route path="/allkeys" element={<AllKeys />} />
        <Route path="/tester" element={<KeyTester />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  )
}
