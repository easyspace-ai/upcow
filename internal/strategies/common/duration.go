package common

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 是一个“可读”的 duration 类型：
// - YAML/JSON 支持字符串（例如 "15m", "900s"）
// - 也支持数字（整数），按“秒”解释（兼容简写）
//
// 目的：让策略配置像 bbgo 一样好写，而不是要求用户写纳秒或做适配器转换。
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		// string
		if value.Tag == "!!str" {
			s := strings.TrimSpace(value.Value)
			if s == "" {
				d.Duration = 0
				return nil
			}
			dd, err := time.ParseDuration(s)
			if err != nil {
				return fmt.Errorf("invalid duration %q: %w", s, err)
			}
			d.Duration = dd
			return nil
		}
		// int -> seconds
		if value.Tag == "!!int" {
			secs, err := strconv.ParseInt(strings.TrimSpace(value.Value), 10, 64)
			if err != nil {
				return fmt.Errorf("invalid duration seconds %q: %w", value.Value, err)
			}
			d.Duration = time.Duration(secs) * time.Second
			return nil
		}
		// float -> seconds (allow)
		if value.Tag == "!!float" {
			f, err := strconv.ParseFloat(strings.TrimSpace(value.Value), 64)
			if err != nil {
				return fmt.Errorf("invalid duration seconds %q: %w", value.Value, err)
			}
			d.Duration = time.Duration(f * float64(time.Second))
			return nil
		}
	}
	return fmt.Errorf("unsupported duration node: kind=%d tag=%s value=%q", value.Kind, value.Tag, value.Value)
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		d.Duration = 0
		return nil
	}

	// string
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			d.Duration = 0
			return nil
		}
		dd, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		d.Duration = dd
		return nil
	}

	// number -> seconds
	var secs float64
	if err := json.Unmarshal(b, &secs); err != nil {
		return err
	}
	d.Duration = time.Duration(secs * float64(time.Second))
	return nil
}

