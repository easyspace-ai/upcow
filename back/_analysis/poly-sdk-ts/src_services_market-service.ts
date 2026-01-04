/**
 * Market Service
 *
 * Provides enhanced market analysis features:
 * - K-Line aggregation from trade data
 * - Dual token K-Lines (YES + NO)
 * - Spread analysis
 * - Market signal detection
 */

import { DataApiClient, Trade } from '../clients/data-api.js';
import { GammaApiClient, GammaMarket } from '../clients/gamma-api.js';
import { ClobApiClient, ClobMarket } from '../clients/clob-api.js';
import type { UnifiedCache } from '../core/unified-cache.js';
import { PolymarketError, ErrorCode } from '../core/errors.js';
import type { UnifiedMarket, ProcessedOrderbook, ArbitrageOpportunity, KLineInterval, KLineCandle, DualKLineData, SpreadDataPoint, RealtimeSpreadAnalysis } from '../core/types.js';

export class MarketService {
  constructor(
    private gammaApi: GammaApiClient,
    private clobApi: ClobApiClient,
    private dataApi: DataApiClient,
    private cache: UnifiedCache
  ) {}

  // ===== Unified Market Access =====

  /**
   * Get market by slug or condition ID
   */
  async getMarket(identifier: string): Promise<UnifiedMarket> {
    const isConditionId = identifier.startsWith('0x') || /^\d+$/.test(identifier);

    if (isConditionId) {
      return this.getMarketByConditionId(identifier);
    } else {
      return this.getMarketBySlug(identifier);
    }
  }

  private async getMarketBySlug(slug: string): Promise<UnifiedMarket> {
    const gammaMarket = await this.gammaApi.getMarketBySlug(slug);
    if (!gammaMarket) {
      throw new PolymarketError(ErrorCode.MARKET_NOT_FOUND, `Market not found: ${slug}`);
    }

    try {
      const clobMarket = await this.clobApi.getMarket(gammaMarket.conditionId);
      return this.mergeMarkets(gammaMarket, clobMarket);
    } catch {
      return this.fromGammaMarket(gammaMarket);
    }
  }

  private async getMarketByConditionId(conditionId: string): Promise<UnifiedMarket> {
    // Try to get data from both sources for best accuracy
    let clobMarket: ClobMarket | null = null;
    let gammaMarket: GammaMarket | null = null;

    // Try CLOB first (authoritative for trading data)
    try {
      clobMarket = await this.clobApi.getMarket(conditionId);
    } catch {
      // CLOB failed, continue to try Gamma
    }

    // Always try Gamma for accurate slug and metadata
    try {
      gammaMarket = await this.gammaApi.getMarketByConditionId(conditionId);
    } catch {
      // Gamma failed
    }

    // Merge if both available (preferred)
    if (gammaMarket && clobMarket) {
      return this.mergeMarkets(gammaMarket, clobMarket);
    }

    // Gamma only - still useful for metadata
    if (gammaMarket) {
      return this.fromGammaMarket(gammaMarket);
    }

    // CLOB only - slug might be stale, add warning
    if (clobMarket) {
      const market = this.fromClobMarket(clobMarket);
      // Check if CLOB slug looks stale (doesn't match question keywords)
      const questionWords = clobMarket.question.toLowerCase().split(/\s+/).slice(0, 3);
      const slugWords = clobMarket.marketSlug.toLowerCase().split('-');
      const hasMatchingWord = questionWords.some(qw =>
        slugWords.some(sw => sw.includes(qw) || qw.includes(sw))
      );
      if (!hasMatchingWord && clobMarket.marketSlug.length > 0) {
        // Slug appears stale, use conditionId as fallback identifier
        market.slug = `market-${conditionId.slice(0, 10)}`;
      }
      return market;
    }

    throw new PolymarketError(ErrorCode.MARKET_NOT_FOUND, `Market not found: ${conditionId}`);
  }

  // ===== K-Line Aggregation =====

