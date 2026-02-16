/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        primary: {
          50: '#eff6ff',
          100: '#dbeafe',
          200: '#bfdbfe',
          300: '#93c5fd',
          400: '#60a5fa',
          500: '#3b82f6',
          600: '#2563eb',
          700: '#1d4ed8',
          800: '#1e40af',
          900: '#1e3a8a',
        },
        gmail: {
          red: '#c5221f',
          blue: '#1a73e8',
          gray: '#5f6368',
          lightGray: '#f1f3f4',
          border: '#dadce0',
          hover: '#f1f3f4',
          selected: '#e8f0fe',
        },
      },
      fontFamily: {
        sans: ['Google Sans', 'Roboto', 'sans-serif'],
      },
    },
  },
  plugins: [],
}
