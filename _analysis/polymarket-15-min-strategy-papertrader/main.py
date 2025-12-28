#!/usr/bin/env python3
from __future__ import annotations

import argparse
import csv
import json
import os
import sys
import time
import threading
from dataclasses import dataclass, asdict
from datetime import datetime, timezone, timedelta
from typing import Dict, List, Optional, Tuple

import requests

try:
    from websocket import WebSocketApp  # type: ignore
    WS_AVAILABLE = True
except Exception:
    WS_AVAILABLE = False

# optional google drive deps (only used if env vars are set)
# pip install google-api-python-client google-auth google-auth-httplib2
try:
    from google.oauth2.credentials import Credentials  # type: ignore
    from google.auth.transport.requests import Request  # type: ignore
    from googleapiclient.discovery import build  # type: ignore
    from googleapiclient.http import MediaFileUpload  # type: ignore
    GDRIVE_LIBS_AVAILABLE = True
except Exception:
    GDRIVE_LIBS_AVAILABLE = False


GAMMA_API = "https://gamma-api.polymarket.com"
CLOB_HOST = "https://clob.polymarket.com"


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


def parse_iso_dt(s: Optional[str]) -> Optional[datetime]:
    if not s:
        return None
    try:
        if s.endswith("Z"):
            return datetime.fromisoformat(s.replace("Z", "+00:00"))
        return datetime.fromisoformat(s)
    except Exception:
        return None


def safe_float(x, default=None):
    try:
        return float(x)
    except Exception:
        return default


def clamp(n: float, lo: float, hi: float) -> float:
    return max(lo, min(hi, n))


def normalize_json_env(s: str) -> str:
    return s.strip()


def parse_clob_token_ids(v) -> List[str]:
    if v is None:
        return []
    if isinstance(v, list):
        return [str(x) for x in v]
    s = str(v).strip()
    if not s:
        return []
    if s.startswith("[") and s.endswith("]"):
        try:
            arr = json.loads(s)
            if isinstance(arr, list):
                return [str(x) for x in arr]
        except Exception:
            pass
    if "," in s:
        return [p.strip().strip('"').strip("'") for p in s.split(",") if p.strip()]
    return [s.strip().strip('"').strip("'")]


@dataclass
class StrategyConfig:
    symbols: Tuple[str, ...] = ("BTC", "ETH", "SOL", "XRP")
    slug_prefix_map: Dict[str, str] = None  # set in __post_init__
    discovery_refresh_sec: float = 10.0
    lookback_limit: int = 250

    # requested defaults
    buy_chunk_usd: float = 1.0
    max_side_spend_usd: float = 4.0
    require_free_cash: float = 0.05

    entry_ask_cents: float = 35.0
    dca_step_cents: float = 5.0
    dca_levels: int = 3
    sum_cap_dollars: float = 0.99

    expensive_min: float = 0.73
    cheap_max: float = 0.22
    spread_sum_cap: float = 1.00

    allow_unhedged_breakeven_exit: bool = False
    allow_hedged_breakeven_exit: bool = False

    settle_grace_sec: float = 5.0
    settle_signal_bid: float = 0.99
    settle_max_wait_sec: float = 180.0

    prefer_websocket: bool = True
    rest_poll_interval_sec: float = 0.7

    state_path: str = "paper_state.json"
    trades_csv: str = "paper_trades.csv"
    results_csv: str = "paper_market_results.csv"

    def __post_init__(self):
        if self.slug_prefix_map is None:
            self.slug_prefix_map = {
                "BTC": "btc-updown-15m-",
                "ETH": "eth-updown-15m-",
                "SOL": "sol-updown-15m-",
                "XRP": "xrp-updown-15m-",
            }


@dataclass
class Fill:
    ts: str
    slug: str
    symbol: str
    token_id: str
    side: str
    price: float
    shares: float
    usd: float
    note: str


