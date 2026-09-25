package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const MinToolOutputChars = 1000
const MaxToolOutputChars = 100000
const DefaultToolOutputChars = 20000

var ErrToolOutputSettings = errors.New("请输入 1,000–100,000 的整数；截断开关必须为布尔值")

type ToolOutputSettings struct {
	Enabled  bool `json:"enabled"`
	MaxChars int  `json:"max_chars"`
}

func DefaultToolOutputSettings() ToolOutputSettings {
	return ToolOutputSettings{Enabled: true, MaxChars: DefaultToolOutputChars}
}
func (s ToolOutputSettings) Validate() error {
	if s.MaxChars < MinToolOutputChars || s.MaxChars > MaxToolOutputChars {
		return ErrToolOutputSettings
	}
	return nil
}

// Missing fields and null are not silently interpreted as false or zero.
func (s *ToolOutputSettings) UnmarshalJSON(data []byte) error {
	var value struct {
		Enabled  *bool `json:"enabled"`
		MaxChars *int  `json:"max_chars"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return ErrToolOutputSettings
	}
	var extra any
	if d.Decode(&extra) != io.EOF || value.Enabled == nil || value.MaxChars == nil {
		return ErrToolOutputSettings
	}
	next := ToolOutputSettings{*value.Enabled, *value.MaxChars}
	if err := next.Validate(); err != nil {
		return err
	}
	*s = next
	return nil
}
