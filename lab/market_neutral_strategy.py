"""
Polymarket BTC 15分钟 Up/Down 市场中性套利策略

核心逻辑：
1. 监控UP和DOWN的ask价格
2. 当 UP_ask + DOWN_ask < 1.0 时，同时买入两者
3. 保持UP/DOWN持仓平衡
4. 持有到结算，无论哪方胜出都能盈利
"""

import time
from typing import Optional, Tuple
from dataclasses import dataclass


@dataclass
class MarketData:
    """市场数据"""
    up_ask: float      # UP卖一价
    up_bid: float      # UP买一价
    down_ask: float    # DOWN卖一价
    down_bid: float    # DOWN买一价
    timestamp: float   # 时间戳


@dataclass
class Position:
    """持仓信息"""
    up_shares: float = 0.0      # UP持仓数量
    down_shares: float = 0.0    # DOWN持仓数量
    up_cost: float = 0.0        # UP总成本
    down_cost: float = 0.0      # DOWN总成本
    
    @property
    def total_shares(self) -> float:
        """总持仓数量"""
        return self.up_shares + self.down_shares
    
    @property
    def total_cost(self) -> float:
        """总成本"""
        return self.up_cost + self.down_cost
    
    @property
    def net_position(self) -> float:
        """净持仓 = UP - DOWN"""
        return self.up_shares - self.down_shares
    
    @property
    def imbalance_ratio(self) -> float:
        """持仓不平衡比例"""
        if self.total_shares == 0:
            return 0.0
        return abs(self.net_position) / self.total_shares
    
    def calculate_profit(self) -> Tuple[float, float]:
        """
        计算预期利润
        返回：(如果UP胜出的利润, 如果DOWN胜出的利润)
        """
        profit_if_up = self.up_shares * 1.0 - self.total_cost
        profit_if_down = self.down_shares * 1.0 - self.total_cost
        return profit_if_up, profit_if_down
    
    def min_profit(self) -> float:
        """最小利润（无论哪方胜出）"""
        profit_up, profit_down = self.calculate_profit()
        return min(profit_up, profit_down)