  /**
   * Get K-Line candles for a market (single token)
   */
  async getKLines(
    conditionId: string,
    interval: KLineInterval,
    options?: { limit?: number; tokenId?: string; outcomeIndex?: number }
  ): Promise<KLineCandle[]> {
    const trades = await this.dataApi.getTradesByMarket(conditionId, options?.limit || 1000);

    // Filter by token/outcome if specified
    let filteredTrades = trades;
    if (options?.tokenId) {
      filteredTrades = trades.filter((t) => t.asset === options.tokenId);
    } else if (options?.outcomeIndex !== undefined) {
      filteredTrades = trades.filter((t) => t.outcomeIndex === options.outcomeIndex);
    }

    return this.aggregateToKLines(filteredTrades, interval);
  }

  /**
   * Get dual K-Lines (YES + NO tokens)
   */
  async getDualKLines(
    conditionId: string,
    interval: KLineInterval,
    options?: { limit?: number }
  ): Promise<DualKLineData> {
    const market = await this.getMarket(conditionId);
    const trades = await this.dataApi.getTradesByMarket(conditionId, options?.limit || 1000);

    // Separate trades by outcome
    const yesTrades = trades.filter((t) => t.outcomeIndex === 0 || t.outcome === 'Yes');
    const noTrades = trades.filter((t) => t.outcomeIndex === 1 || t.outcome === 'No');

    const yesCandles = this.aggregateToKLines(yesTrades, interval);
    const noCandles = this.aggregateToKLines(noTrades, interval);

    // Get current orderbook for real-time spread analysis
    let currentOrderbook: ProcessedOrderbook | undefined;
    let realtimeSpread: RealtimeSpreadAnalysis | undefined;
    try {
      currentOrderbook = await this.clobApi.getProcessedOrderbook(conditionId);
      realtimeSpread = this.calculateRealtimeSpread(currentOrderbook);
    } catch {
      // Orderbook not available
    }

    // Calculate historical spread from trade close prices (for backtesting)
    const spreadAnalysis = this.analyzeHistoricalSpread(yesCandles, noCandles);

    return {
      conditionId,
      interval,
      market,
      yes: yesCandles,
      no: noCandles,
      spreadAnalysis,      // Historical (trade-based)
      realtimeSpread,      // Real-time (orderbook-based)
      currentOrderbook,
    };
  }

  /**
   * Aggregate trades into K-Line candles
   */
  private aggregateToKLines(trades: Trade[], interval: KLineInterval): KLineCandle[] {
    const intervalMs = getIntervalMs(interval);
    const buckets = new Map<number, Trade[]>();

    // Group trades into time buckets
    for (const trade of trades) {
      const bucketTime = Math.floor(trade.timestamp / intervalMs) * intervalMs;
      const bucket = buckets.get(bucketTime) || [];
      bucket.push(trade);
      buckets.set(bucketTime, bucket);
    }

    // Convert buckets to candles
    const candles: KLineCandle[] = [];
    for (const [timestamp, bucketTrades] of buckets) {
      if (bucketTrades.length === 0) continue;

      // Sort by timestamp for correct open/close
      bucketTrades.sort((a, b) => a.timestamp - b.timestamp);

      const prices = bucketTrades.map((t) => t.price);
      const buyTrades = bucketTrades.filter((t) => t.side === 'BUY');
      const sellTrades = bucketTrades.filter((t) => t.side === 'SELL');

      candles.push({
        timestamp,
        open: bucketTrades[0].price,
        high: Math.max(...prices),
        low: Math.min(...prices),
        close: bucketTrades[bucketTrades.length - 1].price,
        volume: bucketTrades.reduce((sum, t) => sum + t.size * t.price, 0),
        tradeCount: bucketTrades.length,
        buyVolume: buyTrades.reduce((sum, t) => sum + t.size * t.price, 0),
        sellVolume: sellTrades.reduce((sum, t) => sum + t.size * t.price, 0),
      });
    }

    return candles.sort((a, b) => a.timestamp - b.timestamp);
  }

