import polymarket from '../feeds/polymarket.js';
import polyChainlink from '../feeds/oracle.js';
import binance from '../feeds/binance.js';
import axios from 'axios';
import { CONFIG } from '../config/constants.js';
import logger from '../utils/logger.js';
import { MathUtils } from '../utils/math.js'; // 导入上面新建的数学库

function sleep(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
}

class MyBot {
    constructor() {
        this.proxyUrl = CONFIG.PROXY_SERVER;
        this.priceLogTs = 0;
        this.chainlinkPrice = 0;

        // --- 策略参数 ---
        this.k = 0.08;
        this.c = 0.10;
        this.sizePerTrade = 12;
        this.inventorySkewFactor = 0.005 / 100;

        this.baseMinEdgeMaker = 0.0005; // 0.05%
        this.baseMinEdgeTaker = 0.003;  // 0.30%
        this.marketWeight = 0.7;        // 70% 权重跟随市场

        // --- 分级熔断参数 ---
        this.decayStartTime = 300;      // 剩余300秒开始衰减
        this.reduceOnlyTime = 300;      // 剩余300秒只减不加
        this.forceCloseTime = 180;      // 剩余180秒强制平仓
        this.maxEdgeAtZero = 0.02;      // 结束时要求的额外利润门槛

        // --- 风控参数 ---
        this.hedgeThreshold = 80;       // 净持仓阈值
        this.stopQuoteThreshold = 60;
        this.hedgeSizeMultiplier = 1.5;

        // --- 运行时状态 ---
        this.marketInfo = {
            slug: null,
            startTime: 0,
            endDate: null,
            strikePrice: null,
        };

        // 本地模拟持仓 (实盘请对接 API /positions)
        this.makerOrders = { UP: {}, DOWN: {} }; // 用于维护挂单列表：防止重复挂单、模拟挂单成交
        this.inventory = { UP: 0, DOWN: 0 };
        this.cash = 0; // 用于简单的 PnL 统计
    }

    async start() {
        // 1. 监听 Polymarket 市场元数据
        polymarket.on('market_update', (data) => {
            if(this.marketInfo.slug){
                logger.info(`\n============== 结束统计 ${this.marketInfo.slug} ==============`);
                logger.info(`最终持仓 UP:${this.inventory.UP} DOWN:${this.inventory.DOWN} 总成本:${Math.abs(this.cash)}`);
                const endDirect = this.chainlinkPrice - this.marketInfo.strikePrice > 0 ? 'UP' : 'DOWN';

                logger.info(`最终盈亏 ${this.inventory[endDirect] - Math.abs(this.cash)}`);
                logger.info(`\n`);
            }

            logger.info(`\n============== 新一轮市场 ${data.slug} ==============`);
            // 重置状态
            this.marketInfo = data;
            this.inventory = { UP: 0, DOWN: 0 };
            this.cash = 0;

            if(polyChainlink.ws){
                polyChainlink.subscribe(data.slug);
            } else {
                setTimeout(() => {
                    polyChainlink.subscribe(data.slug);
                }, 3000);
            }

            // 异步获取行权价
            this.ensureStrikePrice();
        });
        await polymarket.initialize();

        // 2. 启动预言机 (备用)
        polyChainlink.on('price', (data) => { this.chainlinkPrice = data.price; });
        polyChainlink.connect();

        // 3. 监听币安行情 (驱动策略 Tick)
        binance.on('tick', (data) => this.onTick(data));
        binance.connect();
    }

    /**
     * 异步获取行权价
     * @returns {Promise<void>}
     */
    async ensureStrikePrice() {
        let attempts = 0;
        while (attempts < 20) {
            const startIso = new Date(this.marketInfo.startTime * 1000).toISOString().split('.')[0] + 'Z';
            const endIso = this.marketInfo.endDate.toISOString().split('.')[0] + 'Z';
            logger.info(`获取官方行权价 ${startIso} 至 ${endIso}`);
            const strike = await this.fetchStrikePrice(startIso, endIso);
            if (strike) {
                this.marketInfo.strikePrice = strike;
                logger.info(`🎯 锁定官方行权价: ${this.marketInfo.strikePrice}`);
                break;
            }
            await sleep(5000);
            attempts++;
        }
    }

    /**
     * 计算动态 Maker Edge (越临近结束要求越高)
     */
    getDynamicMakerEdge(remaining) {
        if (remaining > this.decayStartTime) {
            return this.baseMinEdgeMaker;
        } else {
            const progress = (this.decayStartTime - remaining) / this.decayStartTime;
            const p = Math.max(0.0, Math.min(1.0, progress));
            return this.baseMinEdgeMaker + p * (this.maxEdgeAtZero - this.baseMinEdgeMaker);
        }
    }