class MarketNeutralArbitrageStrategy:
    """市场中性套利策略"""
    
    def __init__(
        self,
        min_profit_threshold: float = 0.02,      # 最小利润阈值2%
        max_imbalance_ratio: float = 0.2,         # 最大持仓不平衡比例20%
        base_order_size: float = 10.0,            # 基础订单大小
        max_order_size: float = 50.0,             # 最大订单大小
        max_total_position: float = 1000.0,     # 最大总持仓
        max_single_side_position: float = 600.0, # 单边最大持仓
    ):
        self.min_profit_threshold = min_profit_threshold
        self.max_imbalance_ratio = max_imbalance_ratio
        self.base_order_size = base_order_size
        self.max_order_size = max_order_size
        self.max_total_position = max_total_position
        self.max_single_side_position = max_single_side_position
        
        self.position = Position()
        self.trade_history = []
    
    def on_tick(self, market: MarketData) -> Optional[dict]:
        """
        每个Tick执行的核心逻辑
        
        Args:
            market: 市场数据
            
        Returns:
            交易决策字典，包含：
            - action: 'BUY_UP', 'BUY_DOWN', 'BUY_BOTH', None
            - size: 订单大小
            - price: 价格
            - reason: 决策原因
        """
        # 1. 检查是否有套利机会
        total_cost = market.up_ask + market.down_ask
        profit_margin = 1.0 - total_cost
        
        if profit_margin < self.min_profit_threshold:
            return None  # 没有套利机会
        
        # 2. 检查持仓限制
        if self.position.total_shares >= self.max_total_position:
            return None  # 已达到最大持仓
        
        # 3. 计算持仓不平衡度
        imbalance = self.position.imbalance_ratio
        
        # 4. 决策买入方向
        if imbalance > self.max_imbalance_ratio:
            # 持仓不平衡，优先买入较少的一方
            if self.position.up_shares < self.position.down_shares:
                return self._decide_buy_up(market, reason="持仓不平衡，买入UP")
            else:
                return self._decide_buy_down(market, reason="持仓不平衡，买入DOWN")
        else:
            # 持仓平衡，根据价格优势买入
            # 优先买入价格更低的（成本更低）
            if market.up_ask < market.down_ask:
                return self._decide_buy_up(market, reason="价格优势，买入UP")
            else:
                return self._decide_buy_down(market, reason="价格优势，买入DOWN")
    
    def _decide_buy_up(self, market: MarketData, reason: str) -> Optional[dict]:
        """决定买入UP"""
        # 检查单边持仓限制
        if self.position.up_shares >= self.max_single_side_position:
            return None
        
        # 计算订单大小
        size = self._calculate_order_size(market.up_ask, "UP")
        if size <= 0:
            return None
        
        return {
            "action": "BUY_UP",
            "size": size,
            "price": market.up_ask,
            "reason": reason,
        }
    
    def _decide_buy_down(self, market: MarketData, reason: str) -> Optional[dict]:
        """决定买入DOWN"""
        # 检查单边持仓限制
        if self.position.down_shares >= self.max_single_side_position:
            return None
        
        # 计算订单大小
        size = self._calculate_order_size(market.down_ask, "DOWN")
        if size <= 0:
            return None
        
        return {
            "action": "BUY_DOWN",
            "size": size,
            "price": market.down_ask,
            "reason": reason,
        }
    
    def _calculate_order_size(self, price: float, side: str) -> float:
        """
        计算订单大小
        
        考虑因素：
        1. 基础订单大小
        2. 价格越低，可以买入更多
        3. 持仓不平衡时，增加订单大小
        4. 不超过最大订单大小
        """
        # 基础大小
        size = self.base_order_size
        
        # 价格越低，可以买入更多（因为成本更低）
        if price < 0.3:
            size *= 1.5  # 价格很低，增加买入量
        elif price < 0.5:
            size *= 1.2
        
        # 持仓不平衡时，增加订单大小以快速平衡
        if self.position.imbalance_ratio > self.max_imbalance_ratio:
            size *= 1.5
        
        # 限制最大订单大小
        size = min(size, self.max_order_size)
        
        # 确保满足最小订单金额要求（Polymarket要求≥$1）
        min_size = 1.1 / price  # 至少$1.1的订单
        size = max(size, min_size)
        
        return round(size, 2)
    
    def execute_trade(self, decision: dict) -> bool:
        """
        执行交易
        
        Args:
            decision: 交易决策字典
            
        Returns:
            是否执行成功
        """
        if decision is None:
            return False
        
        action = decision["action"]
        size = decision["size"]
        price = decision["price"]
        
        cost = price * size
        
        if action == "BUY_UP":
            self.position.up_shares += size
            self.position.up_cost += cost
        elif action == "BUY_DOWN":
            self.position.down_shares += size
            self.position.down_cost += cost
        else:
            return False
        
        # 记录交易历史
        self.trade_history.append({
            "timestamp": time.time(),
            "action": action,
            "size": size,
            "price": price,
            "cost": cost,
            "reason": decision.get("reason", ""),
            "position": {
                "up_shares": self.position.up_shares,
                "down_shares": self.position.down_shares,
                "net_position": self.position.net_position,
                "total_cost": self.position.total_cost,
            }
        })
        
        return True
    
    def get_status(self) -> dict:
        """获取当前状态"""
        profit_up, profit_down = self.position.calculate_profit()
        min_profit = self.position.min_profit()
        
        return {
            "position": {
                "up_shares": self.position.up_shares,
                "down_shares": self.position.down_shares,
                "net_position": self.position.net_position,
                "total_cost": self.position.total_cost,
                "imbalance_ratio": self.position.imbalance_ratio,
            },
            "profit": {
                "if_up_wins": profit_up,
                "if_down_wins": profit_down,
                "min_profit": min_profit,
            },
            "trades": {
                "total_count": len(self.trade_history),
                "up_count": sum(1 for t in self.trade_history if t["action"] == "BUY_UP"),
                "down_count": sum(1 for t in self.trade_history if t["action"] == "BUY_DOWN"),
            }
        }


# 使用示例
if __name__ == "__main__":
    # 创建策略实例
    strategy = MarketNeutralArbitrageStrategy(
        min_profit_threshold=0.02,      # 2%最小利润
        max_imbalance_ratio=0.2,        # 20%最大不平衡
        base_order_size=10.0,           # 基础订单10 shares
    )
    
    # 模拟市场数据
    market = MarketData(
        up_ask=0.48,
        up_bid=0.47,
        down_ask=0.50,
        down_bid=0.49,
        timestamp=time.time()
    )
    
    # 执行策略
    decision = strategy.on_tick(market)
    if decision:
        print(f"交易决策: {decision}")
        strategy.execute_trade(decision)
        print(f"当前状态: {strategy.get_status()}")
    else:
        print("没有交易机会")
