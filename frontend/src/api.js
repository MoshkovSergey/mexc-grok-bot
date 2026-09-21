// Simple API client. In Docker, nginx proxies /api to backend.
// In Vite dev mode, vite.config.js proxies /api to localhost:8080.

const API_BASE = import.meta.env.VITE_API_BASE || ''

async function handle(res) {
  if (!res.ok) {
    let detail = ''
    try {
      const j = await res.json()
      detail = j.error || JSON.stringify(j)
    } catch {
      detail = await res.text().catch(() => '')
    }
    throw new Error(`${res.method} ${res.url} failed: ${res.status} ${detail}`)
  }
  return res.json()
}

export async function getJSON(path) {
  return handle(await fetch(`${API_BASE}${path}`, {
    method: 'GET',
    headers: { Accept: 'application/json' },
  }))
}

export async function postJSON(path) {
  return handle(await fetch(`${API_BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
  }))
}

export async function putJSON(path, body) {
  return handle(await fetch(`${API_BASE}${path}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  }))
}