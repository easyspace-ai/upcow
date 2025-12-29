# bot_v5.1.js 算法深度分析

## 一、核心算法架构

### 1.1 整体设计思路

这是一个**自适应做市策略**，结合了：
- **定价模型**：基于Delta和时间的概率模型
- **市场跟随**：70%权重跟随市场盘口
- **库存管理**：通过库存偏斜动态调整报价
- **时间衰减**：临近结算时提高利润要求
- **风险控制**：分级熔断机制

## 二、值得学习的核心算法

### 2.1 自适应定价模型（Adaptive Center）

**位置**：第195-224行

```javascript
// 1. 使用合约价格计算 Delta (反应更快)
const delta = fut - this.marketInfo.strikePrice;

// 2. 时间因子：sqrt(remaining)
const timeFactor = Math.sqrt(Math.max(1, remaining));
const rawX = delta / timeFactor;

// 3. 模型概率：使用正态分布CDF
const z = this.k * rawX + this.c;
const modelFairUp = MathUtils.normCdf(z);

// 4. 市场中枢 (Mid Price)
let marketMidUp = modelFairUp;
if (marketUp.bid > 0 && marketUp.ask > 0) {
    marketMidUp = (marketUp.bid + marketUp.ask) / 2;
}

// 5. 融合概率 (70% 市场权重)
const finalFairUp = (1 - this.marketWeight) * modelFairUp + this.marketWeight * marketMidUp;
```

**学习要点**：

1. **Delta计算**：使用期货价格而非现货，反应更快
   ```javascript
   const delta = fut - this.marketInfo.strikePrice;
   ```
   - 期货价格通常比现货更敏感
   - 能更快捕捉市场变化

2. **时间衰减因子**：使用 `sqrt(remaining)` 而非线性
   ```javascript
   const timeFactor = Math.sqrt(Math.max(1, remaining));
   const rawX = delta / timeFactor;
   ```
   - **为什么用sqrt？**：时间越短，价格波动的影响越大
   - 平方根衰减比线性衰减更平滑，避免极端值

3. **模型+市场融合**：70%权重跟随市场
   ```javascript
   const finalFairUp = (1 - this.marketWeight) * modelFairUp + this.marketWeight * marketMidUp;
   ```
   - **优势**：既相信模型，又尊重市场
   - **参数调优**：`marketWeight = 0.7` 是经验值，可根据回测调整

### 2.2 库存偏斜机制（Inventory Skew）

**位置**：第217-224行

```javascript
// 库存偏斜 (Skew)
const netInv = this.inventory.UP - this.inventory.DOWN;
const skew = netInv * this.inventorySkewFactor;

// 基于库存偏斜动态调整报价
const resPriceUp = finalFairUp - skew;
const resPriceDown = finalFairDown + skew;
```

**学习要点**：

1. **库存偏斜公式**：
   ```javascript
   skew = netInv * inventorySkewFactor
   // inventorySkewFactor = 0.005 / 100 = 0.00005
   ```
   - **作用**：如果持有太多UP（netInv > 0），降低UP的买入价
   - **目的**：自动平衡持仓，避免单边风险

2. **动态报价调整**：
   ```javascript
   resPriceUp = finalFairUp - skew    // UP价格降低
   resPriceDown = finalFairDown + skew // DOWN价格提高
   ```
   - **逻辑**：持仓多的一方，降低买入价（不想再买）
   - **效果**：自然引导买入较少的一方

3. **参数设置**：
   ```javascript
   this.inventorySkewFactor = 0.005 / 100; // 0.00005
   ```
   - **为什么这么小？**：避免过度调整，保持策略稳定性
   - **调优建议**：根据持仓规模动态调整

### 2.3 时间衰减机制（Time Decay）

**位置**：第115-126行，第227-229行

```javascript
// 分级熔断参数
this.decayStartTime = 300;      // 剩余300秒开始衰减
this.reduceOnlyTime = 300;      // 剩余300秒只减不加
this.forceCloseTime = 180;      // 剩余180秒强制平仓
this.maxEdgeAtZero = 0.02;      // 结束时要求的额外利润门槛

// 动态Maker Edge计算
getDynamicMakerEdge(remaining) {
    if (remaining > this.decayStartTime) {
        return this.baseMinEdgeMaker;  // 0.05%
    } else {
        const progress = (this.decayStartTime - remaining) / this.decayStartTime;
        const p = Math.max(0.0, Math.min(1.0, progress));
        return this.baseMinEdgeMaker + p * (this.maxEdgeAtZero - this.baseMinEdgeMaker);
        // 从 0.05% 线性增长到 2%
    }
}
```

