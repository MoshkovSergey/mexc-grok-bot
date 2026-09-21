# MEXC Grok-like Bot

Учебный/исследовательский trading bot scaffold для MEXC Spot API.

## Важно

- Проект не гарантирует прибыль.
- По умолчанию работает в **paper mode**.
- Live trading включается только через `ENABLE_LIVE_TRADING=true`.
- Перед live-торговлей проверьте:
  - официальные требования MEXC к точности количества/цены;
  - лимиты API;
  - комиссии;
  - риски частичных исполнений;
  - юридические/налоговые аспекты в вашей юрисдикции.

## Архитектура

- **backend**: Go HTTP API, бот-движок, MEXC REST client, PostgreSQL persistence.
- **frontend**: React dashboard для наблюдения за equity, позицией, сигналами, рисками.
- **db**: PostgreSQL для свечей, состояния бота, ордеров, снапшотов equity, risk events.
- **docker**: локальный запуск всех сервисов.

## Запуск через Docker

```bash
cp .env.example .env
# отредактируйте .env
docker compose up --build