    /**
     * 核心策略循环 (v5.1 Logic)
     */
    async onTick({ spot, fut, ts }) {
        if (!this.marketInfo.slug) return;

        // 计算剩余时间 (秒)
        // 注意：ts 是毫秒，startTime 是秒
        const nowSec = ts / 1000;
        const remaining = CONFIG.MARKET.INTERVAL_SECONDS - (nowSec - this.marketInfo.startTime);

        // 兜底逻辑：如果开始10s还没拿到官方 Strike，就用 Chainlink 顶替
        if ((remaining <= 0 || remaining > CONFIG.MARKET.INTERVAL_SECONDS - 10) && !this.marketInfo.strikePrice) {
            if(this.chainlinkPrice) this.marketInfo.strikePrice = this.chainlinkPrice;
        }

        // 如果没有 Strike Price，无法计算 Delta，跳过
        if (!this.marketInfo.strikePrice) return;

        // 获取盘口数据
        // 注意：getExecutionData 返回的是 { price(Ask), bid, ... }
        const marketUp = polymarket.getExecutionData('UP');
        const marketDown = polymarket.getExecutionData('DOWN');

        // ---------------- v5.1 核心算法开始 ----------------

        // 判定Maker成交
        // --- 检查 UP 方向的买单 ---
        Object.keys(this.makerOrders.UP).forEach(priceKey => {
            const orderPrice = parseFloat(priceKey.replace('@', ''));
            const orderSize = this.makerOrders.UP[priceKey];
            // 判定条件：市场的卖一价 (Ask) 已经跌到了我的买单价以下或相等
            // 这意味着市场上有人愿意以这个价格卖出，我的买单被成交了
            if (marketUp.ask > 0 && marketUp.ask <= orderPrice) {
                logger.info(`✅ MAKER 成交: Buy UP @ ${orderPrice} (Size: ${orderSize})`);

                // 执行成交逻辑 (更新持仓)
                this.inventory.UP += orderSize;
                this.cash -= orderPrice * orderSize;

                // 从挂单列表中移除
                delete this.makerOrders.UP[priceKey];

                // 打印最新持仓
                const net = this.inventory.UP - this.inventory.DOWN;
                logger.info(`📊 Inv: UP:${this.inventory.UP} DOWN:${this.inventory.DOWN} Net:${net} | Est.Cost: ${Math.abs(this.cash).toFixed(2)}`);
            }
        });
        // --- 检查 DOWN 方向的买单 ---
        Object.keys(this.makerOrders.DOWN).forEach(priceKey => {
            const orderPrice = parseFloat(priceKey.replace('@', ''));
            const orderSize = this.makerOrders.DOWN[priceKey];

            // 判定条件：市场的卖一价 (Ask) <= 我的买单价
            if (marketDown.ask > 0 && marketDown.ask <= orderPrice) {
                logger.info(`✅ MAKER 成交: Buy DOWN @ ${orderPrice} (Size: ${orderSize})`);

                this.inventory.DOWN += orderSize;
                this.cash -= orderPrice * orderSize;

                delete this.makerOrders.DOWN[priceKey];

                const net = this.inventory.UP - this.inventory.DOWN;
                logger.info(`📊 Inv: UP:${this.inventory.UP} DOWN:${this.inventory.DOWN} Net:${net} | Est.Cost: ${Math.abs(this.cash).toFixed(2)}`);
            }
        });

        // 1. 定价模型 (Adaptive Center)
        // 使用合约价格计算 Delta (反应更快)
        const delta = fut - this.marketInfo.strikePrice;

        // 防止除以0
        const timeFactor = Math.sqrt(Math.max(1, remaining));
        const rawX = delta / timeFactor;

        // 模型概率
        const z = this.k * rawX + this.c;
        const modelFairUp = MathUtils.normCdf(z);

        // 市场中枢 (Mid Price)
        let marketMidUp = modelFairUp; // 默认
        if (marketUp.bid > 0 && marketUp.ask > 0) {
            marketMidUp = (marketUp.bid + marketUp.ask) / 2;
        }

        // 融合概率 (70% 市场权重)
        const finalFairUp = (1 - this.marketWeight) * modelFairUp + this.marketWeight * marketMidUp;
        const finalFairDown = 1.0 - finalFairUp;

        // 库存偏斜 (Skew)
        const netInv = this.inventory.UP - this.inventory.DOWN;
        const skew = netInv * this.inventorySkewFactor;

        // 基于库存偏斜动态调整报价
        // 如果囤了太多的 UP (netInv > 0)，需要降低 UP 的买入价（不想再买贵的了），时降低 UP 的卖出价（赶紧便宜点卖出去），反之亦然
        const resPriceUp = finalFairUp - skew;
        const resPriceDown = finalFairDown + skew;

        // 2. 状态判定
        const currentMakerEdge = this.getDynamicMakerEdge(remaining);
        const isReduceOnly = remaining < this.reduceOnlyTime;  // < 300s
        const isForceClose = remaining < this.forceCloseTime;  // < 180s

        // ---------------- 决策逻辑 ----------------
        if(ts - this.priceLogTs >= 1000){
            logger.info(`-`);
        }

        let action = null;

        // [A] 强制平仓 (Force Close) - 最高优先级
        if (isForceClose) {
            if (Math.abs(netInv) >= 5) { // 只有敞口较大时才操作
                if (netInv > 0) {
                    // 持有净多头 (UP)，需要卖 UP -> 即买入 DOWN (Taker)
                    if (marketDown.ask > 0 && marketDown.ask < 0.99) {
                        action = { type: 'FORCE_CLOSE', side: 'DOWN', price: marketDown.ask, size: this.sizePerTrade };
                    }
                } else {
                    // 持有净空头 (DOWN)，需要买入 UP (Taker)
                    if (marketUp.ask > 0 && marketUp.ask < 0.99) {
                        action = { type: 'FORCE_CLOSE', side: 'UP', price: marketUp.ask, size: this.sizePerTrade };
                    }
                }
            }
        }

        // [B] 正常逻辑 (如果没触发强制平仓)
        if (!action && !isForceClose) {
            // 风控对冲
            let forceActionSide = null;
            if (netInv > this.hedgeThreshold) forceActionSide = 'DOWN';
            else if (netInv < -this.hedgeThreshold) forceActionSide = 'UP';

            if (forceActionSide) {
                // Taker Hedge：通过Taker纠偏
                const targetBook = forceActionSide === 'UP' ? marketUp : marketDown;
                const fairPrice = forceActionSide === 'UP' ? finalFairUp : finalFairDown;

                if (targetBook.ask > 0 && targetBook.ask < fairPrice + 0.03) {
                    action = {
                        type: 'TAKER_HEDGE',
                        side: forceActionSide,
                        price: targetBook.ask,
                        size: this.sizePerTrade * this.hedgeSizeMultiplier
                    };
                }
            }

            // --- 交易逻辑 (修正版) ---
            if (!action) {
                // ----------------------
                // UP 方向 (买入 UP)
                // ----------------------
                // 允许交易条件：1. 正常交易期 OR 2. 减仓期且买入能平空头
                const allowTradeUp = (!isReduceOnly) || (netInv < 0);
                const allowTradeDown = (!isReduceOnly) || (netInv > 0);

                // 目标挂单价 = 市场买一价 + 0.001 (压价一档，成为最优买价)
                const targetUpBid = marketUp.bid + 0.001;
                const targetDownBid = marketDown.bid + 0.001;

                // 如果允许交易且库存未超限
                if (allowTradeUp && netInv < this.stopQuoteThreshold) {

                    // 1. Taker (主动吃单): 市场卖价极低，直接买入
                    if (marketUp.ask > 0 && marketUp.ask < resPriceUp - this.baseMinEdgeTaker) {
                        action = { type: 'TAKER', side: 'UP', price: marketUp.ask, size: this.sizePerTrade };
                    }
                    // 2. Maker (被动挂单): 市场卖价不够低，但我愿意挂个买单等别人卖给我
                    else {
                        // 检查这个挂单价是否有利润 (保留价格 - 挂单价 > Maker门槛)
                        if (targetUpBid < resPriceUp - currentMakerEdge) {
                            // 生成 Maker 信号 (实盘中需 OrderManager 执行)
                            action = { type: 'MAKER', side: 'UP', price: targetUpBid, size: this.sizePerTrade };
                            // logger.info(`💡 建议挂单 UP @ ${targetUpBid}`);
                        }
                    }
                }

                // ----------------------
                // DOWN 方向 (买入 DOWN) - [已补全 Maker 逻辑]
                // ----------------------
                // 如果还没决定做 UP，再看 DOWN
                if (!action) {
                    if (allowTradeDown && netInv > -this.stopQuoteThreshold) {
                        // 1. Taker (主动吃单): 市场卖价极低，直接买入
                        if (marketDown.ask > 0 && marketDown.ask < resPriceDown - this.baseMinEdgeTaker) {
                            action = { type: 'TAKER', side: 'DOWN', price: marketDown.ask, size: this.sizePerTrade };
                        }
                        // 2. Maker (被动挂单)
                        else {
                            if (targetDownBid < resPriceDown - currentMakerEdge) {
                                // 生成 Maker 信号
                                action = { type: 'MAKER', side: 'DOWN', price: targetDownBid, size: this.sizePerTrade };
                                // logger.info(`💡 建议挂单 DOWN @ ${targetDownBid}`);
                            }
                        }
                    }
                }

                if (ts - this.priceLogTs >= 1000) {
                    const isTakerUp = marketUp.ask < resPriceUp - this.baseMinEdgeTaker;
                    const isTakerDown = marketDown.ask < resPriceDown - this.baseMinEdgeTaker;

                    const isMakerUp = targetUpBid < resPriceUp - currentMakerEdge;
                    const isMakerDown = targetDownBid < resPriceDown - currentMakerEdge;
                    logger.info(`AllowUp:${allowTradeUp} AllowDown:${allowTradeDown} | MakerUp:${isMakerUp} MakerDown:${isMakerDown} | TakerUp:${isTakerUp} TakerDown:${isTakerDown}`);
                    logger.info(`DeltaUp:${targetUpBid - resPriceUp} DeltaDown:${targetDownBid - resPriceDown}`);
                }
            }
        }

        // 3. 执行 & 模拟
        if (action) {
            await this.executeTrade(action, remaining);
        }

        // 4. 定时日志 (每1秒)
        if (ts - this.priceLogTs >= 1000) {
            this.priceLogTs = ts;
            const logNetInv = this.inventory.UP - this.inventory.DOWN;
            logger.info(`[${new Date(parseInt(marketUp?.timestamp || 0)).toISOString()}][Delay:${new Date().getTime()-(marketUp?.timestamp || 0)}ms] UpBid:${marketUp.bid} DownBid:${marketDown.bid} UpAsk:${marketUp.ask} DownAsk:${marketDown.ask} | PriceUp:${resPriceUp} PriceDown:${resPriceDown}`);
            logger.info(`[Rem:${remaining.toFixed(0)}s] Fut:${fut} Poly:${this.chainlinkPrice} Strike:${this.marketInfo.strikePrice} Delta:${delta} | FairUP:${MathUtils.round(finalFairUp)} Skew:${skew} NetInv:${logNetInv} | Mode: ${isForceClose?'FORCE':(isReduceOnly?'REDUCE':'NORMAL')}`);
        }
    }