@dataclass
class MarketResult:
    ts: str
    slug: str
    symbol: str
    end_time_utc: str
    winner: str
    up_token: str
    down_token: str
    up_shares: float
    down_shares: float
    up_cost: float
    down_cost: float
    pnl: float
    balance_after: float


@dataclass
class Position:
    shares: float = 0.0
    cost_usd: float = 0.0

    @property
    def avg_price(self) -> float:
        if self.shares <= 0:
            return 0.0
        return self.cost_usd / self.shares


class OrderBook:
    def __init__(self):
        self.bids: List[Tuple[float, float]] = []
        self.asks: List[Tuple[float, float]] = []
        self.last_update_ts: float = 0.0

    def best_bid(self) -> Optional[float]:
        return self.bids[0][0] if self.bids else None

    def best_ask(self) -> Optional[float]:
        return self.asks[0][0] if self.asks else None


class OrderBookStore:
    def __init__(self):
        self._lock = threading.Lock()
        self._books: Dict[str, OrderBook] = {}

    def update_from_snapshot(self, token_id: str, bids: list, asks: list):
        ob = OrderBook()

        bb = []
        for lvl in bids or []:
            p = safe_float(lvl.get("price"))
            s = safe_float(lvl.get("size"))
            if p is None or s is None:
                continue
            bb.append((p, s))
        bb.sort(key=lambda x: x[0], reverse=True)

        aa = []
        for lvl in asks or []:
            p = safe_float(lvl.get("price"))
            s = safe_float(lvl.get("size"))
            if p is None or s is None:
                continue
            aa.append((p, s))
        aa.sort(key=lambda x: x[0])

        ob.bids = bb
        ob.asks = aa
        ob.last_update_ts = time.time()

        with self._lock:
            self._books[token_id] = ob

    def get(self, token_id: str) -> OrderBook:
        with self._lock:
            return self._books.get(token_id, OrderBook())


class MarketFeed:
    def __init__(self, token_ids: List[str], store: OrderBookStore, cfg: StrategyConfig):
        self.token_ids = token_ids[:]
        self.store = store
        self.cfg = cfg
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._ws: Optional[WebSocketApp] = None
        self._ws_thread: Optional[threading.Thread] = None

    def start(self):
        self._stop.clear()
        if self.cfg.prefer_websocket and WS_AVAILABLE:
            self._start_ws()
        else:
            self._start_rest_poll()

    def stop(self):
        self._stop.set()
        try:
            if self._ws:
                self._ws.close()
        except Exception:
            pass
        if self._thread and self._thread.is_alive():
            self._thread.join(timeout=2)
        if self._ws_thread and self._ws_thread.is_alive():
            self._ws_thread.join(timeout=2)

    def set_token_ids(self, token_ids: List[str]):
        token_ids = token_ids[:]
        if token_ids == self.token_ids:
            return
        self.stop()
        self.token_ids = token_ids
        self.start()

    def _start_ws(self):
        def on_message(ws, message: str):
            try:
                data = json.loads(message)
            except Exception:
                return
            if data.get("event_type") == "book":
                token_id = str(data.get("asset_id") or data.get("token_id") or "")
                bids = data.get("bids") or data.get("buys") or []
                asks = data.get("asks") or data.get("sells") or []
                if token_id:
                    self.store.update_from_snapshot(token_id, bids, asks)

        def on_open(ws):
            try:
                payload = {"assets_ids": self.token_ids, "type": "market"}
                ws.send(json.dumps(payload))
            except Exception:
                pass

        def ws_supervisor():
            while not self._stop.is_set():
                try:
                    ws_base = CLOB_HOST.replace("https://", "wss://").replace("http://", "ws://")
                    url = f"{ws_base}/ws/market"
                    self._ws = WebSocketApp(url, on_message=on_message, on_open=on_open)
                    self._ws.run_forever(ping_interval=30, ping_timeout=10)
                except Exception:
                    pass
                for _ in range(10):
                    if self._stop.is_set():
                        break
                    time.sleep(0.5)

        self._ws_thread = threading.Thread(target=ws_supervisor, daemon=True)
        self._ws_thread.start()

        self._thread = threading.Thread(target=self._rest_poll_loop, daemon=True)
        self._thread.start()

    def _start_rest_poll(self):
        self._thread = threading.Thread(target=self._rest_poll_loop, daemon=True)
        self._thread.start()

    def _rest_poll_loop(self):
        while not self._stop.is_set():
            if not self.token_ids:
                time.sleep(self.cfg.rest_poll_interval_sec)
                continue
            try:
                body = [{"token_id": tid} for tid in self.token_ids]
                r = requests.post(
                    f"{CLOB_HOST}/books",
                    headers={"Content-Type": "application/json"},
                    data=json.dumps(body),
                    timeout=10,
                )
                if r.status_code == 200:
                    arr = r.json()
                    if isinstance(arr, list):
                        for item in arr:
                            tid = str(item.get("asset_id") or item.get("token_id") or "")
                            bids = item.get("bids") or []
                            asks = item.get("asks") or []
                            if tid:
                                self.store.update_from_snapshot(tid, bids, asks)
            except Exception:
                pass
            time.sleep(self.cfg.rest_poll_interval_sec)