  /**
   * Analyze historical spread from trade close prices (for backtesting)
   *
   * This uses trade close prices, not orderbook bid/ask.
   * Useful for:
   * - Historical analysis / backtesting
   * - Understanding past price movements
   * - Identifying patterns when orderbook data unavailable
   */
  private analyzeHistoricalSpread(
    yesCandles: KLineCandle[],
    noCandles: KLineCandle[]
  ): SpreadDataPoint[] {
    const yesMap = new Map(yesCandles.map((c) => [c.timestamp, c]));
    const noMap = new Map(noCandles.map((c) => [c.timestamp, c]));

    const allTimestamps = [...new Set([...yesMap.keys(), ...noMap.keys()])].sort();

    let lastYes = 0.5;
    let lastNo = 0.5;
    const analysis: SpreadDataPoint[] = [];

    for (const ts of allTimestamps) {
      const yesCandle = yesMap.get(ts);
      const noCandle = noMap.get(ts);

      if (yesCandle) lastYes = yesCandle.close;
      if (noCandle) lastNo = noCandle.close;

      const priceSum = lastYes + lastNo;
      const priceSpread = priceSum - 1;

      // Determine arb opportunity based on price deviation
      // Note: This is indicative only - actual arb requires orderbook analysis
      let arbOpportunity: 'LONG' | 'SHORT' | '' = '';
      if (priceSpread < -0.005) arbOpportunity = 'LONG';   // Sum < 0.995
      else if (priceSpread > 0.005) arbOpportunity = 'SHORT'; // Sum > 1.005

      analysis.push({
        timestamp: ts,
        yesPrice: lastYes,
        noPrice: lastNo,
        priceSum,
        priceSpread,
        arbOpportunity,
      });
    }

    return analysis;
  }

  /**
   * Calculate real-time spread from orderbook (for live trading)
   *
   * This uses orderbook bid/ask prices for accurate arbitrage detection.
   * Useful for:
   * - Real-time arbitrage execution
   * - Live trading decisions
   * - Accurate profit calculations
   */
  private calculateRealtimeSpread(orderbook: ProcessedOrderbook): RealtimeSpreadAnalysis {
    const { yes, no, summary } = orderbook;

    // Determine arbitrage opportunity
    let arbOpportunity: 'LONG' | 'SHORT' | '' = '';
    let arbProfitPercent = 0;

    if (summary.longArbProfit > 0.001) {  // > 0.1% threshold
      arbOpportunity = 'LONG';
      arbProfitPercent = summary.longArbProfit * 100;
    } else if (summary.shortArbProfit > 0.001) {  // > 0.1% threshold
      arbOpportunity = 'SHORT';
      arbProfitPercent = summary.shortArbProfit * 100;
    }

    return {
      timestamp: Date.now(),
      // Orderbook prices
      yesBid: yes.bid,
      yesAsk: yes.ask,
      noBid: no.bid,
      noAsk: no.ask,
      // Spread metrics
      askSum: summary.askSum,
      bidSum: summary.bidSum,
      askSpread: summary.askSum - 1,
      bidSpread: summary.bidSum - 1,
      // Arbitrage
      longArbProfit: summary.longArbProfit,
      shortArbProfit: summary.shortArbProfit,
      arbOpportunity,
      arbProfitPercent,
    };
  }

  /**
   * Get real-time spread analysis only (without K-lines)
   * Use this for quick arbitrage checks
   */
  async getRealtimeSpread(conditionId: string): Promise<RealtimeSpreadAnalysis> {
    const orderbook = await this.clobApi.getProcessedOrderbook(conditionId);
    return this.calculateRealtimeSpread(orderbook);
  }

  // ===== Orderbook Analysis =====

  /**
   * Get processed orderbook with analytics
   */
  async getOrderbook(conditionId: string): Promise<ProcessedOrderbook> {
    return this.clobApi.getProcessedOrderbook(conditionId);
  }

