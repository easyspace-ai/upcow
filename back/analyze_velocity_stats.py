#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
VelocityHedgeHold 策略速度统计分析工具

功能：
1. 从日志或数据文件中提取价格数据
2. 计算不同窗口大小下的速度
3. 统计满足不同速度/位移条件的概率
4. 生成参数配置建议
"""

import re
import json
import csv
import os
import sys
from pathlib import Path
from collections import defaultdict, deque
from datetime import datetime, timedelta
from typing import List, Tuple, Dict, Optional
import statistics

class PriceSample:
    """价格样本"""
    def __init__(self, timestamp: datetime, price_cents: int, token_type: str):
        self.timestamp = timestamp
        self.price_cents = price_cents
        self.token_type = token_type  # 'up' or 'down'

class VelocityCalculator:
    """速度计算器（与 Go 代码逻辑一致）"""
    
    def __init__(self, window_seconds: int):
        self.window_seconds = window_seconds
        self.samples_up = deque()
        self.samples_down = deque()
    
    def add_sample(self, sample: PriceSample):
        """添加价格样本"""
        if sample.token_type == 'up':
            self.samples_up.append(sample)
        else:
            self.samples_down.append(sample)
        self._prune(sample.timestamp)
    
    def _prune(self, now: datetime):
        """清理过期样本（与 Go 代码逻辑一致）"""
        cutoff = now - timedelta(seconds=self.window_seconds)
        
        # 清理 UP 样本
        while self.samples_up and self.samples_up[0].timestamp < cutoff:
            self.samples_up.popleft()
        
        # 清理 DOWN 样本
        while self.samples_down and self.samples_down[0].timestamp < cutoff:
            self.samples_down.popleft()
    
    def compute_velocity(self, token_type: str) -> Optional[Dict]:
        """计算速度（与 Go 代码逻辑一致）"""
        samples = self.samples_up if token_type == 'up' else self.samples_down
        
        if len(samples) < 2:
            return None
        
        first = samples[0]
        last = samples[-1]
        
        dt = (last.timestamp - first.timestamp).total_seconds()
        if dt <= 0.001:
            return None
        
        delta = last.price_cents - first.price_cents
        if delta <= 0:  # 只计算上行（与 Go 代码一致）
            return None
        
        velocity = delta / dt
        
        if velocity != velocity or abs(velocity) == float('inf'):  # NaN or Inf
            return None
        
        return {
            'ok': True,
            'delta': delta,
            'seconds': dt,
            'velocity': velocity
        }

class LogParser:
    """日志解析器"""
    
    @staticmethod
    def parse_timestamp(line: str) -> Optional[datetime]:
        """解析日志时间戳"""
        # 格式: [25-12-30 16:03:10]
        pattern = r'\[(\d+)-(\d+)-(\d+)\s+(\d+):(\d+):(\d+)\]'
        match = re.search(pattern, line)
        if match:
            year, month, day, hour, minute, second = match.groups()
            try:
                # 假设是 2025 年
                return datetime(2025, int(month), int(day), int(hour), int(minute), int(second))
            except:
                pass
        return None
    
    @staticmethod
    def extract_price_from_log(line: str) -> Optional[Tuple[str, int]]:
        """从日志中提取价格信息"""
        # 匹配格式: ⚡ [velocityhedgehold] 准备触发: side=up entryAsk=92c ...
        pattern = r'side=(up|down)\s+entryAsk=(\d+)c'
        match = re.search(pattern, line)
        if match:
            token_type = match.group(1)
            price_cents = int(match.group(2))
            return (token_type, price_cents)
        
        # 匹配格式: 📥 [sessionPriceHandler] 首次收到价格事件: up @ 0.5400
        pattern = r'价格事件:\s*(up|down)\s+@\s+([\d.]+)'
        match = re.search(pattern, line)
        if match:
            token_type = match.group(1)
            price_decimal = float(match.group(2))
            price_cents = int(price_decimal * 100 + 0.5)
            return (token_type, price_cents)
        
        # 匹配格式: 📥 [sessionPriceHandler] 首次收到价格事件: up @ 0.5400 (Session=polymarket)
        pattern = r'首次收到价格事件:\s*(up|down)\s+@\s+([\d.]+)'
        match = re.search(pattern, line)
        if match:
            token_type = match.group(1)
            price_decimal = float(match.group(2))
            price_cents = int(price_decimal * 100 + 0.5)
            return (token_type, price_cents)
        
        # 匹配格式: [price_change->price] 或其他价格更新日志
        # 尝试从盘口价差日志中提取: UP: bid=XXc ask=XXc DOWN: bid=XXc ask=XXc
        pattern = r'UP:\s*bid=(\d+)c\s+ask=(\d+)c.*DOWN:\s*bid=(\d+)c\s+ask=(\d+)c'
        match = re.search(pattern, line)
        if match:
            # 返回 UP 和 DOWN 的 mid 价格
            up_bid, up_ask, down_bid, down_ask = map(int, match.groups())
            up_mid = (up_bid + up_ask) // 2
            down_mid = (down_bid + down_ask) // 2
            # 返回两个价格样本（需要调用者处理）
            return ('up', up_mid)  # 先返回 UP，DOWN 需要单独处理
        
        return None

class CSVDataParser:
    """CSV 数据解析器（用于 datarecorder 生成的数据）"""
    
    @staticmethod
    def parse_csv(file_path: str) -> List[PriceSample]:
        """解析 CSV 文件"""
        samples = []
        try:
            with open(file_path, 'r', encoding='utf-8') as f:
                reader = csv.DictReader(f)
                for row in reader:
                    try:
                        # 尝试多种时间戳格式
                        timestamp_str = row.get('Timestamp', '') or row.get('timestamp', '')
                        if not timestamp_str:
                            continue
                        
                        # 尝试解析时间戳（可能是 Unix 时间戳或 ISO 格式）
                        try:
                            if timestamp_str.isdigit():
                                timestamp = datetime.fromtimestamp(int(timestamp_str))
                            else:
                                timestamp = datetime.fromisoformat(timestamp_str.replace('Z', '+00:00'))
                        except:
                            continue
                        
                        # 尝试多种价格字段名
                        up_price = float(row.get('UpPrice', 0) or row.get('up_price', 0) or row.get('UP', 0))
                        down_price = float(row.get('DownPrice', 0) or row.get('down_price', 0) or row.get('DOWN', 0))
                        
                        if up_price > 0 and up_price < 1.0:  # 验证价格合理性
                            samples.append(PriceSample(
                                timestamp=timestamp,
                                price_cents=int(up_price * 100 + 0.5),
                                token_type='up'
                            ))
                        
                        if down_price > 0 and down_price < 1.0:  # 验证价格合理性
                            samples.append(PriceSample(
                                timestamp=timestamp,
                                price_cents=int(down_price * 100 + 0.5),
                                token_type='down'
                            ))
                    except Exception as e:
                        continue
        except Exception as e:
            print(f"读取 CSV 文件失败 {file_path}: {e}")
        
        return samples

class VelocityAnalyzer:
    """速度分析器"""
    
    def __init__(self, window_seconds: int):
        self.window_seconds = window_seconds
        self.calculator = VelocityCalculator(window_seconds)
        self.velocity_samples = []
        self.delta_samples = []
    
    def analyze_samples(self, samples: List[PriceSample]):
        """分析价格样本"""
        # 按时间排序
        samples.sort(key=lambda x: x.timestamp)
        
        for sample in samples:
            self.calculator.add_sample(sample)
            
            # 计算速度
            for token_type in ['up', 'down']:
                metrics = self.calculator.compute_velocity(token_type)
                if metrics and metrics['ok']:
                    self.velocity_samples.append(metrics['velocity'])
                    self.delta_samples.append(metrics['delta'])
    
    def get_statistics(self) -> Dict:
        """获取统计信息"""
        if not self.velocity_samples:
            return {}
        
        return {
            'count': len(self.velocity_samples),
            'min_velocity': min(self.velocity_samples),
            'max_velocity': max(self.velocity_samples),
            'mean_velocity': statistics.mean(self.velocity_samples),
            'median_velocity': statistics.median(self.velocity_samples),
            'stdev_velocity': statistics.stdev(self.velocity_samples) if len(self.velocity_samples) > 1 else 0,
            'min_delta': min(self.delta_samples),
            'max_delta': max(self.delta_samples),
            'mean_delta': statistics.mean(self.delta_samples),
            'median_delta': statistics.median(self.delta_samples),
        }
    
    def calculate_probability(self, min_velocity: float, min_delta: int) -> float:
        """计算满足条件的概率"""
        if not self.velocity_samples:
            return 0.0
        
        count = 0
        for i, vel in enumerate(self.velocity_samples):
            delta = self.delta_samples[i]
            if vel >= min_velocity and delta >= min_delta:
                count += 1
        
        return count / len(self.velocity_samples) * 100.0

def analyze_log_file(log_file: str) -> List[PriceSample]:
    """从日志文件提取价格数据"""
    samples = []
    parser = LogParser()
    
    with open(log_file, 'r', encoding='utf-8') as f:
        for line in f:
            timestamp = parser.parse_timestamp(line)
            if not timestamp:
                continue
            
            price_info = parser.extract_price_from_log(line)
            if price_info:
                token_type, price_cents = price_info
                samples.append(PriceSample(timestamp, price_cents, token_type))
    
    return samples

def analyze_cycle_velocity(
    samples: List[PriceSample],
    window_seconds_range: List[int],
    min_velocity_range: List[float],
    min_delta_range: List[int]
) -> Dict:
    """分析周期内的速度统计"""
    results = {}
    
    for window_sec in window_seconds_range:
        analyzer = VelocityAnalyzer(window_sec)
        analyzer.analyze_samples(samples)
        
        stats = analyzer.get_statistics()
        if not stats:
            continue
        
        results[window_sec] = {
            'statistics': stats,
            'probabilities': {}
        }
        
        # 计算不同参数组合的概率
        for min_vel in min_velocity_range:
            for min_delta in min_delta_range:
                prob = analyzer.calculate_probability(min_vel, min_delta)
                key = f"vel_{min_vel}_delta_{min_delta}"
                results[window_sec]['probabilities'][key] = prob
    
    return results

def generate_recommendations(analysis_results: Dict, target_probability: float = 5.0) -> Dict:
    """生成参数配置建议"""
    recommendations = []
    
    for window_sec, data in analysis_results.items():
        stats = data['statistics']
        probs = data['probabilities']
        
        # 找到满足目标概率的参数组合
        for key, prob in probs.items():
            if prob >= target_probability:
                # 解析参数
                parts = key.split('_')
                min_vel = float(parts[1])
                min_delta = int(parts[3])
                
                recommendations.append({
                    'windowSeconds': window_sec,
                    'minVelocityCentsPerSec': min_vel,
                    'minMoveCents': min_delta,
                    'probability': prob,
                    'expected_triggers_per_cycle': prob / 100.0 * 900  # 假设周期15分钟=900秒
                })
    
    # 按概率排序
    recommendations.sort(key=lambda x: x['probability'], reverse=True)
    
    return recommendations

def main():
    """主函数"""
    import argparse
    
    parser = argparse.ArgumentParser(description='VelocityHedgeHold velocity statistics analysis')
    parser.add_argument('--log-dir', type=str, default='logs', help='Log directory path')
    parser.add_argument('--data-dir', type=str, default='data', help='Data directory path (CSV files)')
    parser.add_argument('--window-seconds', type=int, nargs='+', default=[3, 5, 8, 10], help='Window size range in seconds, e.g., 3 5 8 10')
    parser.add_argument('--min-velocity', type=float, nargs='+', default=[0.2, 0.3, 0.4, 0.5, 0.6], help='Min velocity range (c/s), e.g., 0.2 0.3 0.4')
    parser.add_argument('--min-delta', type=int, nargs='+', default=[3, 4, 5, 6, 7], help='Min delta range (cents), e.g., 3 4 5 6')
    parser.add_argument('--target-probability', type=float, default=5.0, help='Target trigger probability (percent), default: 5.0')
    parser.add_argument('--output', type=str, default='velocity_analysis_report.json', help='Output JSON file path')
    
    args = parser.parse_args()
    
    print("🔍 开始分析速度统计...")
    
    # 收集价格样本
    all_samples = []
    
    # 从日志文件提取
    log_dir = Path(args.log_dir)
    if log_dir.exists():
        print(f"📂 扫描日志目录: {log_dir}")
        for log_file in log_dir.glob("*.log"):
            print(f"  处理日志文件: {log_file}")
            samples = analyze_log_file(str(log_file))
            all_samples.extend(samples)
            print(f"    提取到 {len(samples)} 个价格样本")
    
    # 从 CSV 文件提取
    data_dir = Path(args.data_dir)
    if data_dir.exists():
        print(f"📂 扫描数据目录: {data_dir}")
        csv_parser = CSVDataParser()
        for csv_file in data_dir.rglob("*.csv"):
            print(f"  处理 CSV 文件: {csv_file}")
            samples = csv_parser.parse_csv(str(csv_file))
            all_samples.extend(samples)
            print(f"    提取到 {len(samples)} 个价格样本")
    
    if not all_samples:
        print("❌ 未找到任何价格数据！")
        print("   请确保日志目录或数据目录中有数据文件")
        sys.exit(1)
    
    print(f"\n✅ 总共收集到 {len(all_samples)} 个价格样本")
    
    # 按周期分组分析
    print("\n📊 开始统计分析...")
    
    # 分析所有样本
    analysis_results = analyze_cycle_velocity(
        all_samples,
        args.window_seconds,
        args.min_velocity,
        args.min_delta
    )
    
    # 生成建议
    recommendations = generate_recommendations(analysis_results, args.target_probability)
    
    # 输出结果
    output_data = {
        'summary': {
            'total_samples': len(all_samples),
            'analysis_windows': list(analysis_results.keys()),
            'total_recommendations': len(recommendations)
        },
        'analysis_results': analysis_results,
        'recommendations': recommendations[:20]  # 只显示前20个
    }
    
    # 保存 JSON
    with open(args.output, 'w', encoding='utf-8') as f:
        json.dump(output_data, f, indent=2, ensure_ascii=False, default=str)
    
    print(f"\n✅ 分析完成！结果已保存到: {args.output}")
    
    # 打印摘要
    print("\n" + "="*80)
    print("📈 速度统计摘要")
    print("="*80)
    
    for window_sec, data in analysis_results.items():
        stats = data['statistics']
        print(f"\n窗口大小: {window_sec}秒")
        print(f"  样本数: {stats['count']}")
        print(f"  速度范围: {stats['min_velocity']:.3f} - {stats['max_velocity']:.3f} (c/s)")
        print(f"  平均速度: {stats['mean_velocity']:.3f} (c/s)")
        print(f"  中位数速度: {stats['median_velocity']:.3f} (c/s)")
        print(f"  位移范围: {stats['min_delta']} - {stats['max_delta']} (c)")
        print(f"  平均位移: {stats['mean_delta']:.1f} (c)")
    
    print("\n" + "="*80)
    print("💡 参数配置建议（按触发概率排序）")
    print("="*80)
    
    for i, rec in enumerate(recommendations[:10], 1):
        print(f"\n建议 #{i}:")
        print(f"  windowSeconds: {rec['windowSeconds']}")
        print(f"  minVelocityCentsPerSec: {rec['minVelocityCentsPerSec']}")
        print(f"  minMoveCents: {rec['minMoveCents']}")
        print(f"  预期触发概率: {rec['probability']:.2f}%")
        print(f"  预期每周期触发次数: {rec['expected_triggers_per_cycle']:.1f}")

if __name__ == '__main__':
    main()