class FileLogger:
    def __init__(self, trades_csv: str, results_csv: str):
        self.trades_csv = trades_csv
        self.results_csv = results_csv
        self._ensure_headers()

    def _ensure_headers(self):
        if not os.path.exists(self.trades_csv):
            with open(self.trades_csv, "w", newline="", encoding="utf-8") as f:
                w = csv.writer(f)
                w.writerow(["ts", "slug", "symbol", "token_id", "side", "price", "shares", "usd", "note"])

        if not os.path.exists(self.results_csv):
            with open(self.results_csv, "w", newline="", encoding="utf-8") as f:
                w = csv.writer(f)
                w.writerow([
                    "ts", "slug", "symbol", "end_time_utc", "winner",
                    "up_token", "down_token",
                    "up_shares", "down_shares",
                    "up_cost", "down_cost",
                    "pnl", "balance_after"
                ])

    def log_fill(self, fill: Fill):
        with open(self.trades_csv, "a", newline="", encoding="utf-8") as f:
            w = csv.writer(f)
            w.writerow([fill.ts, fill.slug, fill.symbol, fill.token_id, fill.side,
                        f"{fill.price:.6f}", f"{fill.shares:.6f}", f"{fill.usd:.6f}", fill.note])

    def log_result(self, res: MarketResult):
        with open(self.results_csv, "a", newline="", encoding="utf-8") as f:
            w = csv.writer(f)
            w.writerow([
                res.ts, res.slug, res.symbol, res.end_time_utc, res.winner,
                res.up_token, res.down_token,
                f"{res.up_shares:.6f}", f"{res.down_shares:.6f}",
                f"{res.up_cost:.6f}", f"{res.down_cost:.6f}",
                f"{res.pnl:.6f}", f"{res.balance_after:.6f}"
            ])


class PaperAccount:
    def __init__(self, cfg: StrategyConfig, symbol: str, starting_balance: float):
        self.cfg = cfg
        self.symbol = symbol
        self.cash = float(starting_balance)
        self.positions: Dict[str, Position] = {}

    def to_dict(self):
        return {"symbol": self.symbol, "cash": self.cash, "positions": {k: asdict(v) for k, v in self.positions.items()}}

    @staticmethod
    def from_dict(cfg: StrategyConfig, d: dict, default_symbol: str, default_balance: float) -> "PaperAccount":
        sym = str(d.get("symbol") or default_symbol)
        a = PaperAccount(cfg, sym, default_balance)
        a.cash = float(d.get("cash", default_balance))
        pos = d.get("positions", {}) or {}
        for k, v in pos.items():
            a.positions[str(k)] = Position(float(v.get("shares", 0.0)), float(v.get("cost_usd", 0.0)))
        return a


@dataclass
class ActiveMarket:
    symbol: str
    slug: str
    condition_id: str
    end_time: datetime
    up_token: str
    down_token: str