  /**
   * Detect arbitrage opportunity
   *
   * 使用有效价格（考虑镜像订单）计算套利机会
   * 详细原理见: docs/01-polymarket-orderbook-arbitrage.md
   */
  async detectArbitrage(conditionId: string, threshold = 0.005): Promise<ArbitrageOpportunity | null> {
    const orderbook = await this.getOrderbook(conditionId);
    const { effectivePrices } = orderbook.summary;

    if (orderbook.summary.longArbProfit > threshold) {
      return {
        type: 'long',
        profit: orderbook.summary.longArbProfit,
        // 使用有效价格描述实际操作
        action: `Buy YES @ ${effectivePrices.effectiveBuyYes.toFixed(4)} + NO @ ${effectivePrices.effectiveBuyNo.toFixed(4)}, Merge for $1`,
        expectedProfit: orderbook.summary.longArbProfit,
      };
    }

    if (orderbook.summary.shortArbProfit > threshold) {
      return {
        type: 'short',
        profit: orderbook.summary.shortArbProfit,
        // 使用有效价格描述实际操作
        action: `Split $1, Sell YES @ ${effectivePrices.effectiveSellYes.toFixed(4)} + NO @ ${effectivePrices.effectiveSellNo.toFixed(4)}`,
        expectedProfit: orderbook.summary.shortArbProfit,
      };
    }

    return null;
  }

  // ===== Market Discovery =====

  /**
   * Get trending markets
   */
  async getTrendingMarkets(limit = 20): Promise<GammaMarket[]> {
    return this.gammaApi.getTrendingMarkets(limit);
  }

  /**
   * Search markets
   */
  async searchMarkets(params: {
    active?: boolean;
    closed?: boolean;
    limit?: number;
    offset?: number;
    order?: string;
  }): Promise<GammaMarket[]> {
    return this.gammaApi.getMarkets(params);
  }

  // ===== Market Signal Detection =====

  /**
   * Detect market signals (volume surge, depth imbalance, whale trades)
   */
  async detectMarketSignals(conditionId: string): Promise<
    Array<{
      type: 'volume_surge' | 'depth_imbalance' | 'whale_trade' | 'momentum';
      severity: 'low' | 'medium' | 'high';
      details: Record<string, unknown>;
    }>
  > {
    const signals: Array<{
      type: 'volume_surge' | 'depth_imbalance' | 'whale_trade' | 'momentum';
      severity: 'low' | 'medium' | 'high';
      details: Record<string, unknown>;
    }> = [];

    const market = await this.getMarket(conditionId);
    const orderbook = await this.getOrderbook(conditionId);
    const trades = await this.dataApi.getTradesByMarket(conditionId, 100);

    // Volume surge detection
    if (market.volume24hr && market.volume > 0) {
      const avgDaily = market.volume / 7; // Approximate
      const ratio = market.volume24hr / avgDaily;
      if (ratio > 2) {
        signals.push({
          type: 'volume_surge',
          severity: ratio > 5 ? 'high' : ratio > 3 ? 'medium' : 'low',
          details: { volume24hr: market.volume24hr, avgDaily, ratio },
        });
      }
    }

    // Depth imbalance detection
    if (orderbook.summary.imbalanceRatio > 1.5 || orderbook.summary.imbalanceRatio < 0.67) {
      const ratio = orderbook.summary.imbalanceRatio;
      signals.push({
        type: 'depth_imbalance',
        severity: ratio > 3 || ratio < 0.33 ? 'high' : 'medium',
        details: {
          imbalanceRatio: ratio,
          bidDepth: orderbook.summary.totalBidDepth,
          askDepth: orderbook.summary.totalAskDepth,
          direction: ratio > 1 ? 'BUY_PRESSURE' : 'SELL_PRESSURE',
        },
      });
    }

    // Whale trade detection
    const recentLargeTrades = trades.filter((t) => t.size * t.price > 1000);
    for (const trade of recentLargeTrades.slice(0, 3)) {
      const value = trade.size * trade.price;
      signals.push({
        type: 'whale_trade',
        severity: value > 10000 ? 'high' : value > 5000 ? 'medium' : 'low',
        details: {
          size: trade.size,
          price: trade.price,
          usdValue: value,
          side: trade.side,
          outcome: trade.outcome,
        },
      });
    }

    return signals;
  }

