// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package pm

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// WritePrometheus renders every counter in Prometheus text-exposition
// format (v0.0.4). Suitable for a /metrics endpoint scraped by a
// Prometheus server, Grafana Agent, or any OTEL collector with the
// prometheus receiver. Counter names are converted from the
// "AUTH.SuccBase" style used internally to "sacore_auth_succbase"
// snake-case so they match Prometheus naming conventions; the
// original family is preserved as a `family` label so dashboards can
// still filter by 3GPP measurement group.
//
// Usage (from a webservice route handler):
//
//	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
//	pm.Default.WritePrometheus(w)
func (c *Counters) WritePrometheus(w io.Writer) {
	all := c.All()
	// Sort for stable output so diffing two scrapes is useful.
	names := make([]string, 0, len(all))
	for k := range all {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		metric, family := promName(n)
		fmt.Fprintf(w, "# HELP %s Internal 3GPP counter %s\n", metric, n)
		fmt.Fprintf(w, "# TYPE %s counter\n", metric)
		fmt.Fprintf(w, "%s{family=%q} %d\n", metric, family, all[n])
	}
}

// promName converts "AUTH.SuccBase" → ("sacore_auth_succbase", "AUTH").
// Prometheus names must match [a-zA-Z_][a-zA-Z0-9_]*; any dots, hyphens
// or other punctuation become underscores.
func promName(in string) (metric, family string) {
	family = in
	if dot := strings.Index(in, "."); dot > 0 {
		family = in[:dot]
	}
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, in)
	return "sacore_" + strings.ToLower(s), family
}
