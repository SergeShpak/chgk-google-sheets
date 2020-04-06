package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	GameName          string
	NumberOfQuestions int
	HasWarmUpQuestion bool
	Teams             []string

	OutputDir string `json:"-"`
	NewGame   bool   `json:"-"`
	CredsFile string `json:"-"`
}

func ParseJSONConfig(file string) (*Config, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var c Config
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return nil, err
	}
	if len(c.GameName) == 0 {
		return nil, fmt.Errorf("game name cannot be empty")
	}
	return &c, nil
}
