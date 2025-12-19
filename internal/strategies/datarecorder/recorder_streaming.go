package datarecorder

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DataPoint 数据点
type DataPoint struct {
	Timestamp        int64   // Unix 时间戳（秒）
	BTCTargetPrice   float64 // BTC 目标价（上一个周期收盘价）
	BTCRealtimePrice float64 // BTC 实时价
	UpPrice          float64 // UP 价格
	DownPrice        float64 // DOWN 价格
}

// DataRecorder 数据记录器（流式写入，每条记录实时追加到 CSV）
type DataRecorder struct {
	outputDir    string
	currentCycle string // 当前周期 slug

	file   *os.File
	writer *csv.Writer

	mu sync.Mutex
}

// NewDataRecorder 创建新的数据记录器
func NewDataRecorder(outputDir string) (*DataRecorder, error) {
	// 确保输出目录存在
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("创建输出目录失败: %w", err)
	}

	return &DataRecorder{
		outputDir: outputDir,
	}, nil
}

// StartCycle 开始新周期（如果已在该周期则不做任何事）
func (dr *DataRecorder) StartCycle(cycleSlug string) error {
	dr.mu.Lock()
	defer dr.mu.Unlock()

	if cycleSlug == "" {
		return fmt.Errorf("cycle slug 不能为空")
	}

	// 如果已经在同一个周期且 writer 存在，直接返回
	if dr.currentCycle == cycleSlug && dr.writer != nil {
		return nil
	}

	oldCycle := dr.currentCycle

	// 关闭旧周期文件
	if dr.writer != nil {
		dr.writer.Flush()
		if err := dr.writer.Error(); err != nil {
			return fmt.Errorf("旧周期 CSV writer 错误: %w", err)
		}
	}
	if dr.file != nil {
		if err := dr.file.Close(); err != nil {
			return fmt.Errorf("关闭旧周期文件失败: %w", err)
		}
		if oldCycle != "" {
			// 验证旧周期文件已保存
			oldFilename := fmt.Sprintf("%s.csv", oldCycle)
			oldPath := filepath.Join(dr.outputDir, oldFilename)
			if info, err := os.Stat(oldPath); err == nil {
				if info.Size() > 0 {
					// 文件存在且非空，保存成功
				} else {
					return fmt.Errorf("旧周期文件为空: %s", oldPath)
				}
			}
		}
	}

	dr.currentCycle = cycleSlug
	dr.file = nil
	dr.writer = nil

	// 打开/创建当前周期文件（追加模式）
	filename := fmt.Sprintf("%s.csv", cycleSlug)
	path := filepath.Join(dr.outputDir, filename)

	// 判断文件是否已存在且非空，用于决定是否写入表头
	var needHeader bool
	if info, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			needHeader = true
		} else {
			return fmt.Errorf("获取 CSV 文件信息失败: %w", err)
		}
	} else if info.Size() == 0 {
		needHeader = true
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("打开 CSV 文件失败: %w", err)
	}

	writer := csv.NewWriter(file)

	if needHeader {
		header := []string{
			"timestamp",
			"btc_target_price",
			"btc_realtime_price",
			"up_price",
			"down_price",
		}
		if err := writer.Write(header); err != nil {
			file.Close()
			return fmt.Errorf("写入 CSV 头失败: %w", err)
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			file.Close()
			return fmt.Errorf("刷新 CSV 头失败: %w", err)
		}
	}

	dr.file = file
	dr.writer = writer

	return nil
}

// Record 记录数据点（立即追加到当前周期 CSV）
func (dr *DataRecorder) Record(point DataPoint) error {
	dr.mu.Lock()
	defer dr.mu.Unlock()

	if dr.currentCycle == "" || dr.file == nil || dr.writer == nil {
		return fmt.Errorf("当前周期未初始化，无法记录数据")
	}

	record := []string{
		fmt.Sprintf("%d", point.Timestamp),
		fmt.Sprintf("%.2f", point.BTCTargetPrice),
		fmt.Sprintf("%.2f", point.BTCRealtimePrice),
		fmt.Sprintf("%.4f", point.UpPrice),
		fmt.Sprintf("%.4f", point.DownPrice),
	}

	if err := dr.writer.Write(record); err != nil {
		return fmt.Errorf("写入 CSV 数据失败: %w", err)
	}
	dr.writer.Flush()
	if err := dr.writer.Error(); err != nil {
		return fmt.Errorf("刷新 CSV 数据失败: %w", err)
	}

	return nil
}

// SaveCurrentCycle 刷新并关闭当前周期文件
func (dr *DataRecorder) SaveCurrentCycle() error {
	dr.mu.Lock()
	defer dr.mu.Unlock()

	if dr.file == nil {
		// 如果没有打开的文件，直接返回
		return nil
	}

	currentCycle := dr.currentCycle
	filePath := ""
	if currentCycle != "" {
		filename := fmt.Sprintf("%s.csv", currentCycle)
		filePath = filepath.Join(dr.outputDir, filename)
	}

	// 刷新 writer
	if dr.writer != nil {
		dr.writer.Flush()
		if err := dr.writer.Error(); err != nil {
			return fmt.Errorf("CSV writer 错误 (周期=%s): %w", currentCycle, err)
		}
	}

	// 关闭文件
	if err := dr.file.Close(); err != nil {
		return fmt.Errorf("关闭 CSV 文件失败 (周期=%s): %w", currentCycle, err)
	}

	// 验证文件已保存（如果知道文件路径）
	if filePath != "" {
		if info, err := os.Stat(filePath); err != nil {
			return fmt.Errorf("验证保存的文件失败 (周期=%s, 路径=%s): %w", currentCycle, filePath, err)
		} else if info.Size() == 0 {
			return fmt.Errorf("保存的文件为空 (周期=%s, 路径=%s)", currentCycle, filePath)
		}
		// 文件存在且非空，保存成功
	}

	dr.file = nil
	dr.writer = nil
	dr.currentCycle = ""

	return nil
}

// GetCurrentCycle 获取当前周期
func (dr *DataRecorder) GetCurrentCycle() string {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	return dr.currentCycle
}


