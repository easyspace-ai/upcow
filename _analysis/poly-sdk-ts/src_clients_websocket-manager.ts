/**
 * WebSocket Manager
 *
 * Wraps @nevuamarkets/poly-websockets for real-time market data:
 * - Price updates (derived from order book and trades)
 * - Order book snapshots
 * - Last trade price events
 *
 * Features:
 * - Automatic connection management
 * - Rate limiting built-in
 * - Price caching for quick access
 * - EventEmitter-based event distribution
 */

import { EventEmitter } from 'events';
import type { PriceUpdate, BookUpdate } from '../core/types.js';

// Note: poly-websockets types (these match the library's exports)
interface PolymarketPriceUpdateEvent {
  event_type: 'price_update';
  asset_id: string;
  timestamp: string;
  price: string;
  midpoint: string;
  spread: string;
}

interface BookEvent {
  market: string;
  asset_id: string;
  timestamp: string;
  bids: Array<{ price: string; size: string }>;
  asks: Array<{ price: string; size: string }>;
  event_type: 'book';
}

interface LastTradePriceEvent {
  asset_id: string;
  event_type: 'last_trade_price';
  price: string;
  side: 'BUY' | 'SELL';
  size: string;
  timestamp: string;
}

export interface WebSocketManagerConfig {
  maxMarketsPerWS?: number;
  enableLogging?: boolean;
}

export interface WebSocketManagerEvents {
  priceUpdate: (update: PriceUpdate) => void;
  bookUpdate: (update: BookUpdate) => void;
  lastTrade: (trade: { assetId: string; price: number; side: 'BUY' | 'SELL'; size: number; timestamp: number }) => void;
  connected: (info: { groupId: string; assetIds: string[] }) => void;
  disconnected: (info: { groupId: string; code: number; reason: string }) => void;
  error: (error: Error) => void;
}

export class WebSocketManager extends EventEmitter {
  private wsManager: unknown; // WSSubscriptionManager instance
  private subscriptions: Set<string> = new Set();
  private priceCache: Map<string, PriceUpdate> = new Map();
  private bookCache: Map<string, BookUpdate> = new Map();
  private initialized = false;
  private config: WebSocketManagerConfig;

  constructor(config: WebSocketManagerConfig = {}) {
    super();
    this.config = {
      maxMarketsPerWS: config.maxMarketsPerWS || 100,
      enableLogging: config.enableLogging ?? false,
    };
  }

  /**
   * Initialize the WebSocket manager (lazy initialization)
   */
  private async ensureInitialized(): Promise<void> {
    if (this.initialized) return;

    try {
      // Dynamic import to handle the package not being installed
      const { WSSubscriptionManager } = await import('@nevuamarkets/poly-websockets');

      this.wsManager = new WSSubscriptionManager(
        {
          onPolymarketPriceUpdate: this.handlePriceUpdate.bind(this),
          onBook: this.handleBookUpdate.bind(this),
          onLastTradePrice: this.handleLastTradePrice.bind(this),
          onWSOpen: this.handleOpen.bind(this),
          onWSClose: this.handleClose.bind(this),
          onError: this.handleError.bind(this),
        },
        {
          maxMarketsPerWS: this.config.maxMarketsPerWS,
        }
      );

      this.initialized = true;
    } catch (error) {
      throw new Error(
        'Failed to initialize WebSocket manager. Make sure @nevuamarkets/poly-websockets is installed.'
      );
    }
  }

  // ===== Subscription Management =====

  /**
   * Subscribe to asset price updates
   */
  async subscribe(assetIds: string[]): Promise<void> {
    await this.ensureInitialized();

    const newIds = assetIds.filter((id) => !this.subscriptions.has(id));
    if (newIds.length === 0) return;

    const manager = this.wsManager as { addSubscriptions: (ids: string[]) => Promise<void> };
    await manager.addSubscriptions(newIds);
    newIds.forEach((id) => this.subscriptions.add(id));
  }