**学习要点**：

1. **三阶段设计**：
   - **0-5分钟（剩余300s+）**：正常交易，Maker Edge = 0.05%
   - **5-10分钟（剩余180-300s）**：只减不加（reduceOnly）
   - **最后3分钟（剩余<180s）**：强制平仓（forceClose）

2. **动态利润要求**：
   ```javascript
   // 剩余时间越长，利润要求越低
   // 剩余时间越短，利润要求越高（最高2%）
   ```
   - **原理**：时间越短，不确定性越大，需要更高利润补偿
   - **实现**：线性增长，从0.05%到2%

3. **风险控制**：
   - **reduceOnly**：避免在最后阶段增加风险
   - **forceClose**：强制平仓，避免结算风险

### 2.4 Maker/Taker混合策略

**位置**：第293-327行

```javascript
// UP 方向
if (allowTradeUp && netInv < this.stopQuoteThreshold) {
    // 1. Taker (主动吃单): 市场卖价极低，直接买入
    if (marketUp.ask > 0 && marketUp.ask < resPriceUp - this.baseMinEdgeTaker) {
        action = { type: 'TAKER', side: 'UP', price: marketUp.ask, size: this.sizePerTrade };
    }
    // 2. Maker (被动挂单): 市场卖价不够低，但我愿意挂个买单等别人卖给我
    else {
        const targetUpBid = marketUp.bid + 0.001; // 压价一档
        if (targetUpBid < resPriceUp - currentMakerEdge) {
            action = { type: 'MAKER', side: 'UP', price: targetUpBid, size: this.sizePerTrade };
        }
    }
}
```

**学习要点**：

1. **Taker策略**（主动吃单）：
   ```javascript
   if (marketUp.ask < resPriceUp - baseMinEdgeTaker) {
       // 市场卖价 < 我的保留价格 - 0.3%
       // 直接买入，立即成交
   }
   ```
   - **触发条件**：市场卖价足够低（低于保留价格0.3%）
   - **优势**：立即成交，不等待
   - **成本**：支付maker费用（但价格足够低）

2. **Maker策略**（被动挂单）：
   ```javascript
   const targetUpBid = marketUp.bid + 0.001; // 压价一档
   if (targetUpBid < resPriceUp - currentMakerEdge) {
       // 挂买单，等待别人卖给我
   }
   ```
   - **挂单价**：市场买一价 + 0.001（成为最优买价）
   - **利润要求**：必须满足Maker Edge（动态调整）
   - **优势**：获得maker费用，成本更低

3. **优先级**：Taker优先，Maker备选
   - **逻辑**：如果价格足够好，立即成交；否则挂单等待

### 2.5 风险控制机制

**位置**：第238-275行

```javascript
// [A] 强制平仓 (Force Close) - 最高优先级
if (isForceClose) {
    if (Math.abs(netInv) >= 5) {
        if (netInv > 0) {
            // 持有净多头，买入DOWN对冲
            action = { type: 'FORCE_CLOSE', side: 'DOWN', ... };
        } else {
            // 持有净空头，买入UP对冲
            action = { type: 'FORCE_CLOSE', side: 'UP', ... };
        }
    }
}

// [B] 风控对冲
if (netInv > this.hedgeThreshold) forceActionSide = 'DOWN';
else if (netInv < -this.hedgeThreshold) forceActionSide = 'UP';

if (forceActionSide) {
    // Taker Hedge：通过Taker纠偏
    if (targetBook.ask < fairPrice + 0.03) {
        action = { type: 'TAKER_HEDGE', side: forceActionSide, ... };
    }
}
```

**学习要点**：

1. **分级风控**：
   - **Level 1**：净持仓 > 80，触发对冲（hedgeThreshold）
   - **Level 2**：剩余时间 < 180s，强制平仓（forceClose）
   - **Level 3**：净持仓 > 5，在强制平仓阶段必须平仓

2. **对冲策略**：
   ```javascript
   // 如果持有太多UP（netInv > 80），买入DOWN对冲
   // 使用Taker方式，快速成交
   // 价格要求：不超过fairPrice + 0.03（3%）
   ```

3. **参数设置**：
   ```javascript
   this.hedgeThreshold = 80;           // 净持仓阈值
   this.hedgeSizeMultiplier = 1.5;     // 对冲订单大小 = 基础大小 * 1.5
   ```