def gamma_list_markets(limit: int = 200, closed: Optional[bool] = None) -> List[dict]:
    params = {"limit": limit}
    if closed is not None:
        params["closed"] = "true" if closed else "false"
    r = requests.get(f"{GAMMA_API}/markets", params=params, timeout=15)
    r.raise_for_status()
    data = r.json()
    return data if isinstance(data, list) else []


def gamma_get_market_by_slug(slug: str) -> Optional[dict]:
    r = requests.get(f"{GAMMA_API}/markets/slug/{slug}", timeout=15)
    if r.status_code != 200:
        return None
    try:
        return r.json()
    except Exception:
        return None


def discover_next_market(cfg: StrategyConfig, only_symbol: Optional[str]) -> Optional[ActiveMarket]:
    try:
        markets = gamma_list_markets(limit=cfg.lookback_limit, closed=False)
    except Exception:
        return None

    now = utcnow()
    candidates: List[ActiveMarket] = []

    for m in markets:
        slug = str(m.get("slug") or "")
        if not slug:
            continue

        symbol = None
        for sym in cfg.symbols:
            pref = cfg.slug_prefix_map.get(sym, "")
            if pref and slug.startswith(pref):
                symbol = sym
                break
        if not symbol:
            continue
        if only_symbol and symbol != only_symbol:
            continue

        end_iso = m.get("endDateIso") or m.get("endDate") or m.get("end_date_iso")
        end_time = parse_iso_dt(end_iso)
        if not end_time:
            continue

        condition_id = str(m.get("conditionId") or m.get("condition_id") or "")
        clob_ids = parse_clob_token_ids(m.get("clobTokenIds"))

        if len(clob_ids) < 2:
            full = gamma_get_market_by_slug(slug)
            if full:
                condition_id = condition_id or str(full.get("conditionId") or full.get("condition_id") or "")
                clob_ids = parse_clob_token_ids(full.get("clobTokenIds"))

        if len(clob_ids) < 2 or not condition_id:
            continue

        candidates.append(ActiveMarket(
            symbol=symbol,
            slug=slug,
            condition_id=condition_id,
            end_time=end_time,
            up_token=str(clob_ids[0]),
            down_token=str(clob_ids[1]),
        ))

    if not candidates:
        return None

    future = [c for c in candidates if c.end_time >= now]
    if future:
        future.sort(key=lambda x: x.end_time)
        return future[0]

    candidates.sort(key=lambda x: abs((x.end_time - now).total_seconds()))
    return candidates[0]


def consume_asks_for_buy(ob: OrderBook, usd_to_spend: float) -> Tuple[float, float, float]:
    remaining = usd_to_spend
    shares = 0.0
    spent = 0.0
    for price, size in ob.asks:
        if remaining <= 1e-9:
            break
        if price <= 0:
            continue
        max_shares_here = min(size, remaining / price)
        if max_shares_here <= 0:
            continue
        cost_here = max_shares_here * price
        shares += max_shares_here
        spent += cost_here
        remaining -= cost_here
    avg = (spent / shares) if shares > 0 else 0.0
    return shares, avg, spent


