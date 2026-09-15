import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

// cn merges conditional class names and resolves Tailwind conflicts, matching
// the helper new-api's primitives expect.
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}
