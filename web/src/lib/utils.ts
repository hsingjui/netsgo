import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function createLocalId(prefix = "id") {
  if (typeof globalThis !== "undefined") {
    const c = globalThis.crypto
    if (c && typeof c.randomUUID === "function") {
      return c.randomUUID()
    }
  }
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

export async function copyText(value: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value)
      return
    } catch {
      // Permission denied (e.g. unfocused document); fall through to execCommand.
    }
  }

  const textarea = document.createElement("textarea")
  textarea.value = value
  textarea.style.position = "fixed"
  textarea.style.opacity = "0"
  document.body.appendChild(textarea)
  textarea.select()
  document.execCommand("copy")
  document.body.removeChild(textarea)
}