class UpDownStrategyEngine:
    def __init__(self, cfg: StrategyConfig, mode: str):
        self.cfg = cfg
        self.mode = mode
        self.reset_for_new_market()

    def reset_for_new_market(self):
        self.primary: Optional[str] = None
        self.next_dca_threshold: Dict[str, float] = {"UP": self.cfg.entry_ask_cents / 100.0, "DOWN": self.cfg.entry_ask_cents / 100.0}
        self.dca_buys_done: Dict[str, int] = {"UP": 0, "DOWN": 0}

    def step(self, mkt: ActiveMarket, store: OrderBookStore, acct: PaperAccount, flog: FileLogger):
        up_ob = store.get(mkt.up_token)
        down_ob = store.get(mkt.down_token)

        up_ask = up_ob.best_ask()
        down_ask = down_ob.best_ask()
        up_bid = up_ob.best_bid()
        down_bid = down_ob.best_bid()

        if up_ask is None or down_ask is None or up_bid is None or down_bid is None:
            return

        if self.mode == "threshold35":
            self._step_threshold35(mkt, up_ob, down_ob, up_ask, down_ask, acct, flog)
        elif self.mode == "spread73_22":
            self._step_spread73_22(mkt, up_ob, down_ob, up_ask, down_ask, acct, flog)

    def _maybe_buy(self, mkt: ActiveMarket, token_id: str, ob: OrderBook, acct: PaperAccount, flog: FileLogger, usd: float, note: str):
        if usd <= 0:
            return
        if acct.cash < (usd + self.cfg.require_free_cash):
            return

        shares, avg, spent = consume_asks_for_buy(ob, usd)
        if shares <= 0 or spent <= 0:
            return

        acct.cash -= spent
        pos = acct.positions.get(token_id, Position())
        pos.shares += shares
        pos.cost_usd += spent
        acct.positions[token_id] = pos

        flog.log_fill(Fill(
            ts=utcnow().isoformat(),
            slug=mkt.slug,
            symbol=mkt.symbol,
            token_id=token_id,
            side="BUY",
            price=avg,
            shares=shares,
            usd=spent,
            note=note,
        ))

    def _dca_if_trigger(self, mkt: ActiveMarket, side_label: str, token_id: str, ob: OrderBook, best_ask: float, acct: PaperAccount, flog: FileLogger):
        pos = acct.positions.get(token_id, Position())
        if pos.cost_usd >= self.cfg.max_side_spend_usd:
            return

        threshold = self.next_dca_threshold.get(side_label, self.cfg.entry_ask_cents / 100.0)
        if best_ask <= threshold:
            if self.dca_buys_done[side_label] <= self.cfg.dca_levels:
                self._maybe_buy(mkt, token_id, ob, acct, flog, usd=self.cfg.buy_chunk_usd, note=f"{side_label}_entry_or_dca")
                self.dca_buys_done[side_label] += 1
                self.next_dca_threshold[side_label] = clamp(threshold - (self.cfg.dca_step_cents / 100.0), 0.01, 0.99)

    def _hedge_chunks(self, mkt: ActiveMarket, primary_token: str, hedge_token: str, primary_pos: Position, hedge_pos: Position,
                      hedge_ob: OrderBook, hedge_ask: float, acct: PaperAccount, flog: FileLogger):
        entry = self.cfg.entry_ask_cents / 100.0
        if hedge_ask > entry:
            return

        while hedge_pos.cost_usd + 1e-9 < primary_pos.cost_usd:
            if hedge_pos.cost_usd >= self.cfg.max_side_spend_usd:
                break

            est_shares, est_avg, est_spent = consume_asks_for_buy(hedge_ob, self.cfg.buy_chunk_usd)
            if est_shares <= 0 or est_spent <= 0:
                break

            combined = primary_pos.avg_price + est_avg
            if combined >= self.cfg.sum_cap_dollars:
                break

            self._maybe_buy(mkt, hedge_token, hedge_ob, acct, flog, usd=self.cfg.buy_chunk_usd, note="hedge_chunk_sumcap_ok")
            hedge_pos = acct.positions.get(hedge_token, Position())
            primary_pos = acct.positions.get(primary_token, Position())

    def _step_threshold35(self, mkt: ActiveMarket, up_ob: OrderBook, down_ob: OrderBook, up_ask: float, down_ask: float,
                          acct: PaperAccount, flog: FileLogger):
        entry = self.cfg.entry_ask_cents / 100.0
        up_pos = acct.positions.get(mkt.up_token, Position())
        down_pos = acct.positions.get(mkt.down_token, Position())

        if self.primary is None:
            if up_ask <= entry:
                self.primary = "UP"
            elif down_ask <= entry:
                self.primary = "DOWN"

        if self.primary == "UP":
            self._dca_if_trigger(mkt, "UP", mkt.up_token, up_ob, up_ask, acct, flog)
        elif self.primary == "DOWN":
            self._dca_if_trigger(mkt, "DOWN", mkt.down_token, down_ob, down_ask, acct, flog)

        up_pos = acct.positions.get(mkt.up_token, Position())
        down_pos = acct.positions.get(mkt.down_token, Position())

        if self.primary == "UP" and up_pos.cost_usd > 0:
            self._hedge_chunks(mkt, primary_token=mkt.up_token, hedge_token=mkt.down_token,
                               primary_pos=up_pos, hedge_pos=down_pos, hedge_ob=down_ob, hedge_ask=down_ask, acct=acct, flog=flog)
        if self.primary == "DOWN" and down_pos.cost_usd > 0:
            self._hedge_chunks(mkt, primary_token=mkt.down_token, hedge_token=mkt.up_token,
                               primary_pos=down_pos, hedge_pos=up_pos, hedge_ob=up_ob, hedge_ask=up_ask, acct=acct, flog=flog)

    def _step_spread73_22(self, mkt: ActiveMarket, up_ob: OrderBook, down_ob: OrderBook, up_ask: float, down_ask: float,
                          acct: PaperAccount, flog: FileLogger):
        if up_ask >= self.cfg.expensive_min and down_ask <= self.cfg.cheap_max and (up_ask + down_ask) < self.cfg.spread_sum_cap:
            self._maybe_buy(mkt, mkt.up_token, up_ob, acct, flog, usd=self.cfg.buy_chunk_usd, note="spread73_22_up_exp")
            self._maybe_buy(mkt, mkt.down_token, down_ob, acct, flog, usd=self.cfg.buy_chunk_usd, note="spread73_22_down_cheap")

        if down_ask >= self.cfg.expensive_min and up_ask <= self.cfg.cheap_max and (down_ask + up_ask) < self.cfg.spread_sum_cap:
            self._maybe_buy(mkt, mkt.down_token, down_ob, acct, flog, usd=self.cfg.buy_chunk_usd, note="spread73_22_down_exp")
            self._maybe_buy(mkt, mkt.up_token, up_ob, acct, flog, usd=self.cfg.buy_chunk_usd, note="spread73_22_up_cheap")


