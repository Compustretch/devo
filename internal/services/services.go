package services

import (
	"fmt"
	"strings"
)

var Names = []string{"clickhouse", "grafana"}

func Valid(name string) bool {
	for _, s := range Names {
		if name == s { return true }
	}
	return false
}

func Parse(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" { continue }
		if !Valid(name) { return nil, fmt.Errorf("unknown service %q (valid: clickhouse, grafana)", name) }
		out[name] = true
	}
	return out, nil
}
