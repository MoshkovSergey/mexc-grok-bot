import { useEffect, useMemo, useRef, useState } from "react";
import { createChart, ColorType, CrosshairMode, LineStyle } from "lightweight-charts";

const UP = "#22c55e";
const DOWN = "#ef4444";
const WARN = "#f59e0b";
const GRID = "rgba(148,163,184,0.10)";
const TEXT = "#94a3b8";
const EMA_FAST = "#2dd4bf";
const EMA_SLOW = "#fb923c";
const TS1 = "#a78bfa";
const TS2 = "#38bdf8";
const SM_TOP = "#d35422";
const SM_BOT = "#00a5e6";
const SM_POC = "#f1c40f";
const SM_MID = "#94a3b8";

const CHART_PRICE_DECIMALS = 6;
const CHART_PRICE_MIN_MOVE = 1 / Math.pow(10, CHART_PRICE_DECIMALS);

function toUnixSec(value) {
  const ms = typeof value === "number" ? value : new Date(value).getTime();
  if (!Number.isFinite(ms)) return null;
  return Math.floor(ms / 1000);
}
function isNum(v) { return v != null && Number.isFinite(Number(v)); }

// Marker builder respects visibility.markers.* ; undefined => show (back-compat).
function buildMarkers(bars, orders, riskEvents, indicator, smartMoney, visibility) {
  if (!Array.isArray(bars) || bars.length === 0) return [];
  const times = bars.map((b) => b.time).filter((t) => t != null).sort((a, b) => a - b);
  if (times.length === 0) return [];

  const mk = (g) => visibility?.markers?.[g] !== false;

  const snap = (eventTime) => {
    if (eventTime == null || eventTime < times[0]) return null;
    let lo = 0, hi = times.length - 1, ans = null;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (times[mid] <= eventTime) { ans = times[mid]; lo = mid + 1; } else { hi = mid - 1; }
    }
    return ans;
  };

  const markers = [];

  if (mk("smaOrders")) {
    for (const o of orders ?? []) {
      const side = String(o.side || "").toUpperCase();
      if (side !== "BUY" && side !== "SELL") continue;
      const t = snap(toUnixSec(o.createdAt));
      if (t == null) continue;
      markers.push({ time: t, position: side === "BUY" ? "belowBar" : "aboveBar", color: side === "BUY" ? UP : DOWN, shape: side === "BUY" ? "arrowUp" : "arrowDown", text: side, size: 1 });
    }
  }

  if (mk("riskEvents")) {
    for (const r of riskEvents ?? []) {
      const type = String(r.type || "");
      const t = snap(toUnixSec(r.createdAt));
      if (t == null) continue;
      if (type === "max_drawdown") markers.push({ time: t, position: "aboveBar", color: DOWN, shape: "circle", text: "STOP", size: 1 });
      else if (type === "live_order_error") markers.push({ time: t, position: "aboveBar", color: WARN, shape: "square", text: "ERR", size: 1 });
    }
  }

  if (mk("cts")) {
    for (const ib of indicator ?? []) {
      const t = toUnixSec(ib.time);
      if (t == null) continue;
      if (ib.longSignal) { markers.push({ time: t, position: "belowBar", color: "#ffffff", shape: "arrowUp", text: "LONG", size: 2 }); continue; }
      if (ib.shortSignal) { markers.push({ time: t, position: "aboveBar", color: "#ffffff", shape: "arrowDown", text: "SHORT", size: 2 }); continue; }
      if (ib.cisdBull) { markers.push({ time: t, position: "belowBar", color: UP, shape: "labelUp", text: "CISD", size: 1 }); continue; }
      if (ib.cisdBear) { markers.push({ time: t, position: "aboveBar", color: DOWN, shape: "labelDown", text: "CISD", size: 1 }); continue; }
      if (ib.tsBull) { markers.push({ time: t, position: "belowBar", color: UP, shape: "arrowUp", text: "TS", size: 1 }); continue; }
      if (ib.tsBear) { markers.push({ time: t, position: "aboveBar", color: DOWN, shape: "arrowDown", text: "TS", size: 1 }); continue; }
      if (ib.bullSweep) { markers.push({ time: t, position: "belowBar", color: UP, shape: "diamond", text: "SWP", size: 1 }); continue; }
      if (ib.bearSweep) { markers.push({ time: t, position: "aboveBar", color: DOWN, shape: "diamond", text: "SWP", size: 1 }); }
    }
  }

  if (mk("smartMoney")) {
    for (const sb of smartMoney?.perBar ?? []) {
      const t = toUnixSec(sb.time);
      if (t == null) continue;
      if (sb.topSweep) { markers.push({ time: t, position: "aboveBar", color: SM_TOP, shape: "diamond", text: "LQ▲", size: 1 }); continue; }
      if (sb.bottomSweep) { markers.push({ time: t, position: "belowBar", color: SM_BOT, shape: "diamond", text: "LQ▼", size: 1 }); }
    }
  }

  markers.sort((a, b) => a.time - b.time);
  return markers;
}

