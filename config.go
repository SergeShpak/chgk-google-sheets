package main

import (
	"encoding/json"
	"os"
)

type Config struct {
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
	return &c, nil
}