  /**
   * Unsubscribe from asset price updates
   */
  async unsubscribe(assetIds: string[]): Promise<void> {
    if (!this.initialized) return;

    const existingIds = assetIds.filter((id) => this.subscriptions.has(id));
    if (existingIds.length === 0) return;

    const manager = this.wsManager as { removeSubscriptions: (ids: string[]) => Promise<void> };
    await manager.removeSubscriptions(existingIds);
    existingIds.forEach((id) => {
      this.subscriptions.delete(id);
      this.priceCache.delete(id);
      this.bookCache.delete(id);
    });
  }

  /**
   * Unsubscribe from all assets and cleanup
   */
  async unsubscribeAll(): Promise<void> {
    if (!this.initialized) return;

    const manager = this.wsManager as { clearState: () => Promise<void> };
    await manager.clearState();
    this.subscriptions.clear();
    this.priceCache.clear();
    this.bookCache.clear();
  }

  // ===== Price Cache Access =====

  /**
   * Get cached price for an asset
   */
  getPrice(assetId: string): PriceUpdate | undefined {
    return this.priceCache.get(assetId);
  }

  /**
   * Get all cached prices
   */
  getAllPrices(): Map<string, PriceUpdate> {
    return new Map(this.priceCache);
  }

  /**
   * Get cached order book for an asset
   */
  getBook(assetId: string): BookUpdate | undefined {
    return this.bookCache.get(assetId);
  }

  // ===== State Query =====

  /**
   * Get subscribed asset IDs
   */
  getSubscribedAssets(): string[] {
    return Array.from(this.subscriptions);
  }

  /**
   * Get connection statistics
   */
  getStatistics(): { groups: number; subscriptions: number } | null {
    if (!this.initialized) return null;

    const manager = this.wsManager as { getStatistics: () => { groups: number; subscriptions: number } };
    return manager.getStatistics();
  }

  /**
   * Check if initialized
   */
  isInitialized(): boolean {
    return this.initialized;
  }

  // ===== Event Handlers =====

  private async handlePriceUpdate(events: PolymarketPriceUpdateEvent[]): Promise<void> {
    for (const event of events) {
      const update: PriceUpdate = {
        assetId: event.asset_id,
        price: parseFloat(event.price),
        midpoint: parseFloat(event.midpoint),
        spread: parseFloat(event.spread),
        timestamp: parseInt(event.timestamp, 10),
      };
      this.priceCache.set(event.asset_id, update);
      this.emit('priceUpdate', update);
    }
  }

  private async handleBookUpdate(events: BookEvent[]): Promise<void> {
    for (const event of events) {
      // Parse and sort bids descending (highest price = best bid first)
      const bids = event.bids
        .map((l) => ({ price: parseFloat(l.price), size: parseFloat(l.size) }))
        .sort((a, b) => b.price - a.price);

      // Parse and sort asks ascending (lowest price = best ask first)
      const asks = event.asks
        .map((l) => ({ price: parseFloat(l.price), size: parseFloat(l.size) }))
        .sort((a, b) => a.price - b.price);

      const update: BookUpdate = {
        assetId: event.asset_id,
        bids,
        asks,
        timestamp: parseInt(event.timestamp, 10),
      };
      this.bookCache.set(event.asset_id, update);
      this.emit('bookUpdate', update);
    }
  }

  private async handleLastTradePrice(events: LastTradePriceEvent[]): Promise<void> {
    for (const event of events) {
      this.emit('lastTrade', {
        assetId: event.asset_id,
        price: parseFloat(event.price),
        side: event.side,
        size: parseFloat(event.size),
        timestamp: parseInt(event.timestamp, 10),
      });
    }
  }

  private async handleOpen(groupId: string, assetIds: string[]): Promise<void> {
    this.emit('connected', { groupId, assetIds });
  }

  private async handleClose(groupId: string, code: number, reason: string): Promise<void> {
    this.emit('disconnected', { groupId, code, reason });
  }

  private async handleError(error: Error): Promise<void> {
    this.emit('error', error);
  }
}
