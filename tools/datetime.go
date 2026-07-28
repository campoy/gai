package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// DateTime returns the current date and time in a human-readable format.
var DateTime = New(
	"current_datetime",
	"Current date and time. Use for anything about today, now, or the current year.",
	map[string]any{
		"type": "object",
		"properties": map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "IANA timezone name, e.g. Europe/Madrid. Omit for server local time.",
			},
		},
	},
	dateTime,
)

func dateTime(_ context.Context, args string) (string, error) {
	var p struct {
		Timezone string `json:"timezone"`
	}
	if args != "" {
		if err := json.Unmarshal([]byte(args), &p); err != nil {
			return "", err
		}
	}
	loc := time.Local
	if p.Timezone != "" {
		l, err := time.LoadLocation(p.Timezone)
		if err != nil {
			return "", fmt.Errorf("unknown timezone %q", p.Timezone)
		}
		loc = l
	}
	return time.Now().In(loc).Format("2006-01-02 15:04:05 MST"), nil
}
