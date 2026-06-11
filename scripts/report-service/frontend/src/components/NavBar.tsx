import { NavLink } from 'react-router-dom'
import { api } from '../api'

export default function NavBar() {
  const handleLogout = async () => {
    await api.logout()
    window.location.href = '/login'
  }

  return (
    <nav className="flex items-center gap-4 border-b border-gray-200 pb-3 mb-6 text-sm">
      <NavLink to="/" end className={({ isActive }) => isActive ? 'font-semibold text-gray-900' : 'text-gray-500 hover:text-gray-900'}>
        Report
      </NavLink>
      <NavLink to="/keys" className={({ isActive }) => isActive ? 'font-semibold text-gray-900' : 'text-gray-500 hover:text-gray-900'}>
        Key Capacity
      </NavLink>
      <NavLink to="/allkeys" className={({ isActive }) => isActive ? 'font-semibold text-gray-900' : 'text-gray-500 hover:text-gray-900'}>
        All Keys
      </NavLink>
      <button onClick={handleLogout} className="ml-auto text-gray-400 hover:text-gray-700">
        退出
      </button>
    </nav>
  )
}