  // ===== Helper Methods =====

  private mergeMarkets(gamma: GammaMarket, clob: ClobMarket): UnifiedMarket {
    const yesToken = clob.tokens.find((t) => t.outcome === 'Yes');
    const noToken = clob.tokens.find((t) => t.outcome === 'No');

    return {
      conditionId: clob.conditionId,
      slug: gamma.slug,
      question: clob.question,
      description: clob.description || gamma.description,
      tokens: {
        yes: { tokenId: yesToken?.tokenId || '', price: yesToken?.price || gamma.outcomePrices[0] || 0.5 },
        no: { tokenId: noToken?.tokenId || '', price: noToken?.price || gamma.outcomePrices[1] || 0.5 },
      },
      volume: gamma.volume,
      volume24hr: gamma.volume24hr,
      liquidity: gamma.liquidity,
      spread: gamma.spread,
      oneDayPriceChange: gamma.oneDayPriceChange,
      oneWeekPriceChange: gamma.oneWeekPriceChange,
      active: clob.active,
      closed: clob.closed,
      acceptingOrders: clob.acceptingOrders,
      endDate: clob.endDateIso ? new Date(clob.endDateIso) : new Date(),
      source: 'merged',
    };
  }

  private fromGammaMarket(gamma: GammaMarket): UnifiedMarket {
    return {
      conditionId: gamma.conditionId,
      slug: gamma.slug,
      question: gamma.question,
      description: gamma.description,
      tokens: {
        yes: { tokenId: '', price: gamma.outcomePrices[0] || 0.5 },
        no: { tokenId: '', price: gamma.outcomePrices[1] || 0.5 },
      },
      volume: gamma.volume,
      volume24hr: gamma.volume24hr,
      liquidity: gamma.liquidity,
      spread: gamma.spread,
      oneDayPriceChange: gamma.oneDayPriceChange,
      oneWeekPriceChange: gamma.oneWeekPriceChange,
      active: gamma.active,
      closed: gamma.closed,
      acceptingOrders: !gamma.closed,
      endDate: gamma.endDate,
      source: 'gamma',
    };
  }

  private fromClobMarket(clob: ClobMarket): UnifiedMarket {
    const yesToken = clob.tokens.find((t) => t.outcome === 'Yes');
    const noToken = clob.tokens.find((t) => t.outcome === 'No');

    return {
      conditionId: clob.conditionId,
      slug: clob.marketSlug,
      question: clob.question,
      description: clob.description,
      tokens: {
        yes: { tokenId: yesToken?.tokenId || '', price: yesToken?.price || 0.5 },
        no: { tokenId: noToken?.tokenId || '', price: noToken?.price || 0.5 },
      },
      volume: 0,
      volume24hr: undefined,
      liquidity: 0,
      spread: undefined,
      active: clob.active,
      closed: clob.closed,
      acceptingOrders: clob.acceptingOrders,
      endDate: clob.endDateIso ? new Date(clob.endDateIso) : new Date(),
      source: 'clob',
    };
  }
}

// ===== Utility Functions =====

export function getIntervalMs(interval: KLineInterval): number {
  const map: Record<KLineInterval, number> = {
    '30s': 30 * 1000,
    '1m': 60 * 1000,
    '5m': 5 * 60 * 1000,
    '15m': 15 * 60 * 1000,
    '30m': 30 * 60 * 1000,
    '1h': 60 * 60 * 1000,
    '4h': 4 * 60 * 60 * 1000,
    '12h': 12 * 60 * 60 * 1000,
    '1d': 24 * 60 * 60 * 1000,
  };
  return map[interval];
}