def pick_winner_from_book(mkt: ActiveMarket, store: OrderBookStore, settle_signal_bid: float) -> Optional[str]:
    up_bid = store.get(mkt.up_token).best_bid()
    down_bid = store.get(mkt.down_token).best_bid()
    if up_bid is None or down_bid is None:
        return None
    if up_bid >= settle_signal_bid:
        return "UP"
    if down_bid >= settle_signal_bid:
        return "DOWN"
    return None


def force_pick_winner(mkt: ActiveMarket, store: OrderBookStore) -> str:
    up_ob = store.get(mkt.up_token)
    down_ob = store.get(mkt.down_token)
    up_bid = up_ob.best_bid() or 0.0
    down_bid = down_ob.best_bid() or 0.0
    if up_bid > down_bid:
        return "UP"
    if down_bid > up_bid:
        return "DOWN"
    return "UP"


class GoogleDriveUploader:
    def __init__(self, drive_folder_id: str, oauth_token_json: str):
        if not GDRIVE_LIBS_AVAILABLE:
            raise RuntimeError("missing google drive libs")
        self.drive_folder_id = drive_folder_id.strip()
        self.token_data = json.loads(normalize_json_env(oauth_token_json))
        self.service = self._build_service()

    def _build_service(self):
        scopes = ["https://www.googleapis.com/auth/drive.file"]
        creds = Credentials.from_authorized_user_info(self.token_data, scopes=scopes)
        if creds and creds.expired and creds.refresh_token:
            creds.refresh(Request())
        return build("drive", "v3", credentials=creds, cache_discovery=False)

    def _find_file_id(self, filename: str) -> Optional[str]:
        safe_name = filename.replace("'", "\\'")
        q = f"'{self.drive_folder_id}' in parents and name = '{safe_name}' and trashed = false"
        resp = self.service.files().list(q=q, fields="files(id,name)", pageSize=1).execute()
        files = resp.get("files", [])
        return files[0]["id"] if files else None

    def upload_or_update(self, local_path: str, drive_filename: Optional[str] = None) -> str:
        if not os.path.exists(local_path):
            raise FileNotFoundError(local_path)
        filename = drive_filename or os.path.basename(local_path)
        file_id = self._find_file_id(filename)
        media = MediaFileUpload(local_path, resumable=False)
        if file_id:
            self.service.files().update(fileId=file_id, media_body=media).execute()
            return file_id
        meta = {"name": filename, "parents": [self.drive_folder_id]}
        created = self.service.files().create(body=meta, media_body=media, fields="id").execute()
        return created["id"]


