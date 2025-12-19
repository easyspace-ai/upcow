package domain

// Grid 网格领域模型
type Grid struct {
	Levels        []int  // 网格层级列表（分）
	StartLevel    int    // 起始层级（分）
	Gap           int    // 网格间距（分）
	EndLevel      int    // 结束层级（分）
}

// NewGrid 创建新网格（通过计算生成层级）
func NewGrid(startLevel, gap, endLevel int) *Grid {
	levels := make([]int, 0)
	
	// 从 startLevel 开始，按 gap 对齐
	currentLevel := startLevel
	remainder := currentLevel % gap
	if remainder != 0 {
		currentLevel += gap - remainder
	}
	
	// 生成网格层级
	for currentLevel <= endLevel {
		levels = append(levels, currentLevel)
		currentLevel += gap
	}
	
	return &Grid{
		Levels:         levels,
		StartLevel:     startLevel,
		Gap:            gap,
		EndLevel:       endLevel,
	}
}

// NewGridFromLevels 从手工定义的层级列表创建网格
func NewGridFromLevels(levels []int) *Grid {
	if len(levels) == 0 {
		return &Grid{
			Levels:         []int{},
			StartLevel:     0,
			Gap:            0,
			EndLevel:       0,
		}
	}
	
	startLevel := levels[0]
	endLevel := levels[len(levels)-1]
	gap := 0
	if len(levels) > 1 {
		gap = levels[1] - levels[0]
	}
	
	return &Grid{
		Levels:         levels,
		StartLevel:     startLevel,
		Gap:            gap,
		EndLevel:       endLevel,
	}
}

// GetLevel 获取价格对应的网格层级
func (g *Grid) GetLevel(priceCents int) *int {
	for _, level := range g.Levels {
		if priceCents == level {
			return &level
		}
	}
	return nil
}

// GetInterval 获取价格所在的网格区间
func (g *Grid) GetInterval(priceCents int) (lower *int, upper *int) {
	for _, level := range g.Levels {
		if level <= priceCents {
			lower = &level
		}
		if level >= priceCents {
			upper = &level
			break
		}
	}
	return
}

// GetNextLevel 获取下一个网格层级（用于买入时机）
func (g *Grid) GetNextLevel(priceCents int) *int {
	for _, level := range g.Levels {
		if level > priceCents {
			return &level
		}
	}
	return nil // 没有下一个层级（价格已超过最大层级）
}

// GetCurrentIntervalInfo 获取当前价格所在的网格区间信息
// 返回：当前价格、下边界、上边界、下一个买入层级
func (g *Grid) GetCurrentIntervalInfo(priceCents int) (currentPrice int, lowerBound *int, upperBound *int, nextLevel *int) {
	currentPrice = priceCents
	lowerBound, upperBound = g.GetInterval(priceCents)
	nextLevel = g.GetNextLevel(priceCents)
	return
}

