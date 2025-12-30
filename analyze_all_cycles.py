#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
批量分析所有周期的订单和PnL，为每个周期生成独立的CSV和分析报告
"""

import re
import os
import csv
from datetime import datetime
from collections import defaultdict
from pathlib import Path

def parse_log_line(line):
    """解析日志行，提取时间和消息"""
    time_match = re.search(r'\[(\d{2}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]', line)
    if time_match:
        try:
            time_str = time_match.group(1)
            dt = datetime.strptime(time_str, '%y-%m-%d %H:%M:%S')
            timestamp = int(dt.timestamp())
            return {
                'timestamp': timestamp,
                'time': time_str
            }
        except:
            pass
    return None

def extract_order_from_line(line, parsed):
    """从订单日志中提取订单信息"""
    if not parsed:
        return None
    
    # 尝试多种格式匹配订单记录
    patterns = [
        (r'📝.*纸交易.*模拟下单.*orderID=([^\s,]+).*tokenType=(\w+).*side=(\w+).*price=([\d.]+).*size=([\d.]+)', 'full'),
        (r'模拟下单.*orderID=([^\s,]+).*tokenType=(\w+).*price=([\d.]+).*size=([\d.]+)', 'simple'),
        (r'纸交易.*模拟下单.*orderID=([^\s,]+).*tokenType=(\w+).*price=([\d.]+).*size=([\d.]+)', 'simple2'),
    ]
    
    for pattern, ptype in patterns:
        match = re.search(pattern, line)
        if match:
            if ptype == 'full':
                order_id, token, side, price, size = match.groups()
            else:
                order_id, token, price, size = match.groups()
                side = 'BUY'
            
            token = token.lower()
            direction = 'Up' if token == 'up' else 'Down' if token == 'down' else ''
            outcome_index = 0 if token == 'up' else 1 if token == 'down' else 0
            
            market_match = re.search(r'market=([^\s,]+)', line)
            market = market_match.group(1) if market_match else ''
            
            return {
                'timestamp': parsed['timestamp'],
                'time': parsed['time'],
                'order_id': order_id,
                'action': side.upper(),
                'direction': direction,
                'market': market,
                'price': float(price),
                'size': float(size),
                'usdc_amount': float(price) * float(size),
                'outcome_index': outcome_index,
                'type': 'order'
            }
    
    return None

def extract_position_stats(line, parsed):
    """从日志行中提取持仓统计"""
    if '持仓统计' not in line or not parsed:
        return None
    
    up_cost_match = re.search(r'upCost=([\d.]+)', line)
    down_cost_match = re.search(r'downCost=([\d.]+)', line)
    up_shares_match = re.search(r'upShares=([\d.]+)', line)
    down_shares_match = re.search(r'downShares=([\d.]+)', line)
    worst_pnl_match = re.search(r'worstPnL=([\d.]+)', line)
    unhedged_match = re.search(r'unhedged=([\d.]+)', line)
    remaining_match = re.search(r'remainingSeconds=(\d+)', line)
    market_match = re.search(r'market=([^\s,]+)', line)
    
    if not (up_shares_match and down_shares_match):
        return None
    
    return {
        'timestamp': parsed['timestamp'],
        'time': parsed['time'],
        'market': market_match.group(1) if market_match else '',
        'up_shares': float(up_shares_match.group(1)),
        'down_shares': float(down_shares_match.group(1)),
        'up_cost': float(up_cost_match.group(1)) if up_cost_match else 0,
        'down_cost': float(down_cost_match.group(1)) if down_cost_match else 0,
        'worst_pnl': float(worst_pnl_match.group(1)) if worst_pnl_match else 0,
        'unhedged': float(unhedged_match.group(1)) if unhedged_match else 0,
        'remaining': int(remaining_match.group(1)) if remaining_match else 0
    }

def extract_cycle_id_from_market(market_slug):
    """从market slug中提取周期ID"""
    match = re.search(r'(\d{10})$', market_slug)
    if match:
        return match.group(1)
    return None

def generate_csv_for_cycle(cycle_id, cycle_data, output_dir="lab"):
    """为单个周期生成CSV文件"""
    orders = cycle_data['orders']
    if not orders:
        return None
    
    # 按时间排序
    orders.sort(key=lambda x: x['timestamp'])
    
    # 生成CSV文件名
    csv_filename = f"bot_cyclehedge_cycle_{cycle_id}.csv"
    csv_path = os.path.join(output_dir, csv_filename)
    
    # 写入CSV（使用4位年份格式）
    with open(csv_path, 'w', encoding='utf-8', newline='') as f:
        fieldnames = ['时间戳', '时间', '动作', '方向', '市场', '价格', '数量', 'USDC金额', 'OutcomeIndex']
        writer = csv.DictWriter(f, fieldnames=fieldnames)
        writer.writeheader()
        
        for order in orders:
            # 转换时间为4位年份格式
            dt = datetime.fromtimestamp(order['timestamp'])
            time_str = dt.strftime('%Y-%m-%d %H:%M:%S')
            
            writer.writerow({
                '时间戳': order['timestamp'],
                '时间': time_str,
                '动作': order['action'],
                '方向': order['direction'],
                '市场': order['market'],
                '价格': f"{order['price']:.6f}",
                '数量': f"{order['size']:.1f}",
                'USDC金额': f"{order['usdc_amount']:.6f}",
                'OutcomeIndex': order['outcome_index']
            })
    
    return csv_path

def analyze_cycle_pnl(cycle_id, cycle_data):
    """分析单个周期的PnL"""
    orders = cycle_data['orders']
    position_stats = cycle_data['position_stats']
    
    if not position_stats:
        return None
    
    # 获取最终持仓统计
    last_stat = position_stats[-1]
    first_stat = position_stats[0]
    
    final_up_shares = last_stat['up_shares']
    final_down_shares = last_stat['down_shares']
    final_up_cost = last_stat['up_cost']
    final_down_cost = last_stat['down_cost']
    total_cost = final_up_cost + final_down_cost
    worst_pnl = last_stat['worst_pnl']
    
    # 计算PnL
    pnl_up_win = final_up_shares * 1.0 - total_cost if final_up_shares > 0 else 0
    pnl_down_win = final_down_shares * 1.0 - total_cost if final_down_shares > 0 else 0
    worst_case_pnl = min(pnl_up_win, pnl_down_win) if final_up_shares > 0 and final_down_shares > 0 else 0
    
    # 持仓平衡度
    min_shares = min(final_up_shares, final_down_shares)
    max_shares = max(final_up_shares, final_down_shares)
    balance_ratio = (min_shares / max_shares * 100) if max_shares > 0 else 0
    
    # 平均成本
    up_avg_price = final_up_cost / final_up_shares if final_up_shares > 0 else 0
    down_avg_price = final_down_cost / final_down_shares if final_down_shares > 0 else 0
    
    # 订单统计
    up_orders = [o for o in orders if o['direction'] == 'Up']
    down_orders = [o for o in orders if o['direction'] == 'Down']
    
    up_order_total = sum(o['size'] for o in up_orders)
    down_order_total = sum(o['size'] for o in down_orders)
    up_order_amount = sum(o['usdc_amount'] for o in up_orders)
    down_order_amount = sum(o['usdc_amount'] for o in down_orders)
    
    return {
        'cycle_id': cycle_id,
        'market_slug': cycle_data['market_slug'],
        'log_file': cycle_data.get('log_file', ''),
        'log_create_time': cycle_data.get('log_create_time', ''),
        'orders_count': len(orders),
        'up_orders_count': len(up_orders),
        'down_orders_count': len(down_orders),
        'up_order_total': up_order_total,
        'down_order_total': down_order_total,
        'up_order_amount': up_order_amount,
        'down_order_amount': down_order_amount,
        'first_order_time': orders[0]['time'] if orders else None,
        'last_order_time': orders[-1]['time'] if orders else None,
        'first_stat_time': first_stat['time'],
        'last_stat_time': last_stat['time'],
        'final_up_shares': final_up_shares,
        'final_down_shares': final_down_shares,
        'final_up_cost': final_up_cost,
        'final_down_cost': final_down_cost,
        'total_cost': total_cost,
        'pnl_up_win': pnl_up_win,
        'pnl_down_win': pnl_down_win,
        'worst_case_pnl': worst_case_pnl,
        'worst_pnl_from_stat': worst_pnl,
        'balance_ratio': balance_ratio,
        'up_avg_price': up_avg_price,
        'down_avg_price': down_avg_price,
        'stats_count': len(position_stats)
    }

def generate_analysis_report(cycle_id, cycle_data, pnl_data, output_dir="internal/strategies/cyclehedge"):
    """为单个周期生成分析报告"""
    if not pnl_data:
        return None
    
    report_filename = f"周期分析报告_{cycle_id}.md"
    report_path = os.path.join(output_dir, report_filename)
    
    orders = cycle_data['orders']
    position_stats = cycle_data['position_stats']
    
    with open(report_path, 'w', encoding='utf-8') as f:
        f.write(f"# CycleHedge策略周期分析报告 - {cycle_id}\n\n")
        f.write(f"**周期**: {cycle_data['market_slug']}\n")
        if cycle_data.get('log_file'):
            f.write(f"**日志文件**: {cycle_data['log_file']}\n")
        if cycle_data.get('log_create_time'):
            f.write(f"**日志创建时间**: {cycle_data['log_create_time']}\n")
        f.write(f"**生成时间**: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}\n\n")
        f.write("---\n\n")
        
        f.write("## 📊 执行摘要\n\n")
        f.write("| 指标 | 数值 | 状态 |\n")
        f.write("|------|------|------|\n")
        f.write(f"| **订单数** | {pnl_data['orders_count']}笔 | {'✅' if pnl_data['orders_count'] > 0 else '⚠️'} |\n")
        f.write(f"| **最终UP持仓** | {pnl_data['final_up_shares']:.2f} shares | ✅ |\n")
        f.write(f"| **最终DOWN持仓** | {pnl_data['final_down_shares']:.2f} shares | ✅ |\n")
        f.write(f"| **持仓平衡度** | {pnl_data['balance_ratio']:.2f}% | {'✅✅✅' if pnl_data['balance_ratio'] >= 95 else '✅' if pnl_data['balance_ratio'] >= 90 else '⚠️'} |\n")
        f.write(f"| **总成本** | {pnl_data['total_cost']:.4f} USDC | ✅ |\n")
        f.write(f"| **最坏情况PnL** | {pnl_data['worst_case_pnl']:+.4f} USDC | {'✅✅✅' if pnl_data['worst_case_pnl'] > 0 else '⚠️' if pnl_data['worst_case_pnl'] == 0 else '❌'} |\n")
        if pnl_data['total_cost'] > 0:
            profit_ratio = pnl_data['worst_case_pnl']/pnl_data['total_cost']*100
            f.write(f"| **盈利比例** | {profit_ratio:.2f}% | {'✅✅✅' if profit_ratio > 3 else '✅' if profit_ratio > 0 else '⚠️'} |\n")
        
        f.write("\n---\n\n")
        f.write("## 📋 订单统计\n\n")
        if pnl_data['orders_count'] > 0:
            f.write(f"- **总订单数**: {pnl_data['orders_count']}笔\n")
            f.write(f"- **UP订单**: {pnl_data['up_orders_count']}笔 ({pnl_data['up_order_total']:.2f} shares, {pnl_data['up_order_amount']:.4f} USDC)\n")
            f.write(f"- **DOWN订单**: {pnl_data['down_orders_count']}笔 ({pnl_data['down_order_total']:.2f} shares, {pnl_data['down_order_amount']:.4f} USDC)\n")
            if pnl_data['first_order_time'] and pnl_data['last_order_time']:
                f.write(f"- **订单时间范围**: {pnl_data['first_order_time']} 到 {pnl_data['last_order_time']}\n")
        else:
            f.write("**注意**: 日志文件中未找到订单记录。\n\n")
            f.write("**可能原因**:\n")
            f.write("1. 日志文件创建晚了，早期订单没有记录\n")
            f.write("2. 订单记录在其他日志文件中\n")
            f.write("3. 订单记录格式不同\n\n")
        
        f.write("\n---\n\n")
        f.write("## 📊 持仓分析\n\n")
        f.write(f"- **最终UP持仓**: {pnl_data['final_up_shares']:.2f} shares\n")
        f.write(f"- **最终DOWN持仓**: {pnl_data['final_down_shares']:.2f} shares\n")
        f.write(f"- **总持仓**: {pnl_data['final_up_shares'] + pnl_data['final_down_shares']:.2f} shares\n")
        f.write(f"- **持仓平衡度**: {pnl_data['balance_ratio']:.2f}%\n")
        f.write(f"- **未对冲**: {max_shares - min_shares:.2f} shares\n")
        f.write(f"- **持仓统计记录数**: {len(position_stats)}\n")
        f.write(f"- **首次持仓统计**: {pnl_data['first_stat_time']}\n")
        f.write(f"- **最后持仓统计**: {pnl_data['last_stat_time']}\n")
        
        f.write("\n---\n\n")
        f.write("## 💰 成本分析\n\n")
        f.write(f"- **UP成本**: {pnl_data['final_up_cost']:.4f} USDC\n")
        f.write(f"- **DOWN成本**: {pnl_data['final_down_cost']:.4f} USDC\n")
        f.write(f"- **总成本**: {pnl_data['total_cost']:.4f} USDC\n")
        if pnl_data['final_up_shares'] > 0:
            f.write(f"- **UP平均价格**: {pnl_data['up_avg_price']:.6f} USDC/share ({pnl_data['up_avg_price']*100:.2f}c)\n")
        if pnl_data['final_down_shares'] > 0:
            f.write(f"- **DOWN平均价格**: {pnl_data['down_avg_price']:.6f} USDC/share ({pnl_data['down_avg_price']*100:.2f}c)\n")
        if pnl_data['final_up_shares'] > 0 and pnl_data['final_down_shares'] > 0:
            total_avg = pnl_data['up_avg_price'] + pnl_data['down_avg_price']
            f.write(f"- **平均成本合计**: {total_avg:.6f} USDC/set ({total_avg*100:.2f}c)\n")
            f.write(f"- **锁定利润**: {100 - (total_avg*100):.2f}c per set\n")
        
        f.write("\n---\n\n")
        f.write("## 💰 PnL分析\n\n")
        f.write(f"- **UP胜出PnL**: {pnl_data['pnl_up_win']:+.4f} USDC\n")
        f.write(f"- **DOWN胜出PnL**: {pnl_data['pnl_down_win']:+.4f} USDC\n")
        f.write(f"- **最坏情况PnL**: {pnl_data['worst_case_pnl']:+.4f} USDC\n")
        if pnl_data['total_cost'] > 0:
            f.write(f"- **盈利比例**: {pnl_data['worst_case_pnl']/pnl_data['total_cost']*100:.2f}%\n")
    
    return report_path

def main():
    """主函数：分析所有日志文件"""
    log_dir = "logs"
    output_dir = "lab"
    report_dir = "internal/strategies/cyclehedge"
    
    # 确保输出目录存在
    os.makedirs(output_dir, exist_ok=True)
    os.makedirs(report_dir, exist_ok=True)
    
    # 查找所有日志文件
    log_files = sorted(Path(log_dir).glob("btc-updown-15m-*.log"), key=lambda p: p.stat().st_mtime)
    
    if not log_files:
        print(f"❌ 未找到日志文件在 {log_dir} 目录")
        return
    
    print(f"找到 {len(log_files)} 个日志文件")
    
    # 合并所有周期的数据
    all_cycles = {}
    
    for log_file in log_files:
        print(f"\n分析: {log_file.name}")
        cycles = defaultdict(lambda: {
            'orders': [],
            'position_stats': [],
            'cycle_id': None,
            'market_slug': None,
            'log_file': log_file.name,
            'log_create_time': datetime.fromtimestamp(log_file.stat().st_mtime).strftime('%Y-%m-%d %H:%M:%S')
        })
        
        order_ids_seen = set()
        
        try:
            with open(log_file, 'r', encoding='utf-8', errors='ignore') as f:
                for line_num, line in enumerate(f, 1):
                    parsed = parse_log_line(line)
                    if not parsed:
                        continue
                    
                    # 提取订单
                    order = extract_order_from_line(line, parsed)
                    if order:
                        order_key = f"{order['order_id']}"
                        if order_key not in order_ids_seen:
                            cycle_id = extract_cycle_id_from_market(order['market'])
                            if cycle_id:
                                cycles[cycle_id]['cycle_id'] = cycle_id
                                cycles[cycle_id]['market_slug'] = order['market']
                                cycles[cycle_id]['orders'].append(order)
                                order_ids_seen.add(order_key)
                    
                    # 提取持仓统计
                    position_stat = extract_position_stats(line, parsed)
                    if position_stat:
                        cycle_id = extract_cycle_id_from_market(position_stat['market'])
                        if cycle_id:
                            cycles[cycle_id]['cycle_id'] = cycle_id
                            cycles[cycle_id]['market_slug'] = position_stat['market']
                            cycles[cycle_id]['position_stats'].append(position_stat)
        except Exception as e:
            print(f"  ⚠️  读取文件出错: {e}")
            continue
        
        for cycle_id, cycle_data in cycles.items():
            if cycle_id in all_cycles:
                # 合并订单和持仓统计
                all_cycles[cycle_id]['orders'].extend(cycle_data['orders'])
                all_cycles[cycle_id]['position_stats'].extend(cycle_data['position_stats'])
            else:
                all_cycles[cycle_id] = cycle_data
    
    # 去重订单
    for cycle_id in all_cycles:
        orders = all_cycles[cycle_id]['orders']
        seen_ids = set()
        unique_orders = []
        for order in orders:
            if order['order_id'] not in seen_ids:
                unique_orders.append(order)
                seen_ids.add(order['order_id'])
        all_cycles[cycle_id]['orders'] = unique_orders
    
    print(f"\n{'='*80}")
    print(f"总共找到 {len(all_cycles)} 个周期")
    print(f"{'='*80}")
    
    # 为每个周期生成CSV和分析报告
    summary_data = []
    
    for cycle_id in sorted(all_cycles.keys()):
        cycle_data = all_cycles[cycle_id]
        
        # 生成CSV（如果有订单）
        csv_path = generate_csv_for_cycle(cycle_id, cycle_data, output_dir)
        if csv_path:
            print(f"✅ CSV已生成: {csv_path}")
        
        # 分析PnL
        pnl_data = analyze_cycle_pnl(cycle_id, cycle_data)
        if pnl_data:
            # 生成分析报告
            report_path = generate_analysis_report(cycle_id, cycle_data, pnl_data, report_dir)
            if report_path:
                print(f"✅ 分析报告已生成: {report_path}")
            
            summary_data.append(pnl_data)
    
    # 生成汇总报告
    if summary_data:
        summary_path = os.path.join(report_dir, "所有周期汇总报告.md")
        with open(summary_path, 'w', encoding='utf-8') as f:
            f.write("# CycleHedge策略所有周期汇总报告\n\n")
            f.write(f"**生成时间**: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}\n\n")
            f.write("---\n\n")
            
            f.write("## 📊 汇总统计\n\n")
            f.write("| 周期ID | 订单数 | UP持仓 | DOWN持仓 | 总成本 | 最坏PnL | 盈利比例 | 平衡度 |\n")
            f.write("|--------|--------|--------|----------|--------|---------|----------|--------|\n")
            
            total_orders = 0
            total_cost = 0
            total_pnl = 0
            total_cycles = len(summary_data)
            profitable_cycles = 0
            
            for pnl in summary_data:
                total_orders += pnl['orders_count']
                total_cost += pnl['total_cost']
                total_pnl += pnl['worst_case_pnl']
                if pnl['worst_case_pnl'] > 0:
                    profitable_cycles += 1
                
                profit_ratio = (pnl['worst_case_pnl']/pnl['total_cost']*100) if pnl['total_cost'] > 0 else 0
                
                f.write(f"| {pnl['cycle_id']} | {pnl['orders_count']} | {pnl['final_up_shares']:.2f} | {pnl['final_down_shares']:.2f} | {pnl['total_cost']:.4f} | {pnl['worst_case_pnl']:+.4f} | {profit_ratio:.2f}% | {pnl['balance_ratio']:.2f}% |\n")
            
            f.write(f"\n**总计**: {total_cycles}个周期, {total_orders}笔订单, {total_cost:.4f} USDC总成本, {total_pnl:+.4f} USDC总盈利\n")
            f.write(f"**平均盈利比例**: {(total_pnl/total_cost*100) if total_cost > 0 else 0:.2f}%\n")
            f.write(f"**盈利周期数**: {profitable_cycles}/{total_cycles} ({profitable_cycles/total_cycles*100:.1f}%)\n")
        
        print(f"\n✅ 汇总报告已生成: {summary_path}")
    
    print(f"\n{'='*80}")
    print("✅ 所有周期分析完成！")
    print(f"{'='*80}")

if __name__ == "__main__":
    main()