### 2.6 Maker订单管理

**位置**：第154-193行，第364-379行

```javascript
// 判定Maker成交
Object.keys(this.makerOrders.UP).forEach(priceKey => {
    const orderPrice = parseFloat(priceKey.replace('@', ''));
    const orderSize = this.makerOrders.UP[priceKey];
    // 判定条件：市场的卖一价 (Ask) <= 我的买单价
    if (marketUp.ask > 0 && marketUp.ask <= orderPrice) {
        // 成交逻辑
        this.inventory.UP += orderSize;
        this.cash -= orderPrice * orderSize;
        delete this.makerOrders.UP[priceKey];
    }
});

// 挂单逻辑
if(action.type === 'MAKER'){
    // 防止重复挂单
    if(this.makerOrders[action.side][key]){
        return;
    }
    // 全部撤单
    const hasOrders = Object.keys(this.makerOrders[action.side]);
    if(hasOrders.length > 0){
        this.makerOrders[action.side] = {};
    }
    // 重新挂单
    this.makerOrders[action.side][key] = action.size;
}
```

**学习要点**：

1. **Maker成交判定**：
   ```javascript
   // 如果市场卖一价 <= 我的买单价，说明我的买单被成交了
   if (marketUp.ask <= orderPrice) {
       // 成交
   }
   ```
   - **逻辑**：不需要等待订单确认，通过价格判断
   - **优势**：实时性更好，减少延迟

2. **订单管理**：
   - **防重复**：检查是否已挂单
   - **撤单策略**：挂新单前，先撤掉所有旧单
   - **原因**：价格变化快，旧单可能已经不合适

3. **模拟vs实盘**：
   - **模拟**：通过价格判断成交
   - **实盘**：应该通过WebSocket的execution_report确认

## 三、算法设计亮点总结

### 3.1 多维度融合

1. **模型 + 市场**：70%市场权重，30%模型权重
2. **Maker + Taker**：根据价格优势选择策略
3. **时间 + 风险**：时间衰减 + 分级熔断

### 3.2 自适应调整

1. **动态Maker Edge**：根据剩余时间调整利润要求
2. **库存偏斜**：根据持仓自动调整报价
3. **风险对冲**：根据净持仓自动对冲

### 3.3 风险控制

1. **分级熔断**：三阶段风险控制
2. **持仓限制**：stopQuoteThreshold限制单边持仓
3. **强制平仓**：最后阶段强制平仓

### 3.4 执行优化

1. **Taker优先**：价格好时立即成交
2. **Maker备选**：价格不够好时挂单等待
3. **订单管理**：防止重复挂单，及时撤单

## 四、可以改进的地方

### 4.1 参数调优

1. **marketWeight**：可以动态调整，而非固定0.7
2. **inventorySkewFactor**：可以根据持仓规模动态调整
3. **hedgeThreshold**：可以根据市场波动率调整

### 4.2 策略优化

1. **订单大小**：可以根据价格和持仓动态调整
2. **Maker Edge**：可以使用更复杂的衰减函数
3. **对冲策略**：可以使用更精细的对冲算法

### 4.3 实现优化

1. **订单确认**：实盘应该通过WebSocket确认成交
2. **错误处理**：增加更多的错误处理和重试机制
3. **监控指标**：增加更多的监控和统计指标

## 五、学习建议

### 5.1 核心算法

1. **定价模型**：理解Delta、时间因子、正态分布CDF
2. **库存管理**：理解库存偏斜机制
3. **时间衰减**：理解动态利润要求

### 5.2 策略设计

1. **多策略融合**：Maker + Taker混合策略
2. **风险控制**：分级熔断机制
3. **自适应调整**：根据市场状态动态调整

### 5.3 实现技巧

1. **订单管理**：防止重复挂单，及时撤单
2. **成交判定**：通过价格判断Maker成交
3. **状态管理**：清晰的状态管理和日志记录

## 六、总结

这个算法最值得学习的地方：

1. ✅ **自适应定价**：模型+市场融合，动态调整
2. ✅ **库存管理**：通过库存偏斜自动平衡持仓
3. ✅ **时间衰减**：根据剩余时间动态调整策略
4. ✅ **风险控制**：分级熔断，多层级保护
5. ✅ **执行优化**：Maker/Taker混合，灵活应对市场

这是一个**成熟、稳健的做市策略**，适合学习和参考！
