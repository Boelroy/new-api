/** @type {import('tailwindcss').Config} */
import typography from '@tailwindcss/typography'

export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        // Blueprint color system — warm paper / cool ink / electric blue / acid lime.
        // Ported from anispark_blue_print. The neutral `gray` scale below is
        // remapped so the existing gray-*/white utility classes across every
        // page instantly adopt the blueprint palette without editing them.
        ink: '#0B1020',
        secondary: '#687083',
        border: '#D9DDD7',
        // Reverted to the original neutral app background.
        canvas: '#F9FAFB',
        paper: '#F9FAFB',
        brand: {
          DEFAULT: '#2864FF',
          50: '#E9EBFF',
          100: '#D9E1FF',
          200: '#B9CBFF',
          600: '#2864FF',
          700: '#1E4FD6',
        },
        outline: {
          DEFAULT: '#8DB7FF',
          strong: '#4D83FF',
        },
        lime: {
          DEFAULT: '#D9FF43',
          ink: '#4A5A00',
        },
        success: '#3E8E4F',
        warning: '#C97A12',
      },
      // Sharp corners: cards/inputs near-square, buttons crisp, pills/avatars stay round.
      borderRadius: {
        none: '0',
        sm: '2px',
        DEFAULT: '2px',
        md: '2px',
        lg: '3px',
        xl: '3px',
        '2xl': '4px',
        '3xl': '6px',
        full: '9999px',
      },
      letterSpacing: {
        label: '0.14em',
        tightest: '-0.055em',
        display: '-0.045em',
        tight2: '-0.03em',
      },
      boxShadow: {
        card: '0 1px 2px rgba(11, 16, 32, 0.04)',
        hover: '0 8px 30px rgba(11, 16, 32, 0.08)',
        pop: '0 24px 70px rgba(11, 16, 32, 0.16)',
      },
      backgroundImage: {
        // Engineering grid decoration.
        grid: 'linear-gradient(#D9DDD7 1px, transparent 1px), linear-gradient(90deg, #D9DDD7 1px, transparent 1px)',
      },
      backgroundSize: {
        grid: '28px 28px',
      },
      keyframes: {
        pulseRing: {
          '0%': { boxShadow: '0 0 0 0 rgba(40,100,255,0.35)' },
          '70%': { boxShadow: '0 0 0 6px rgba(40,100,255,0)' },
          '100%': { boxShadow: '0 0 0 0 rgba(40,100,255,0)' },
        },
        fadeUp: {
          '0%': { opacity: '0', transform: 'translateY(6px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' },
        },
      },
      animation: {
        pulseRing: 'pulseRing 1.8s infinite',
        fadeUp: 'fadeUp 0.25s ease-out',
      },
    },
  },
  plugins: [typography],
}
