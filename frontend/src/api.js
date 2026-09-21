// Simple API client. In Docker, nginx proxies /api to backend.
// In Vite dev mode, vite.config.js proxies /api to localhost:8080.

const API_BASE = import.meta.env.VITE_API_BASE || ''

export async function getJSON(path) {
  const res = await fetch(`${API_BASE}${path}`, {
    method: 'GET',
    headers: {
      Accept: 'application/json',
    },
  })

  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(`GET ${path} failed: ${res.status} ${text}`)
  }

  return res.json()
}

export async function postJSON(path) {
  const res = await fetch(`${API_BASE}${path}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json',
    },
  })

  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(`POST ${path} failed: ${res.status} ${text}`)
  }

  return res.json()
}