def settle_market(mkt: ActiveMarket, store: OrderBookStore, acct: PaperAccount, flog: FileLogger,
                  cfg: StrategyConfig, uploader: Optional[GoogleDriveUploader]) -> MarketResult:
    up_pos = acct.positions.get(mkt.up_token, Position())
    down_pos = acct.positions.get(mkt.down_token, Position())

    up_shares, down_shares = up_pos.shares, down_pos.shares
    up_cost, down_cost = up_pos.cost_usd, down_pos.cost_usd

    start_wait = time.time()
    winner = None
    while time.time() - start_wait < cfg.settle_max_wait_sec:
        winner = pick_winner_from_book(mkt, store, cfg.settle_signal_bid)
        if winner:
            break
        time.sleep(0.5)
    if not winner:
        winner = force_pick_winner(mkt, store)

    redeem = (up_shares if winner == "UP" else down_shares) * 1.0
    acct.cash += redeem

    acct.positions[mkt.up_token] = Position(0.0, 0.0)
    acct.positions[mkt.down_token] = Position(0.0, 0.0)

    pnl = redeem - (up_cost + down_cost)

    res = MarketResult(
        ts=utcnow().isoformat(),
        slug=mkt.slug,
        symbol=mkt.symbol,
        end_time_utc=mkt.end_time.isoformat(),
        winner=winner,
        up_token=mkt.up_token,
        down_token=mkt.down_token,
        up_shares=up_shares,
        down_shares=down_shares,
        up_cost=up_cost,
        down_cost=down_cost,
        pnl=pnl,
        balance_after=acct.cash,
    )
    flog.log_result(res)

    if uploader is not None:
        try:
            uploader.upload_or_update(cfg.results_csv)
            uploader.upload_or_update(cfg.trades_csv)
            uploader.upload_or_update(cfg.state_path)
        except Exception:
            pass

    return res


def load_state(cfg: StrategyConfig, start_balances: Dict[str, float]) -> Tuple[Dict[str, PaperAccount], dict]:
    accounts: Dict[str, PaperAccount] = {sym: PaperAccount(cfg, sym, start_balances.get(sym, 100.0)) for sym in cfg.symbols}
    meta: dict = {}

    if not os.path.exists(cfg.state_path):
        return accounts, meta

    try:
        with open(cfg.state_path, "r", encoding="utf-8") as f:
            d = json.load(f)
        meta = d.get("meta", {}) or {}
        acc_blob = d.get("accounts")
        if isinstance(acc_blob, dict):
            for sym in cfg.symbols:
                if sym in acc_blob:
                    accounts[sym] = PaperAccount.from_dict(cfg, acc_blob[sym], sym, start_balances.get(sym, 100.0))
        return accounts, meta
    except Exception:
        return accounts, meta


def save_state(cfg: StrategyConfig, accounts: Dict[str, PaperAccount], meta: dict):
    try:
        d = {"accounts": {sym: acct.to_dict() for sym, acct in accounts.items()}, "meta": meta, "saved_at": utcnow().isoformat()}
        with open(cfg.state_path, "w", encoding="utf-8") as f:
            json.dump(d, f, indent=2)
    except Exception:
        pass


