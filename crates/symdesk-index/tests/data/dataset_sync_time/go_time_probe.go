package main

import (
	"encoding/json"
	"fmt"
	"time"
)

type observation struct {
	Value   string `json:"value"`
	UTCDate string `json:"utc_date,omitempty"`
	Error   string `json:"error,omitempty"`
}

func main() {
	values := []string{
		"2026-09-20T01:02:03,123Z",
		"2026-09-20T01:02:03.123Z",
		"2026-09-20T1:02:03Z",
		"2026-09-20T01:02:03+23:59",
		"2026-09-20T01:02:03+24:00",
		"2026-09-20T01:02:03+25:00",
		"2026-09-20T01:02:03+00:59",
		"2026-09-20T01:02:03+00:60",
		"2026-09-20T01:02:03+00:61",
		"2026-09-20T01:02:03z",
		"2026-09-20",
		"2026-09-20T24:00:00Z",
		"2026-09-20T23:60:00Z",
		"2026-09-20T23:59:60Z",
		"2026-02-29T01:02:03Z",
		"2024-02-29T01:02:03Z",
		"0000-01-01T00:00:00+00:01",
		"9999-12-31T23:59:59-24:00",
		"2026-09-20T01:02:03+",
		"2026-09-20T01:02:03Ztrailing",
		"2026-09-20T01:02:03Zé",
	}
	for _, value := range values {
		parsed, err := time.Parse(time.RFC3339, value)
		result := observation{Value: value}
		if err != nil {
			result.Error = err.Error()
		} else {
			result.UTCDate = parsed.UTC().Format(time.DateOnly)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			panic(err)
		}
		fmt.Println(string(encoded))
	}
}