    /**
     * 模拟下单执行 (实盘请替换为真实 API 调用)
     */
    async executeTrade(action, remaining) {
        // 防止频率过快 (简单限流)
        if (this._lastTradeTs && Date.now() - this._lastTradeTs < 200) return;
        this._lastTradeTs = Date.now();

        const key = `@${action.price}`;
        if(action.type === 'MAKER'){
            // 防止重复挂单
            if(this.makerOrders[action.side][key]){
                // logger.info(`已挂单: ${action.side}${key}`);
                return;
            }
            // 全部撤单
            const hasOrders = Object.keys(this.makerOrders[action.side]);
            if(hasOrders.length > 0){
                logger.info(`❌ 撤单: ${action.side} ${hasOrders.join(' ')}`);
                this.makerOrders[action.side] = {};
            }

            // 重新挂单
            this.makerOrders[action.side][key] = action.size;
        }

        const modeMap = {
            'MAKER': '挂单',
            'TAKER': '吃单',
            'FORCE_CLOSE': '强制纠偏'
        };
        logger.info(`⚡ ${modeMap[action.type]} [${action.type}] Buy ${action.side} @ ${action.price} (Size: ${action.size}) | Rem: ${remaining.toFixed(1)}s`);

        // --- 模拟成交 (更新持仓) ---
        // 实盘中，这一步应该由 WebSocket 的 execution_report 或 fills 消息来驱动
        if(action.type === 'TAKER' || action.type === 'FORCE_CLOSE'){
            if (action.side === 'UP') {
                this.inventory.UP += action.size;
                this.cash -= action.price * action.size;
            } else {
                this.inventory.DOWN += action.size;
                this.cash -= action.price * action.size;
            }

            // 打印当前持仓快照
            const net = this.inventory.UP - this.inventory.DOWN;
            logger.info(`📊 Inv: UP:${this.inventory.UP} DOWN:${this.inventory.DOWN} Net:${net} | Est.Cost: ${Math.abs(this.cash).toFixed(2)}`);
        }
    }

    // 复用原有的 Strike Price 获取
    async fetchStrikePrice(startIso, endIso) {
        try {
            const payload = {
                url: CONFIG.API.PRICE_FEED,
                method: "GET",
                params: { symbol: "BTC", eventStartTime: startIso, variant: "fifteen", endDate: endIso }
            };
            const res = await axios.post(this.proxyUrl, payload);
            if (res.data && res.data.openPrice) return parseFloat(res.data.openPrice);
            if(res.data && res.data.error){
                logger.error('fetchStrikePrice: ' + res.data.error);
            }
            return null;
        } catch (e) {
            return null;
        }
    }
}

export default new MyBot();