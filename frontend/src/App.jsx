import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON, putJSON } from "./api.js";
import CandleChart from "./CandleChart.jsx";
import BacktestPanel from "./BacktestPanel.jsx";

const PRICE_DECIMALS = 6;

// ---- Client-side chart visibility (localStorage). NOT a trading setting:
// hiding a label must never change order execution, sizing or risk. Kept out of
// bot_settings / PUT /api/settings on purpose (separation of concerns).
const VIS_STORAGE_KEY = "mexc-chart-visibility";
const DEFAULT_VISIBILITY = {
  markers: { smaOrders: true, riskEvents: true, cts: true, smartMoney: true },
  lines: { ema: true, ts: true, smartMoney: true, volume: true },
};

function mergeVisibility(saved) {
  const out = { markers: {}, lines: {} };
  for (const k of Object.keys(DEFAULT_VISIBILITY.markers)) {
    out.markers[k] = saved?.markers?.[k] ?? DEFAULT_VISIBILITY.markers[k];
  }
  for (const k of Object.keys(DEFAULT_VISIBILITY.lines)) {
    out.lines[k] = saved?.lines?.[k] ?? DEFAULT_VISIBILITY.lines[k];
  }
  return out;
}

function loadVisibility() {
  try {
    const raw = localStorage.getItem(VIS_STORAGE_KEY);
    if (!raw) return mergeVisibility(null);
    return mergeVisibility(JSON.parse(raw));
  } catch {
    return mergeVisibility(null);
  }
}

function saveVisibility(v) {
  try {
    localStorage.setItem(VIS_STORAGE_KEY, JSON.stringify(v));
  } catch {
    /* storage may be unavailable (private mode); ignore, in-memory state still works */
  }
}

function fmtNum(value, digits = 2) {
  const n = Number(value);
  if (!Number.isFinite(n)) return (0).toFixed(digits);
  return n.toFixed(digits);
}
function fmtPrice(value, digits = PRICE_DECIMALS) {
  return fmtNum(value, digits);
}
function fmtPct(value) {
  const n = Number(value);
  if (!Number.isFinite(n)) return "0.00%";
  return `${(n * 100).toFixed(2)}%`;
}
function fmtTime(value) {
  if (!value) return "-";
  try {
    return new Date(value).toLocaleString();
  } catch {
    return String(value);
  }
}
function sourceLabel(src) {
  switch (src) {
    case "cts":
      return "cts (local CTS-CISD port)";
    case "sma":
      return "sma (built-in crossover)";
    default:
      return src || "sma";
  }
}

const EMPTY_FORM = {
  symbol: "",
  interval: "1m",
  signalSource: "sma",
  fastPeriod: 9,
  slowPeriod: 21,
  pollSeconds: 15,
  maxPositionPct: 0.2,
  maxDrawdownPct: 0.05,
  paperEquity: 10000,
  liveOrderValueLimit: 50,
};
function settingsToForm(s) {
  return {
    symbol: s.symbol ?? "",
    interval: s.interval ?? "1m",
    signalSource: String(s.signalSource ?? "sma").toLowerCase(),
    fastPeriod: Number(s.fastPeriod ?? 9),
    slowPeriod: Number(s.slowPeriod ?? 21),
    pollSeconds: Number(s.pollSeconds ?? 15),
    maxPositionPct: Number(s.maxPositionPct ?? 0.2),
    maxDrawdownPct: Number(s.maxDrawdownPct ?? 0.05),
    paperEquity: Number(s.paperEquity ?? 10000),
    liveOrderValueLimit: Number(s.liveOrderValueLimit ?? 50),
  };
}

