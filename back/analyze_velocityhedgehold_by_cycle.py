#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
按周期分析 velocityhedgehold 策略的交易日志
生成明细的下单表格统计
"""

import re
import json
import csv
from collections import defaultdict
from datetime import datetime
from pathlib import Path
from typing import List, Dict, Optional

class Trade:
    def __init__(self):
        self.timestamp = None
        self.cycle_id = None
        self.market = None
        self.trigger_direction = None
        self.trigger_velocity = None
        self.trigger_delta_cents = None
        self.trigger_delta_seconds = None
        self.entry_order_id = None
        self.entry_side = None
        self.entry_price_cents = None
        self.entry_filled_size = None
        self.hedge_order_id = None
        self.hedge_side = None
        self.hedge_price_cents = None
        self.hedge_size = None
        self.hedge_completed = False
        self.hedge_completed_at = None
        self.stop_loss = False
        self.stop_loss_reason = None
        self.stop_loss_at = None
        self.trades_count = None
        self.max_trades = None

class Cycle:
    def __init__(self, cycle_id: str, market: str, start_time: str):
        self.cycle_id = cycle_id
        self.market = market
        self.start_time = start_time
        self.trades: List[Trade] = []
        self.end_time = None
    
    @property
    def total_trades(self) -> int:
        return len(self.trades)
    
    @property
    def completed_hedges(self) -> int:
        return sum(1 for t in self.trades if t.hedge_completed)
    
    @property
    def stop_loss_count(self) -> int:
        return sum(1 for t in self.trades if t.stop_loss)
    
    @property
    def hedge_success_rate(self) -> float:
        if self.total_trades == 0:
            return 0.0
        return (self.completed_hedges / self.total_trades) * 100
    
    @property
    def total_entry_notional(self) -> float:
        """总 Entry 成交金额（USDC）"""
        return sum(t.entry_filled_size * t.entry_price_cents / 100 for t in self.trades)
    
    @property
    def total_hedge_notional(self) -> float:
        """总 Hedge 订单金额（USDC）"""
        return sum(t.hedge_size * t.hedge_price_cents / 100 for t in self.trades if t.hedge_size)

def parse_log_line(line: str) -> Optional[Dict]:
    """解析日志行，提取关键信息"""
    # 移除 ANSI 颜色代码
    line_clean = re.sub(r'\x1b\[[0-9;]*m', '', line)
    
    # 提取时间戳（支持多种格式）
    time_match = re.search(r'\[(\d{2}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]', line_clean)
    if not time_match:
        # 尝试另一种格式：INFO[25-12-30 20:31:50]
        time_match = re.search(r'INFO\[(\d{2}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]', line_clean)
    timestamp = time_match.group(1) if time_match else None
    
    # 只处理 velocityhedgehold 相关日志
    if 'velocityhedgehold' not in line_clean.lower():
        return None
    
    result = {
        'timestamp': timestamp,
        'line': line_clean.strip(),
        'type': None,
        'data': {}
    }
    
    # 周期切换
    if '周期切换：交易计数器已重置' in line_clean:
        match = re.search(r'tradesCount=(\d+)\s+maxTradesPerCycle=(\d+)', line_clean)
        if match:
            result['type'] = 'cycle_reset'
            result['data'] = {
                'trades_count': int(match.group(1)),
                'max_trades': int(match.group(2))
            }
    
    # 触发条件满足
    elif '触发条件满足' in line_clean:
        match = re.search(r'winner=(\w+)\s+vel=([\d.]+)\(c/s\)\s+delta=([\d.]+)c/([\d.]+)s', line_clean)
        if match:
            result['type'] = 'trigger'
            result['data'] = {
                'winner': match.group(1),
                'velocity': float(match.group(2)),
                'delta_cents': float(match.group(3)),
                'delta_seconds': float(match.group(4))
            }
    
    # Entry 订单已提交
    elif 'Entry 订单已提交' in line_clean:
        match = re.search(r'orderID=([^\s]+)\s+side=(\w+)\s+price=(\d+)c\s+size=([\d.]+)', line_clean)
        if match:
            result['type'] = 'entry_submitted'
            result['data'] = {
                'order_id': match.group(1),
                'side': match.group(2),
                'price_cents': int(match.group(3)),
                'size': float(match.group(4))
            }
    
    # Entry 已成交并已挂 Hedge
    elif 'Entry 已成交并已挂 Hedge' in line_clean:
        match = re.search(r'entryID=([^\s]+)\s+filled=([\d.]+)@(\d+)c\s+hedgeID=([^\s]+)\s+limit=(\d+)c\s+unhedgedMax=(\d+)s\s+sl=(\d+)c\s+tradesCount=(\d+)/(\d+)', line_clean)
        if match:
            # 尝试提取市场信息
            market_match = re.search(r'market=([^\s]+)', line_clean)
            market = market_match.group(1) if market_match else None
            
            result['type'] = 'entry_filled_hedge_placed'
            result['data'] = {
                'entry_id': match.group(1),
                'entry_filled_size': float(match.group(2)),
                'entry_price_cents': int(match.group(3)),
                'hedge_id': match.group(4),
                'hedge_limit_cents': int(match.group(5)),
                'unhedged_max_seconds': int(match.group(6)),
                'stop_loss_cents': int(match.group(7)),
                'trades_count': int(match.group(8)),
                'max_trades': int(match.group(9)),
                'market': market
            }
    
    # Hedge 订单已提交
    elif 'Hedge 订单已提交' in line_clean:
        match = re.search(r'orderID=([^\s]+)\s+side=(\w+)\s+price=(\d+)c\s+size=([\d.]+)\s+entryOrderID=([^\s]+)\s+market=([^\s]+)', line_clean)
        if match:
            result['type'] = 'hedge_submitted'
            result['data'] = {
                'hedge_id': match.group(1),
                'side': match.group(2),
                'price_cents': int(match.group(3)),
                'size': float(match.group(4)),
                'entry_id': match.group(5),
                'market': match.group(6)
            }
    
    # 监控结束：仓位已对冲
    elif '监控结束：仓位已对冲' in line_clean:
        match = re.search(r'up=([\d.]+)\s+down=([\d.]+)\s+market=([^\s]+)', line_clean)
        if match:
            result['type'] = 'hedge_completed'
            result['data'] = {
                'up_size': float(match.group(1)),
                'down_size': float(match.group(2)),
                'market': match.group(3)
            }
    
    # Hedge 已完成（按订单成交判断）
    elif 'Hedge 已完成' in line_clean:
        match = re.search(r'entryFilled=([\d.]+)\s+hedgeFilled=([\d.]+)\s+entryOrderID=([^\s]+)\s+hedgeOrderID=([^\s]+)', line_clean)
        if match:
            result['type'] = 'hedge_filled'
            result['data'] = {
                'entry_filled': float(match.group(1)),
                'hedge_filled': float(match.group(2)),
                'entry_id': match.group(3),
                'hedge_id': match.group(4)
            }
    
    # 止损触发
    elif '未对冲止损触发' in line_clean or '止损触发' in line_clean:
        match = re.search(r'(价格|超时|hedge_min_notional_would_oversize|hedge_refused_by_failsafe|hedge_place_failed|unhedged_remaining_too_small|unhedged_remaining_notional_too_small|unhedged_remaining_precision_too_small).*?entryOrderID=([^\s]+)(?:\s+hedgeOrderID=([^\s]+))?', line_clean)
        if match:
            result['type'] = 'stop_loss'
            result['data'] = {
                'reason': match.group(1),
                'entry_id': match.group(2),
                'hedge_id': match.group(3) if len(match.groups()) > 2 and match.group(3) else None
            }
    
    return result if result['type'] else None

def analyze_logs(log_files: List[Path]) -> Dict[str, Cycle]:
    """分析日志文件，按周期分组"""
    cycles: Dict[str, Cycle] = {}
    current_cycle_id = None
    current_market = None
    trades_by_entry_id: Dict[str, Trade] = {}
    entry_side_by_order_id: Dict[str, str] = {}  # 存储 Entry 订单的 side
    entry_prepare_side: str = None  # 临时存储准备触发的 side
    
    for log_file in log_files:
        print(f"正在分析: {log_file}", end='')
        # 尝试从文件名提取周期ID和市场名称
        filename_match = re.search(r'btc-updown-15m-(\d+)', str(log_file))
        file_cycle_id = filename_match.group(1) if filename_match else None
        file_market = f"btc-updown-15m-{file_cycle_id}" if file_cycle_id else None
        
        entry_count = 0
        hedge_count = 0
        with open(log_file, 'r', encoding='utf-8') as f:
            for line_num, line in enumerate(f, 1):
                parsed = parse_log_line(line)
                if not parsed:
                    continue
                
                # 如果从文件名提取到了周期ID，使用它
                if file_cycle_id and not current_cycle_id:
                    current_cycle_id = file_cycle_id
                    current_market = file_market
                    if current_cycle_id not in cycles:
                        cycles[current_cycle_id] = Cycle(
                            cycle_id=current_cycle_id,
                            market=current_market,
                            start_time='N/A'
                        )
                
                # 统计解析到的 Entry 数量（用于进度显示）
                if parsed['type'] == 'entry_filled_hedge_placed':
                    entry_count += 1
                
                # 周期切换
                if parsed['type'] == 'cycle_reset':
                    # 周期切换时，市场信息可能还未设置，先记录周期切换事件
                    # 市场信息会在后续的 Entry/Hedge 日志中获取
                    pass
                
                # Entry 订单已提交（必须在 entry_filled_hedge_placed 之前处理）
                elif parsed['type'] == 'entry_submitted':
                    # 更新 Entry 订单的 side 信息
                    entry_id = parsed['data']['order_id']
                    side = parsed['data']['side']
                    entry_side_by_order_id[entry_id] = side
                    # 同时更新 entry_prepare_side，以便后续匹配
                    entry_prepare_side = side
                
                # 触发条件满足
                elif parsed['type'] == 'trigger':
                    # 创建新交易记录
                    trade = Trade()
                    trade.timestamp = parsed['timestamp']
                    trade.cycle_id = current_cycle_id
                    trade.market = current_market
                    trade.trigger_direction = parsed['data']['winner']
                    trade.trigger_velocity = parsed['data']['velocity']
                    trade.trigger_delta_cents = parsed['data']['delta_cents']
                    trade.trigger_delta_seconds = parsed['data']['delta_seconds']
                    # 暂时不添加到周期，等 Entry 成交后再添加
                
                # Entry 已成交并已挂 Hedge
                elif parsed['type'] == 'entry_filled_hedge_placed':
                    # 从解析结果中获取市场信息
                    market_name = parsed['data'].get('market')
                    if not market_name:
                        # 如果解析结果中没有，从日志行中提取
                        market_match = re.search(r'market=([^\s]+)', parsed['line'])
                        if market_match:
                            market_name = market_match.group(1)
                    
                    if market_name:
                        # 从市场名称提取周期ID（例如：btc-updown-15m-1767097800）
                        cycle_match = re.search(r'btc-updown-15m-(\d+)', market_name)
                        if cycle_match:
                            current_cycle_id = cycle_match.group(1)
                            current_market = market_name
                            
                            # 创建周期（如果不存在）
                            if current_cycle_id not in cycles:
                                cycles[current_cycle_id] = Cycle(
                                    cycle_id=current_cycle_id,
                                    market=current_market,
                                    start_time=parsed['timestamp'] or 'N/A'
                                )
                    
                    trade = Trade()
                    trade.timestamp = parsed['timestamp']
                    trade.cycle_id = current_cycle_id
                    trade.market = current_market
                    trade.entry_order_id = parsed['data']['entry_id']
                    trade.entry_filled_size = parsed['data']['entry_filled_size']
                    trade.entry_price_cents = parsed['data']['entry_price_cents']
                    trade.hedge_order_id = parsed['data']['hedge_id']
                    trade.hedge_price_cents = parsed['data']['hedge_limit_cents']
                    trade.trades_count = parsed['data']['trades_count']
                    trade.max_trades = parsed['data']['max_trades']
                    
                    # 从 Entry 订单已提交日志中提取 side（通过 order_id 匹配）
                    entry_id = parsed['data']['entry_id']
                    if entry_id in entry_side_by_order_id:
                        trade.entry_side = entry_side_by_order_id[entry_id]
                        trade.trigger_direction = entry_side_by_order_id[entry_id]
                    elif entry_prepare_side:
                        # 如果找不到，使用最近一次准备触发的 side
                        trade.entry_side = entry_prepare_side
                        trade.trigger_direction = entry_prepare_side
                    # 如果还是找不到，尝试从触发条件满足日志中获取 winner
                    # 但 winner 可能已经在 trigger 事件中设置了
                    
                    # Hedge 的 side 是 Entry 的相反方向
                    if trade.entry_side:
                        trade.hedge_side = 'down' if trade.entry_side.lower() == 'up' else 'up'
                    
                    trades_by_entry_id[trade.entry_order_id] = trade
                    
                    # 添加到当前周期
                    if current_cycle_id and current_cycle_id in cycles:
                        cycles[current_cycle_id].trades.append(trade)
                
                # Hedge 订单已提交（更新 hedge 信息）
                elif parsed['type'] == 'hedge_submitted':
                    hedge_count += 1
                    entry_id = parsed['data']['entry_id']
                    if entry_id in trades_by_entry_id:
                        trade = trades_by_entry_id[entry_id]
                        trade.hedge_order_id = parsed['data']['hedge_id']
                        trade.hedge_side = parsed['data']['side']
                        trade.hedge_price_cents = parsed['data']['price_cents']
                        trade.hedge_size = parsed['data']['size']
                        trade.market = parsed['data']['market']
                        if not current_market:
                            current_market = trade.market
                        # 更新周期ID
                        if trade.market:
                            cycle_match = re.search(r'btc-updown-15m-(\d+)', trade.market)
                            if cycle_match:
                                trade.cycle_id = cycle_match.group(1)
                                current_cycle_id = trade.cycle_id
                
                # 对冲完成（按持仓判断）
                elif parsed['type'] == 'hedge_completed':
                    # 找到对应的交易（通过最新的未完成的交易）
                    if current_cycle_id and current_cycle_id in cycles:
                        for trade in reversed(cycles[current_cycle_id].trades):
                            if not trade.hedge_completed and not trade.stop_loss:
                                trade.hedge_completed = True
                                trade.hedge_completed_at = parsed['timestamp']
                                break
                
                # 对冲完成（按订单成交判断）
                elif parsed['type'] == 'hedge_filled':
                    entry_id = parsed['data']['entry_id']
                    if entry_id in trades_by_entry_id:
                        trade = trades_by_entry_id[entry_id]
                        trade.hedge_completed = True
                        trade.hedge_completed_at = parsed['timestamp']
                
                # 止损触发
                elif parsed['type'] == 'stop_loss':
                    entry_id = parsed['data']['entry_id']
                    if entry_id in trades_by_entry_id:
                        trade = trades_by_entry_id[entry_id]
                        trade.stop_loss = True
                        trade.stop_loss_reason = parsed['data']['reason']
                        trade.stop_loss_at = parsed['timestamp']
        
        # 打印解析统计
        if entry_count > 0 or hedge_count > 0:
            print(f" -> 解析到 {entry_count} 个 Entry, {hedge_count} 个 Hedge")
        else:
            print()
    
    return cycles

def print_cycle_summary(cycles: Dict[str, Cycle]):
    """打印周期汇总统计"""
    print(f"\n{'='*120}")
    print("周期汇总统计")
    print(f"{'='*120}\n")
    
    total_trades = sum(c.total_trades for c in cycles.values())
    total_completed = sum(c.completed_hedges for c in cycles.values())
    total_stop_loss = sum(c.stop_loss_count for c in cycles.values())
    
    print(f"总周期数: {len(cycles)}")
    print(f"总交易次数: {total_trades}")
    print(f"总对冲成功: {total_completed}")
    print(f"总止损次数: {total_stop_loss}")
    if total_trades > 0:
        print(f"整体对冲成功率: {total_completed/total_trades*100:.2f}%")
    
    print(f"\n{'='*120}")
    print("各周期统计")
    print(f"{'='*120}\n")
    
    print(f"{'周期ID':<15} {'市场':<30} {'交易数':<8} {'对冲成功':<10} {'止损':<8} {'成功率':<10} {'Entry金额':<12} {'Hedge金额':<12}")
    print("-" * 120)
    
    for cycle_id in sorted(cycles.keys()):
        cycle = cycles[cycle_id]
        print(f"{cycle_id:<15} {cycle.market[:30]:<30} {cycle.total_trades:<8} "
              f"{cycle.completed_hedges:<10} {cycle.stop_loss_count:<8} "
              f"{cycle.hedge_success_rate:>6.2f}%  "
              f"{cycle.total_entry_notional:>10.2f}  {cycle.total_hedge_notional:>10.2f}")

def print_trade_details(cycles: Dict[str, Cycle]):
    """打印详细的交易明细表格"""
    print(f"\n{'='*120}")
    print("详细交易明细表")
    print(f"{'='*120}\n")
    
    for cycle_id in sorted(cycles.keys()):
        cycle = cycles[cycle_id]
        if cycle.total_trades == 0:
            continue
        
        print(f"\n周期 {cycle_id} ({cycle.market})")
        print(f"开始时间: {cycle.start_time}")
        print("-" * 120)
        print(f"{'序号':<5} {'时间':<20} {'方向':<6} {'Entry':<25} {'Hedge':<25} {'状态':<15} {'金额(USDC)':<12}")
        print("-" * 120)
        
        for i, trade in enumerate(cycle.trades, 1):
            entry_info = f"{trade.entry_filled_size:.2f}@{trade.entry_price_cents}c"
            if trade.entry_order_id:
                entry_info += f"\nID:{trade.entry_order_id[-8:]}"
            
            hedge_info = "N/A"
            if trade.hedge_order_id:
                hedge_info = f"{trade.hedge_size or 0:.2f}@{trade.hedge_price_cents}c"
                hedge_info += f"\nID:{trade.hedge_order_id[-8:]}"
            
            status = "✅对冲完成" if trade.hedge_completed else ("❌止损" if trade.stop_loss else "⏳进行中")
            if trade.stop_loss:
                status += f"\n原因:{trade.stop_loss_reason[:20]}"
            
            entry_notional = trade.entry_filled_size * trade.entry_price_cents / 100
            hedge_notional = (trade.hedge_size or 0) * (trade.hedge_price_cents or 0) / 100
            
            print(f"{i:<5} {trade.timestamp or 'N/A':<20} "
                  f"{trade.trigger_direction or 'N/A':<6} "
                  f"{entry_info:<25} {hedge_info:<25} {status:<15} "
                  f"E:{entry_notional:.2f}\nH:{hedge_notional:.2f}")
            print("-" * 120)

def export_to_csv(cycles: Dict[str, Cycle], csv_file: Path):
    """导出订单明细到 CSV 文件"""
    with open(csv_file, 'w', newline='', encoding='utf-8-sig') as f:
        writer = csv.writer(f)
        
        # 写入表头
        writer.writerow([
            '周期ID',
            '市场',
            '交易序号',
            '时间',
            'Entry代币',
            'Entry方向',
            'Entry订单ID',
            'Entry成交数量',
            'Entry成交价格(cents)',
            'Entry成交金额(USDC)',
            'Hedge代币',
            'Hedge方向',
            'Hedge订单ID',
            'Hedge价格(cents)',
            'Hedge数量',
            'Hedge金额(USDC)',
            '对冲状态',
            '对冲完成时间',
            '止损状态',
            '止损原因',
            '止损时间',
            '交易计数',
            '最大交易数'
        ])
        
        # 写入数据
        for cycle_id in sorted(cycles.keys()):
            cycle = cycles[cycle_id]
            for idx, trade in enumerate(cycle.trades, 1):
                entry_notional = trade.entry_filled_size * trade.entry_price_cents / 100 if trade.entry_filled_size and trade.entry_price_cents else 0
                hedge_notional = trade.hedge_size * trade.hedge_price_cents / 100 if trade.hedge_size and trade.hedge_price_cents else 0
                
                hedge_status = '已完成' if trade.hedge_completed else ('止损' if trade.stop_loss else '进行中')
                
                # Entry 代币：UP 或 DOWN
                entry_token = trade.entry_side.upper() if trade.entry_side else ''
                # Hedge 代币：与 Entry 相反
                hedge_token = 'DOWN' if entry_token == 'UP' else ('UP' if entry_token == 'DOWN' else '')
                
                writer.writerow([
                    cycle_id,
                    cycle.market,
                    idx,
                    trade.timestamp or '',
                    entry_token,  # Entry代币
                    trade.entry_side or '',  # Entry方向
                    trade.entry_order_id or '',
                    trade.entry_filled_size or 0,
                    trade.entry_price_cents or 0,
                    round(entry_notional, 2),
                    hedge_token,  # Hedge代币
                    trade.hedge_side or '',  # Hedge方向
                    trade.hedge_order_id or '',
                    trade.hedge_price_cents or 0,
                    trade.hedge_size or 0,
                    round(hedge_notional, 2),
                    hedge_status,
                    trade.hedge_completed_at or '',
                    '是' if trade.stop_loss else '否',
                    trade.stop_loss_reason or '',
                    trade.stop_loss_at or '',
                    trade.trades_count or '',
                    trade.max_trades or ''
                ])

def main():
    """主函数"""
    log_dir = Path('logs')
    
    # 查找所有包含 velocityhedgehold 的日志文件
    log_files = []
    for log_file in log_dir.glob('*.log'):
        try:
            with open(log_file, 'r', encoding='utf-8') as f:
                content = f.read()
                if 'velocityhedgehold' in content.lower():
                    log_files.append(log_file)
        except:
            pass
    
    if not log_files:
        print("未找到包含 velocityhedgehold 策略的日志文件")
        return
    
    # 按修改时间排序，最新的在前
    log_files.sort(key=lambda x: x.stat().st_mtime, reverse=False)
    
    print(f"找到 {len(log_files)} 个日志文件")
    for f in log_files:
        print(f"  - {f}")
    
    # 分析日志
    cycles = analyze_logs(log_files)
    
    # 打印统计
    print_cycle_summary(cycles)
    print_trade_details(cycles)
    
    # 保存到 JSON
    output_file = Path('velocityhedgehold_cycle_analysis.json')
    cycles_data = {}
    for cycle_id, cycle in cycles.items():
        cycles_data[cycle_id] = {
            'cycle_id': cycle.cycle_id,
            'market': cycle.market,
            'start_time': cycle.start_time,
            'end_time': cycle.end_time,
            'total_trades': cycle.total_trades,
            'completed_hedges': cycle.completed_hedges,
            'stop_loss_count': cycle.stop_loss_count,
            'hedge_success_rate': cycle.hedge_success_rate,
            'total_entry_notional': cycle.total_entry_notional,
            'total_hedge_notional': cycle.total_hedge_notional,
            'trades': [
                {
                    'timestamp': t.timestamp,
                    'entry_order_id': t.entry_order_id,
                    'entry_filled_size': t.entry_filled_size,
                    'entry_price_cents': t.entry_price_cents,
                    'hedge_order_id': t.hedge_order_id,
                    'hedge_price_cents': t.hedge_price_cents,
                    'hedge_size': t.hedge_size,
                    'hedge_completed': t.hedge_completed,
                    'stop_loss': t.stop_loss,
                    'stop_loss_reason': t.stop_loss_reason
                }
                for t in cycle.trades
            ]
        }
    
    with open(output_file, 'w', encoding='utf-8') as f:
        json.dump(cycles_data, f, indent=2, ensure_ascii=False)
    
    print(f"\n详细分析结果已保存到: {output_file}")
    
    # 导出 CSV
    csv_file = Path('velocityhedgehold_order_details.csv')
    export_to_csv(cycles, csv_file)
    print(f"订单明细 CSV 已保存到: {csv_file}")

if __name__ == '__main__':
    main()
