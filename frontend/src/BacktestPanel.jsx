import { useCallback, useState } from "react";
import { getJSON } from "./api.js";

// Raw-value-aware formatters. The backend sends null for undefined metrics (NaN) and
// the strings "inf"/"-inf" for infinite profit factors, so we MUST inspect the raw
// value before Number() (Number(null)===0 would otherwise render "0.00" and lie).
function n(v, d = 2) {
  if (v === null || v === undefined) return "—";
  if (v === "inf") return "∞";
  if (v === "-inf") return "-∞";
  const x = Number(v);
  if (!Number.isFinite(x) || Number.isNaN(x)) return "—";
  return x.toFixed(d);
}
function pct(v) {
  if (v === null || v === undefined) return "—";
  if (v === "inf") return "∞";
  if (v === "-inf") return "-∞";
  const x = Number(v);
  if (!Number.isFinite(x) || Number.isNaN(x)) return "—";
  return `${(x * 100).toFixed(2)}%`;
}
function pf(v) {
  if (v === null || v === undefined) return "—";
  if (v === "inf") return "∞";
  if (v === "-inf") return "-∞";
  const x = Number(v);
  if (!Number.isFinite(x) || Number.isNaN(x)) return "—";
  return x.toFixed(2);
}

const ROWS = [
  ["Сделки", (m) => m.numTrades],
  ["Доля прибыльных", (m) => pct(m.winRate)],
  ["Профит‑фактор", (m) => pf(m.profitFactor)],
  ["Мат. ожидание (нетто)", (m) => n(m.expectancyNet, 4)],
  ["Медианный нетто P&L", (m) => n(m.medianNetPnl, 4)],
  ["Совокупная доходность", (m) => pct(m.totalReturnPct)],
  ["Макс. просадка", (m) => pct(m.maxDrawdownPct)],
  ["Нагрузка издержек", (m) => pct(m.costDragPct)],
  ["Комиссии (сумма)", (m) => n(m.totalFees, 2)],
  ["Стоимость слиппеджа", (m) => n(m.totalSlippage, 2)],
  ["Sharpe (за бар)", (m) => n(m.sharpePerBar, 3)],
  ["Sortino (за бар)", (m) => n(m.sortinoPerBar, 3)],
];

export default function BacktestPanel({ symbol, interval }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [res, setRes] = useState(null);
  const [takerBps, setTakerBps] = useState("");
  const [slipBps, setSlipBps] = useState("");
  const [smaGrid, setSmaGrid] = useState(true);

  const run = useCallback(async () => {
    setBusy(true);
    setErr("");
    try {
      const p = new URLSearchParams({
        symbol,
        interval,
        source: "both",
        smaGrid: smaGrid ? "1" : "0",
      });
      if (Number(takerBps) > 0) p.set("takerBps", String(takerBps));
      if (Number(slipBps) > 0) p.set("slippageBps", String(slipBps));
      setRes(await getJSON(`/api/backtest?${p.toString()}`));
    } catch (e) {
      setErr(e.message || String(e));
    } finally {
      setBusy(false);
    }
  }, [symbol, interval, takerBps, slipBps, smaGrid]);

  const sma = res?.sma;
  const cts = res?.cts;

  return (
    <section className="panel">
      <div className="bt-head">
        <h2>Бэктест (офлайн, с учётом издержек)</h2>
        <div className="bt-controls">
          <label className="bt-field">
            <span>тейкер, б.п.</span>
            <input
              type="number"
              min="0"
              step="0.1"
              value={takerBps}
              placeholder="0 = оптимизм"
              onChange={(e) => setTakerBps(e.target.value)}
            />
          </label>
          <label className="bt-field">
            <span>слиппедж, б.п.</span>
            <input
              type="number"
              min="0"
              step="0.1"
              value={slipBps}
              placeholder="0 = оптимизм"
              onChange={(e) => setSlipBps(e.target.value)}
            />
          </label>
          <label className="bt-check">
            <input
              type="checkbox"
              checked={smaGrid}
              onChange={(e) => setSmaGrid(e.target.checked)}
            />
            <span>Сетка чувствительности SMA</span>
          </label>
          <button onClick={run} disabled={busy}>
            {busy ? "Расчёт…" : "Запустить сравнение"}
          </button>
        </div>
      </div>

      <p className="bt-note">
        Исполнение на открытии следующего бара, фиксированная доля позиции и тот
        же kill‑switch по просадке, что в живом движке. Комиссии/слиппедж —
        явный ввод; 0 = оптимистичный прогон (флаг ниже). Тяжёлая CTS‑сетка
        параметров считается только в CLI (см. README), чтобы не словить
        HTTP‑таймаут. В таблице: «—» = метрика не определена (нет
        сделок/дисперсии), «∞» = безубыточная серия (профит‑фактор бесконечен).
      </p>

      {err && <div className="error">{err}</div>}

      {res && (
        <>
          {res.assumptions?.length > 0 && (
            <div className="warning">
              <strong>Assumptions / warnings</strong>
              <ul>
                {res.assumptions.map((a, i) => (
                  <li key={i}>{a}</li>
                ))}
              </ul>
            </div>
          )}

          <table className="bt-table">
            <thead>
              <tr>
                <th>Метрика</th>
                <th>SMA</th>
                <th>CTS‑CISD</th>
              </tr>
            </thead>
            <tbody>
              {ROWS.map(([label, get]) => (
                <tr key={label}>
                  <td>{label}</td>
                  <td>{sma ? get(sma.metrics) : "—"}</td>
                  <td>{cts ? get(cts.metrics) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>

          {sma && sma.riskStopped && (
            <div className="hint">SMA: kill‑switch сработал внутри окна.</div>
          )}
          {cts && cts.riskStopped && (
            <div className="hint">CTS: kill‑switch сработал внутри окна.</div>
          )}
          {cts && cts.openAtEnd && (
            <div className="hint">
              CTS: позиция была открыта на конец окна (в equity‑кривой, не в
              статистике сделок).
            </div>
          )}

          {res.smaSensitivity && res.smaSensitivity.length > 0 && (
            <div className="bt-sens">
              <h3>Сетка чувствительности SMA</h3>
              <div className="bt-sens-summary">
                medianNet={n(res.smaSensSummary.medianNet)}% · cvNet=
                {n(res.smaSensSummary.cvNet, 2)} · fracPF&gt;1=
                {pct(res.smaSensSummary.fracPFgt1)} · spike=
                {String(res.smaSensSummary.spike)}{" "}
                <em>
                  (низкий fracPF&gt;1 / высокий cvNet / spike=true → edge
                  неустойчив = переобучение)
                </em>
              </div>
              <table className="bt-table bt-table-sens">
                <thead>
                  <tr>
                    <th>параметры</th>
                    <th>нетто, %</th>
                    <th>ПФ</th>
                    <th>сделки</th>
                    <th>макс. просадка, %</th>
                  </tr>
                </thead>
                <tbody>
                  {res.smaSensitivity.map((r, i) => (
                    <tr key={i}>
                      <td>{r.label}</td>
                      <td>{n(r.netReturnPct)}</td>
                      <td>{pf(r.profitFactor)}</td>
                      <td>{r.numTrades}</td>
                      <td>{n(r.maxDrawdownPct)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}

      {!res && !err && (
        <div className="hint">
          Задайте комиссии/слиппедж (после сверки с MEXC fee schedule и
          VIP‑уровнем) и нажмите Run comparison.
        </div>
      )}
    </section>
  );
}