export default function App() {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [showSettings, setShowSettings] = useState(false);
  const [form, setForm] = useState(EMPTY_FORM);
  const [validIntervals, setValidIntervals] = useState(["1m"]);
  const [validSignalSources, setValidSignalSources] = useState(["sma", "cts"]);
  const [liveEnabled, setLiveEnabled] = useState(false);
  const [settingsLoadedAt, setSettingsLoadedAt] = useState("");
  const [savingSettings, setSavingSettings] = useState(false);
  const [settingsError, setSettingsError] = useState("");
  const [settingsOk, setSettingsOk] = useState("");

  // Visual-only state (localStorage), independent of the trading settings form.
  const [visibility, setVisibility] = useState(() => loadVisibility());

  const toggleMarker = useCallback((key) => {
    setVisibility((v) => {
      const next = { ...v, markers: { ...v.markers, [key]: !v.markers[key] } };
      saveVisibility(next);
      return next;
    });
  }, []);
  const toggleLine = useCallback((key) => {
    setVisibility((v) => {
      const next = { ...v, lines: { ...v.lines, [key]: !v.lines[key] } };
      saveVisibility(next);
      return next;
    });
  }, []);

  const load = useCallback(async () => {
    try {
      setError("");
      setData(await getJSON("/api/dashboard"));
    } catch (e) {
      setError(e.message || String(e));
    }
  }, []);
  useEffect(() => {
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  }, [load]);

  const openSettings = useCallback(async () => {
    setSettingsError("");
    setSettingsOk("");
    setShowSettings(true);
    try {
      const res = await getJSON("/api/settings");
      setForm(settingsToForm(res.settings));
      setValidIntervals(
        Array.isArray(res.validIntervals) && res.validIntervals.length
          ? res.validIntervals
          : ["1m"],
      );
      setValidSignalSources(
        Array.isArray(res.validSignalSources) && res.validSignalSources.length
          ? res.validSignalSources
          : ["sma", "cts"],
      );
      setLiveEnabled(Boolean(res.liveTradingEnabled));
      setSettingsLoadedAt(
        res.settings?.updatedAt ? fmtTime(res.settings.updatedAt) : "-",
      );
    } catch (e) {
      setSettingsError(e.message || String(e));
    }
  }, []);

  const start = async () => {
    setLoading(true);
    try {
      await postJSON("/api/bot/start");
      await load();
    } catch (e) {
      setError(e.message || String(e));
    } finally {
      setLoading(false);
    }
  };
  const stop = async () => {
    setLoading(true);
    try {
      await postJSON("/api/bot/stop");
      await load();
    } catch (e) {
      setError(e.message || String(e));
    } finally {
      setLoading(false);
    }
  };

  const saveSettings = async () => {
    setSavingSettings(true);
    setSettingsError("");
    setSettingsOk("");
    try {
      // NOTE: visibility is intentionally NOT part of this payload (client-only).
      const payload = {
        symbol: String(form.symbol).toUpperCase().trim(),
        interval: form.interval,
        signalSource: String(form.signalSource || "sma")
          .toLowerCase()
          .trim(),
        fastPeriod: Number(form.fastPeriod),
        slowPeriod: Number(form.slowPeriod),
        pollSeconds: Number(form.pollSeconds),
        maxPositionPct: Number(form.maxPositionPct),
        maxDrawdownPct: Number(form.maxDrawdownPct),
        paperEquity: Number(form.paperEquity),
        liveOrderValueLimit: Number(form.liveOrderValueLimit),
      };
      const res = await putJSON("/api/settings", payload);
      setForm(settingsToForm(res.settings));
      setValidIntervals(res.validIntervals);
      setValidSignalSources(
        Array.isArray(res.validSignalSources) && res.validSignalSources.length
          ? res.validSignalSources
          : ["sma", "cts"],
      );
      setLiveEnabled(Boolean(res.liveTradingEnabled));
      setSettingsLoadedAt(
        res.settings?.updatedAt ? fmtTime(res.settings.updatedAt) : "-",
      );
      setSettingsOk(
        "Торговые настройки сохранены. Видимость графиков — локально, применяется сразу.",
      );
      await load();
    } catch (e) {
      setSettingsError(e.message || String(e));
    } finally {
      setSavingSettings(false);
    }
  };

  const setField = (key, value) => setForm((f) => ({ ...f, [key]: value }));

  const candles = Array.isArray(data?.candles) ? data.candles : [];
  const orders = Array.isArray(data?.orders) ? data.orders : [];
  const snapshots = Array.isArray(data?.snapshots) ? data.snapshots : [];
  const riskEvents = Array.isArray(data?.riskEvents) ? data.riskEvents : [];
  const indicator = Array.isArray(data?.indicator) ? data.indicator : [];
  const recentCandles = [...candles].slice(-20).reverse();

  const markerDefs = [
    ["smaOrders", "SMA BUY / SELL"],
    ["riskEvents", "Risk STOP / ERR"],
    ["cts", "CTS TS / CISD / SWP / LONG / SHORT"],
    ["smartMoney", "Smart Money LQ ▲ / ▼"],
  ];
  const lineDefs = [
    ["ema", "EMA fast / slow"],
    ["ts", "TS V1 / V2"],
    ["smartMoney", "SM levels (Liq / POC / Mid)"],
    ["volume", "Volume histogram"],
  ];

  return (
    <div className="app">
      <header className="header">
        <div>
          <h1>MEXC Grok-like Bot</h1>
          <p className="subtitle">
            Research / paper-trading scaffold. Не является финансовой
            рекомендацией.
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
          <button className="settings" onClick={openSettings}>
            Settings
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
                cash: {fmtNum(data.cash)} | pos value:{" "}
                {fmtNum(data.positionValue)}
              </div>
            </div>
            <div className="card">
              <div className="label">Position</div>
              <div className="value">{fmtNum(data.positionQty, 8)}</div>
              <div className="hint">entry: {fmtPrice(data.entryPrice)}</div>
            </div>
            <div className="card">
              <div className="label">Last Signal</div>
              <div className="value">{data.lastSignal || "none"}</div>
              <div className="hint">
                source: {data.signalSource || "sma"} | updated:{" "}
                {fmtTime(data.lastUpdate)}
              </div>
            </div>
            <div className="card">
              <div className="label">Drawdown</div>
              <div className="value">{fmtPct(data.currentDrawdownPct)}</div>
              <div className="hint">
                limit: {fmtPct(data.maxDrawdownPct)} | peak:{" "}
                {fmtNum(data.peakEquity)}
              </div>
            </div>
          </section>

          {data.liveTradingEnabled && (
            <div className="warning">
              LIVE TRADING ENABLED. Убедитесь, что вы понимаете риски, комиссии,
              precision filters и поведение MEXC API.
            </div>
          )}

          <section className="panel panel-chart">
            <CandleChart
              candles={candles}
              orders={orders}
              riskEvents={riskEvents}
              indicator={indicator}
              smartMoney={data?.smartMoney ?? null}
              visibility={visibility}
              signalSource={data?.signalSource}
              symbol={data.symbol}
              interval={data.interval}
            />
          </section>
          <BacktestPanel symbol={data.symbol} interval={data.interval} />
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
                    <td>{fmtPrice(c.open)}</td>
                    <td>{fmtPrice(c.high)}</td>
                    <td>{fmtPrice(c.low)}</td>
                    <td>{fmtPrice(c.close)}</td>
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
                    <td>{fmtPrice(o.price)}</td>
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

      {showSettings && (
        <div className="modal-backdrop" onClick={() => setShowSettings(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <h2>Settings</h2>
              <button
                className="icon-btn"
                onClick={() => setShowSettings(false)}
                aria-label="Close"
              >
                ×
              </button>
            </div>
            <p className="modal-note">
              Торговые параметры (ниже) применяются без рестарта через
              PostgreSQL и сохраняются кнопкой Save. Смена пары в paper-режиме
              автоматически закрывает позицию; в live-режиме смена пары при
              позиции запрещена. Live включается только переменной
              ENABLE_LIVE_TRADING. Signal source “cts” — локальный порт CTS-CISD
              (без TradingView), требует прогрева ~200–300 закрытых баров. Smart
              Money / POC — визуальный слой (порт CC BY-NC-SA 4.0, BigBeluga),
              сигналы НЕ торгует.
            </p>

            <div className="badge-row">
              <span
                className={
                  liveEnabled ? "badge badge-live" : "badge badge-paper"
                }
              >
                {liveEnabled ? "LIVE" : "PAPER"}
              </span>
              <span className="badge-muted">saved: {settingsLoadedAt}</span>
            </div>

            {settingsError && <div className="error">{settingsError}</div>}
            {settingsOk && <div className="success">{settingsOk}</div>}

            <div className="form-grid">
              <label className="field">
                <span>Symbol</span>
                <input
                  value={form.symbol}
                  onChange={(e) =>
                    setField("symbol", e.target.value.toUpperCase())
                  }
                  placeholder="BTCUSDT"
                />
              </label>
              <label className="field">
                <span>Interval</span>
                <select
                  value={form.interval}
                  onChange={(e) => setField("interval", e.target.value)}
                >
                  {validIntervals.map((iv) => (
                    <option key={iv} value={iv}>
                      {iv}
                    </option>
                  ))}
                </select>
              </label>
              <label className="field">
                <span>Signal source</span>
                <select
                  value={form.signalSource}
                  onChange={(e) => setField("signalSource", e.target.value)}
                >
                  {(Array.isArray(validSignalSources) &&
                  validSignalSources.length
                    ? validSignalSources
                    : ["sma", "cts"]
                  ).map((src) => (
                    <option key={src} value={src}>
                      {sourceLabel(src)}
                    </option>
                  ))}
                </select>
              </label>
              <label className="field">
                <span>Fast period</span>
                <input
                  type="number"
                  min="1"
                  value={form.fastPeriod}
                  onChange={(e) => setField("fastPeriod", e.target.value)}
                />
              </label>
              <label className="field">
                <span>Slow period</span>
                <input
                  type="number"
                  min="2"
                  value={form.slowPeriod}
                  onChange={(e) => setField("slowPeriod", e.target.value)}
                />
              </label>
              <label className="field">
                <span>Poll seconds</span>
                <input
                  type="number"
                  min="5"
                  value={form.pollSeconds}
                  onChange={(e) => setField("pollSeconds", e.target.value)}
                />
              </label>
              <label className="field">
                <span>Max position (0..1)</span>
                <input
                  type="number"
                  min="0.01"
                  max="1"
                  step="0.01"
                  value={form.maxPositionPct}
                  onChange={(e) => setField("maxPositionPct", e.target.value)}
                />
              </label>
              <label className="field">
                <span>Max drawdown (0..1)</span>
                <input
                  type="number"
                  min="0.01"
                  max="1"
                  step="0.01"
                  value={form.maxDrawdownPct}
                  onChange={(e) => setField("maxDrawdownPct", e.target.value)}
                />
              </label>
              <label className="field">
                <span>Paper equity (start)</span>
                <input
                  type="number"
                  min="1"
                  value={form.paperEquity}
                  onChange={(e) => setField("paperEquity", e.target.value)}
                />
              </label>
              <label className="field">
                <span>Live order limit</span>
                <input
                  type="number"
                  min="1"
                  value={form.liveOrderValueLimit}
                  onChange={(e) =>
                    setField("liveOrderValueLimit", e.target.value)
                  }
                />
              </label>
            </div>

            {/* Visual-only section: applies instantly, stored in localStorage, NOT sent to API. */}
            <div className="vis-section">
              <div className="vis-title">Chart labels &amp; overlays</div>
              <div className="vis-sub">
                Локально для этого браузера. Не влияет на торговлю/риск.
                Применяется сразу, без Save.
              </div>

              <div className="vis-group">
                <div className="vis-group-label">Markers</div>
                {markerDefs.map(([key, label]) => (
                  <label key={key} className="vis-check">
                    <input
                      type="checkbox"
                      checked={visibility.markers[key]}
                      onChange={() => toggleMarker(key)}
                    />
                    <span>{label}</span>
                  </label>
                ))}
              </div>

              <div className="vis-group">
                <div className="vis-group-label">Lines / overlays</div>
                {lineDefs.map(([key, label]) => (
                  <label key={key} className="vis-check">
                    <input
                      type="checkbox"
                      checked={visibility.lines[key]}
                      onChange={() => toggleLine(key)}
                    />
                    <span>{label}</span>
                  </label>
                ))}
              </div>
            </div>

            <div className="modal-actions">
              <button
                className="ghost"
                onClick={() => setShowSettings(false)}
                disabled={savingSettings}
              >
                Close
              </button>
              <button onClick={saveSettings} disabled={savingSettings}>
                {savingSettings ? "Saving…" : "Save trading settings"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
