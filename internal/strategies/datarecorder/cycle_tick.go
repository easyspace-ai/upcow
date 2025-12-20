package datarecorder

import "context"

func (s *DataRecorderStrategy) cycleCheckTick(ctx context.Context) error {
	// 复用既有实现：基于时间戳检查并切换周期
	s.checkAndSwitchCycleByTime(ctx)
	return nil
}

