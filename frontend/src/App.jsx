import { useCallback, useEffect, useState } from 'react'
import { getJSON, postJSON } from './api.js'

function fmtNum(value, digits = 2) {
  const n = Number(value)
  if (!Number.isFinite(n)) return (0).toFixed(digits)
  return n.toFixed(digits)
}

function fmtPct(value) {
  const n = Number(value)
  if (!Number.isFinite(n)) return '0.00%'
  return `${(n * 100).toFixed(2)}%`
}

function fmtTime(value) {
  if (!value) return '-'
  try {
    return new Date(value).toLocaleString()
  } catch {
    return String(value)
  }
}

export default function App() {
  const [data, setData] = useState(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    try {
      setError('')
      const next = await getJSON('/api/dashboard')
      setData(next)
    } catch (e) {
      setError(e.message || String(e))
    }
  }, [])

  useEffect(() => {
    load()
    const id = setInterval(load, 5000)
    return () => clearInterval(id)
  }, [load])

  const start = async () => {
    setLoading(true)
    try {
      await postJSON('/api/bot/start')
      await load()
    } catch (e) {
      setError(e.message || String(e))
    } finally {
      setLoading(false)
    }
  }

  const stop = async () => {
    setLoading(true)
    try {
      await postJSON('/api/bot/stop')
      await load()
    } catch (e) {
      setError(e.message || String(e))
    } finally {
      setLoading(false)
    }
  }

  const candles = Array.isArray(data?.candles) ? data.candles : []
  const orders = Array.isArray(data?.orders) ? data.orders : []
  const snapshots = Array.isArray(data?.snapshots) ? data.snapshots : []
  const riskEvents = Array.isArray(data?.riskEvents) ? data.riskEvents : []

  const recentCandles = [...candles].slice(-20).reverse()

  return (
    <div className="app">
      <header className="header">
        <div>
          <h1>MEXC Grok-like Bot</h1>
          <p className="subtitle">
            Research / paper-trading scaffold. Не является финансовой рекомендацией.
          </p>
        </div>

        <div className="controls">
          <button onClick={start} disabled={loading}>
            Start
          </button>
          <button className="secondary" onClick={stop} disabled={loading}>
            Stop
          </button>
          <button className="ghost" onClick={load} disabled={loading}>
            Refresh
          </button>
        </div>
      </header>

      {error && <div className="error">{error}</div>}

      {!data ? (
        <div className="card">Загрузка...</div>
      ) : (
        <>
          <section className="grid">
            <div className="card">
              <div className="label">Status</div>
              <div className="value">{data.status}</div>
              <div className="hint">
                mode: {data.mode} | running: {String(data.running)}
              </div>
            </div>

            <div className="card">
              <div className="label">Symbol</div>
              <div className="value">{data.symbol}</div>
              <div className="hint">interval: {data.interval}</div>
            </div>

            <div className="card">
              <div className="label">Equity</div>
              <div className="value">{fmtNum(data.equity)}</div>
              <div className="hint">
                cash: {fmtNum(data.cash)} | pos value: {fmtNum(data.positionValue)}
              </div>
            </div>

            <div className="card">
              <div className="label">Position</div>
              <div className="value">{fmtNum(data.positionQty, 8)}</div>
              <div className="hint">entry: {fmtNum(data.entryPrice)}</div>
            </div>

            <div className="card">
              <div className="label">Last Signal</div>
              <div className="value">{data.lastSignal || 'none'}</div>
              <div className="hint">updated: {fmtTime(data.lastUpdate)}</div>
            </div>

            <div className="card">
              <div className="label">Drawdown</div>
              <div className="value">{fmtPct(data.currentDrawdownPct)}</div>
              <div className="hint">
                limit: {fmtPct(data.maxDrawdownPct)} | peak: {fmtNum(data.peakEquity)}
              </div>
            </div>
          </section>

          {data.liveTradingEnabled && (
            <div className="warning">
              LIVE TRADING ENABLED. Убедитесь, что вы понимаете риски, комиссии,
              precision filters и поведение MEXC API.
            </div>
          )}

          <section className="panel">
            <h2>Recent Candles</h2>
            <table>
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Open</th>
                  <th>High</th>
                  <th>Low</th>
                  <th>Close</th>
                  <th>Volume</th>
                </tr>
              </thead>
              <tbody>
                {recentCandles.length === 0 && (
                  <tr>
                    <td colSpan="6" className="empty">
                      Нет данных. Запустите бота или подождите первого поллинга.
                    </td>
                  </tr>
                )}
                {recentCandles.map((c) => (
                  <tr key={c.openTime}>
                    <td>{fmtTime(c.openTime)}</td>
                    <td>{fmtNum(c.open)}</td>
                    <td>{fmtNum(c.high)}</td>
                    <td>{fmtNum(c.low)}</td>
                    <td>{fmtNum(c.close)}</td>
                    <td>{fmtNum(c.volume, 6)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>

          <section className="panel">
            <h2>Orders</h2>
            <table>
              <thead>
                <tr>
                  <th>ID</th>
                  <th>Time</th>
                  <th>Side</th>
                  <th>Qty</th>
                  <th>Price</th>
                  <th>Status</th>
                  <th>Note</th>
                </tr>
              </thead>
              <tbody>
                {orders.length === 0 && (
                  <tr>
                    <td colSpan="7" className="empty">
                      Ордеров пока нет.
                    </td>
                  </tr>
                )}
                {orders.map((o) => (
                  <tr key={o.id}>
                    <td>{o.id}</td>
                    <td>{fmtTime(o.createdAt)}</td>
                    <td>{o.side}</td>
                    <td>{fmtNum(o.qty, 8)}</td>
                    <td>{fmtNum(o.price)}</td>
                    <td>{o.status}</td>
                    <td>{o.note}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>

          <section className="panel">
            <h2>Risk Events</h2>
            <table>
              <thead>
                <tr>
                  <th>ID</th>
                  <th>Time</th>
                  <th>Type</th>
                  <th>Message</th>
                </tr>
              </thead>
              <tbody>
                {riskEvents.length === 0 && (
                  <tr>
                    <td colSpan="4" className="empty">
                      Risk events отсутствуют.
                    </td>
                  </tr>
                )}
                {riskEvents.map((r) => (
                  <tr key={r.id}>
                    <td>{r.id}</td>
                    <td>{fmtTime(r.createdAt)}</td>
                    <td>{r.type}</td>
                    <td>{r.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>

          <section className="panel">
            <h2>Equity Snapshots</h2>
            <table>
              <thead>
                <tr>
                  <th>ID</th>
                  <th>Time</th>
                  <th>Equity</th>
                  <th>Cash</th>
                  <th>Position Value</th>
                </tr>
              </thead>
              <tbody>
                {snapshots.length === 0 && (
                  <tr>
                    <td colSpan="5" className="empty">
                      Снапшотов пока нет.
                    </td>
                  </tr>
                )}
                {snapshots.map((s) => (
                  <tr key={s.id}>
                    <td>{s.id}</td>
                    <td>{fmtTime(s.createdAt)}</td>
                    <td>{fmtNum(s.equity)}</td>
                    <td>{fmtNum(s.cash)}</td>
                    <td>{fmtNum(s.positionValue)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>
        </>
      )}
    </div>
  )
}