def init_gdrive_uploader_from_env() -> Optional[GoogleDriveUploader]:
    drive_folder_id = os.getenv("DRIVE_FOLDER_ID", "").strip()
    oauth_token_json = os.getenv("GOOGLE_OAUTH_TOKEN_JSON", "").strip()
    if not (drive_folder_id and oauth_token_json):
        print("[GDRIVE] disabled (missing env vars)")
        return None
    try:
        u = GoogleDriveUploader(drive_folder_id, oauth_token_json)
        print("[GDRIVE] enabled")
        return u
    except Exception as e:
        print(f"[GDRIVE] disabled (init failed): {e}")
        return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode", choices=["threshold35", "spread73_22"], default="threshold35")
    ap.add_argument("--symbol", choices=["BTC", "ETH", "SOL", "XRP", "AUTO"], default="AUTO")
    ap.add_argument("--prefer-rest", action="store_true")
    args = ap.parse_args()

    cfg = StrategyConfig()
    if args.prefer_rest:
        cfg.prefer_websocket = False

    # hard set start balances to 100 on all symbols
    start_balances = {"BTC": 100.0, "ETH": 100.0, "SOL": 100.0, "XRP": 100.0}

    flog = FileLogger(cfg.trades_csv, cfg.results_csv)
    accounts, meta = load_state(cfg, start_balances)
    uploader = init_gdrive_uploader_from_env()

    # create/upload the csvs immediately on startup (and state file too)
    if uploader is not None:
        try:
            uploader.upload_or_update(cfg.trades_csv)
            uploader.upload_or_update(cfg.results_csv)
            if not os.path.exists(cfg.state_path):
                with open(cfg.state_path, "w", encoding="utf-8") as f:
                    json.dump({"accounts": {}, "meta": {}, "saved_at": utcnow().isoformat()}, f)
            uploader.upload_or_update(cfg.state_path)
            print("[GDRIVE] initial upload done")
        except Exception as e:
            print(f"[GDRIVE] initial upload failed: {e}")

    store = OrderBookStore()
    engine = UpDownStrategyEngine(cfg, mode=args.mode)

    active_market: Optional[ActiveMarket] = None
    feed: Optional[MarketFeed] = None

    last_discovery = 0.0
    last_state_save = 0.0

    print(f"[START] mode={args.mode} chunk_usd={cfg.buy_chunk_usd} start_balance=100 ws_available={WS_AVAILABLE}")

    while True:
        now = utcnow()

        if active_market is None or (time.time() - last_discovery) >= cfg.discovery_refresh_sec:
            last_discovery = time.time()
            only_symbol = None if args.symbol == "AUTO" else args.symbol
            nxt = discover_next_market(cfg, only_symbol=only_symbol)
            if nxt and (active_market is None or nxt.slug != active_market.slug):
                active_market = nxt
                engine.reset_for_new_market()
                if feed is None:
                    feed = MarketFeed([active_market.up_token, active_market.down_token], store, cfg)
                    feed.start()
                else:
                    feed.set_token_ids([active_market.up_token, active_market.down_token])
                meta["active_market"] = {
                    "symbol": active_market.symbol,
                    "slug": active_market.slug,
                    "condition_id": active_market.condition_id,
                    "end_time": active_market.end_time.isoformat(),
                    "up_token": active_market.up_token,
                    "down_token": active_market.down_token,
                }

        if active_market is None or feed is None:
            time.sleep(1)
            continue

        acct = accounts[active_market.symbol]

        try:
            engine.step(active_market, store, acct, flog)
        except KeyboardInterrupt:
            raise
        except Exception:
            pass

        if now >= (active_market.end_time + timedelta(seconds=float(cfg.settle_grace_sec))):
            try:
                settle_market(active_market, store, acct, flog, cfg, uploader)
            except Exception:
                time.sleep(1)
                continue
            meta["last_settled"] = active_market.slug
            meta["active_market"] = None
            active_market = None

        if (time.time() - last_state_save) >= 3.0:
            last_state_save = time.time()
            save_state(cfg, accounts, meta)

        time.sleep(0.2)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)