import { createContext, useContext } from 'react'

export type Theme = 'light' | 'dark'

export type ThemeContextValue = {
  theme: Theme
  toggle: () => void
}

export const ThemeContext = createContext<ThemeContextValue>({ theme: 'light', toggle: () => {} })

export function useTheme(): ThemeContextValue {
  return useContext(ThemeContext)
}