function toLinePoints(rows, key) {
  if (!Array.isArray(rows)) return [];
  const out = [];
  for (const r of rows) {
    const t = toUnixSec(r.time);
    const v = r[key];
    if (t == null || v == null || Number.isNaN(Number(v))) continue;
    out.push({ time: t, value: Number(v) });
  }
  out.sort((a, b) => a.time - b.time);
  return out;
}

function lastFinite(rows, key) {
  if (!Array.isArray(rows)) return null;
  for (let i = rows.length - 1; i >= 0; i--) {
    const v = rows[i]?.[key];
    if (isNum(v) && !Number.isNaN(Number(v))) return Number(v);
  }
  return null;
}

export default function CandleChart({ candles, orders, riskEvents, indicator, smartMoney, visibility, symbol, interval, signalSource }) {
  const containerRef = useRef(null);
  const chartRef = useRef(null);
  const candleSeriesRef = useRef(null);
  const volumeSeriesRef = useRef(null);
  const emaFastRef = useRef(null);
  const emaSlowRef = useRef(null);
  const ts1Ref = useRef(null);
  const ts2Ref = useRef(null);
  const smLinesRef = useRef([]);
  const prevMetaRef = useRef({ symbol: "", interval: "" });
  const [autoFit, setAutoFit] = useState(false);

  const ln = (name) => visibility?.lines?.[name] !== false;

  const seriesData = useMemo(() => {
    if (!Array.isArray(candles)) return [];
    const seen = new Set(); const out = [];
    for (const c of candles) {
      const t = toUnixSec(c.openTime);
      if (t == null || seen.has(t)) continue;
      seen.add(t);
      out.push({ time: t, open: c.open, high: c.high, low: c.low, close: c.close });
    }
    out.sort((a, b) => a.time - b.time);
    return out;
  }, [candles]);

  const volumeData = useMemo(
    () => {
      if (!ln("volume") || !Array.isArray(candles)) return [];
      const seen = new Set(); const out = [];
      for (const c of candles) {
        const t = toUnixSec(c.openTime);
        if (t == null || seen.has(t)) continue;
        seen.add(t);
        out.push({ time: t, value: Number(c.volume) || 0, color: c.close >= c.open ? "rgba(34,197,94,0.35)" : "rgba(239,68,68,0.35)" });
      }
      out.sort((a, b) => a.time - b.time);
      return out;
    },
    [candles, visibility],
  );

  const markers = useMemo(
    () => buildMarkers(seriesData, orders, riskEvents, indicator, smartMoney, visibility),
    [seriesData, orders, riskEvents, indicator, smartMoney, visibility],
  );

  const emaFastPts = useMemo(() => (ln("ema") ? toLinePoints(indicator, "emaFast") : []), [indicator, visibility]);
  const emaSlowPts = useMemo(() => (ln("ema") ? toLinePoints(indicator, "emaSlow") : []), [indicator, visibility]);
  const ts1Pts = useMemo(() => (ln("ts") ? toLinePoints(indicator, "tsV1") : []), [indicator, visibility]);
  const ts2Pts = useMemo(() => (ln("ts") ? toLinePoints(indicator, "tsV2") : []), [indicator, visibility]);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return undefined;
    const chart = createChart(el, {
      width: el.clientWidth, height: 420,
      layout: { background: { type: ColorType.Solid, color: "transparent" }, textColor: TEXT, fontSize: 12, attributionLogo: false },
      grid: { vertLines: { color: GRID }, horzLines: { color: GRID } },
      crosshair: { mode: CrosshairMode.Normal },
      rightPriceScale: { borderColor: GRID },
      timeScale: { borderColor: GRID, timeVisible: true, secondsVisible: false },
    });
    const candleSeries = chart.addCandlestickSeries({
      upColor: UP, downColor: DOWN, borderUpColor: UP, borderDownColor: DOWN, wickUpColor: UP, wickDownColor: DOWN,
      priceFormat: { type: "price", precision: CHART_PRICE_DECIMALS, minMove: CHART_PRICE_MIN_MOVE },
    });
    const volumeSeries = chart.addHistogramSeries({ priceFormat: { type: "volume" }, priceScaleId: "volume" });
    chart.priceScale("volume").applyOptions({ scaleMargins: { top: 0.82, bottom: 0 } });
    const emaFast = chart.addLineSeries({ color: EMA_FAST, lineWidth: 2, priceLineVisible: false, lastValueVisible: false, title: "EMA Fast" });
    const emaSlow = chart.addLineSeries({ color: EMA_SLOW, lineWidth: 2, priceLineVisible: false, lastValueVisible: false, title: "EMA Slow" });
    const ts1 = chart.addLineSeries({ color: TS1, lineWidth: 1, priceLineVisible: false, lastValueVisible: false, title: "TS V1" });
    const ts2 = chart.addLineSeries({ color: TS2, lineWidth: 1, priceLineVisible: false, lastValueVisible: false, title: "TS V2" });

    chartRef.current = chart; candleSeriesRef.current = candleSeries; volumeSeriesRef.current = volumeSeries;
    emaFastRef.current = emaFast; emaSlowRef.current = emaSlow; ts1Ref.current = ts1; ts2Ref.current = ts2;

    const ro = new ResizeObserver(() => { if (containerRef.current) chart.applyOptions({ width: containerRef.current.clientWidth }); });
    ro.observe(el);
    return () => {
      ro.disconnect(); chart.remove();
      chartRef.current = null; candleSeriesRef.current = null; volumeSeriesRef.current = null;
      emaFastRef.current = null; emaSlowRef.current = null; ts1Ref.current = null; ts2Ref.current = null;
      smLinesRef.current = [];
    };
  }, []);

  // Viewport hold via LOGICAL range: no right-edge drift on new bars.
  useEffect(() => {
    const chart = chartRef.current; const cs = candleSeriesRef.current; const vs = volumeSeriesRef.current;
    if (!chart || !cs || !vs) return;
    const ts = chart.timeScale();
    const prevMeta = prevMetaRef.current;
    const metaChanged = prevMeta.symbol !== symbol || prevMeta.interval !== interval;
    const isFirst = prevMeta.symbol === "";
    prevMetaRef.current = { symbol, interval };
    const holdView = !autoFit && !metaChanged && !isFirst && seriesData.length > 0;
    const prevLogical = holdView ? ts.getVisibleLogicalRange() : null;
    cs.setData(seriesData); vs.setData(volumeData);
    if (autoFit || metaChanged || isFirst || !prevLogical) ts.fitContent();
    else ts.setVisibleLogicalRange(prevLogical);
  }, [seriesData, volumeData, autoFit, symbol, interval]);

  useEffect(() => { if (emaFastRef.current) emaFastRef.current.setData(emaFastPts); }, [emaFastPts]);
  useEffect(() => { if (emaSlowRef.current) emaSlowRef.current.setData(emaSlowPts); }, [emaSlowPts]);
  useEffect(() => { if (ts1Ref.current) ts1Ref.current.setData(ts1Pts); }, [ts1Pts]);
  useEffect(() => { if (ts2Ref.current) ts2Ref.current.setData(ts2Pts); }, [ts2Pts]);
  useEffect(() => { if (!candleSeriesRef.current) return; candleSeriesRef.current.setMarkers(markers); }, [markers]);

  // Smart Money horizontal level lines, gated by visibility.lines.smartMoney.
  useEffect(() => {
    const cs = candleSeriesRef.current;
    if (!cs) return;
    for (const line of smLinesRef.current) { try { cs.removePriceLine(line); } catch { /* noop */ } }
    smLinesRef.current = [];
    if (!ln("smartMoney")) return;
    const perBar = smartMoney?.perBar;
    const prof = smartMoney?.profile;
    const add = (price, color, style, title) => {
      if (!isNum(price)) return;
      smLinesRef.current.push(cs.createPriceLine({ price: Number(price), color, lineWidth: 2, lineStyle: style, axisLabelVisible: true, title }));
    };
    add(lastFinite(perBar, "liqTop"), SM_TOP, LineStyle.Solid, "Liq Top");
    add(lastFinite(perBar, "liqBottom"), SM_BOT, LineStyle.Solid, "Liq Bot");
    if (prof?.valid) {
      add(prof.pocPrice, SM_POC, LineStyle.Solid, "POC");
      add(prof.midPrice, SM_MID, LineStyle.Dashed, "Mid");
    }
  }, [smartMoney, visibility]);

  const showCtsLines = signalSource === "cts";
  const prof = smartMoney?.profile;
  const fmt6 = (v) => (isNum(v) ? Number(v).toFixed(CHART_PRICE_DECIMALS) : "—");

  const btnBase = { position: "absolute", top: 8, right: 8, zIndex: 5, width: 26, height: 26, lineHeight: "24px", textAlign: "center", borderRadius: 8, border: "1px solid rgba(148,163,184,0.35)", background: autoFit ? "#60a5fa" : "rgba(12,18,32,0.85)", color: autoFit ? "#071018" : "#94a3b8", fontWeight: 800, fontSize: 13, cursor: "pointer", userSelect: "none", padding: 0 };

  return (
    <div className="chart-block">
      <div className="chart-head">
        <h2>Price &amp; Signals</h2>
        <div className="chart-meta">
          <span className="chip">{symbol}</span>
          <span className="chip chip-dim">{interval}</span>
          <span className="chip chip-dim">src: {signalSource || "sma"}</span>
          <span className="legend">
            <i className="dot dot-up" /> BUY/LONG
            <i className="dot dot-down" /> SELL/SHORT
            <i className="dot dot-warn" /> RISK
          </span>
        </div>
      </div>
      <div ref={containerRef} className="chart-wrap">
        <button type="button" onClick={() => setAutoFit((v) => !v)} title={autoFit ? "Auto-fit ON — click to hold your view" : "Auto-fit OFF — click to fill window"} aria-pressed={autoFit} style={btnBase}>A</button>
      </div>
      {showCtsLines && (ln("ema") || ln("ts")) && (
        <div className="chart-legend-lines">
          {ln("ema") && (<><span><i style={{ background: EMA_FAST }} /> EMA Fast</span><span><i style={{ background: EMA_SLOW }} /> EMA Slow</span></>)}
          {ln("ts") && (<><span><i style={{ background: TS1 }} /> TS V1</span><span><i style={{ background: TS2 }} /> TS V2</span></>)}
        </div>
      )}
      {ln("smartMoney") && (
        <div className="chart-legend-lines">
          <span><i style={{ background: SM_TOP }} /> Liq Top {fmt6(lastFinite(smartMoney?.perBar, "liqTop"))}</span>
          <span><i style={{ background: SM_BOT }} /> Liq Bot {fmt6(lastFinite(smartMoney?.perBar, "liqBottom"))}</span>
          <span><i style={{ background: SM_POC }} /> POC {prof?.valid ? fmt6(prof.pocPrice) : "—"}</span>
          <span><i style={{ background: SM_MID }} /> Mid {prof?.valid ? fmt6(prof.midPrice) : "—"}</span>
        </div>
      )}
      {seriesData.length === 0 && (
        <div className="chart-empty">Нет свечей для отображения. Дождитесь первого поллинга или смените пару/таймфрейм в Settings.</div>
      )}
    </div>
  );
}