package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Parses OpenWorkflow duration strings (e.g. "1s", "100ms", "5m", "7d",
// "5 minutes") into milliseconds.

var durationRegex = regexp.MustCompile(`(?i)^(-?\.?\d+(?:\.\d+)?)\s*([a-z]+)?$`)

const (
	secondMS = 1000.0
	minuteMS = 60 * secondMS
	hourMS   = 60 * minuteMS
	dayMS    = 24 * hourMS
	weekMS   = 7 * dayMS
	// Average Gregorian month/year (365.25 days / 12) so month * 12 === year.
	monthMS = 2_629_800_000.0
	yearMS  = 31_557_600_000.0
)

var durationMultipliers = map[string]float64{
	"millisecond": 1, "milliseconds": 1, "msec": 1, "msecs": 1, "ms": 1,
	"second": secondMS, "seconds": secondMS, "sec": secondMS, "secs": secondMS, "s": secondMS,
	"minute": minuteMS, "minutes": minuteMS, "min": minuteMS, "mins": minuteMS, "m": minuteMS,
	"hour": hourMS, "hours": hourMS, "hr": hourMS, "hrs": hourMS, "h": hourMS,
	"day": dayMS, "days": dayMS, "d": dayMS,
	"week": weekMS, "weeks": weekMS, "w": weekMS,
	"month": monthMS, "months": monthMS, "mo": monthMS,
	"year": yearMS, "years": yearMS, "yr": yearMS, "yrs": yearMS, "y": yearMS,
}

// parseDuration parses a duration string into milliseconds.
func parseDuration(str string) (float64, error) {
	if str == "" {
		return 0, fmt.Errorf(`invalid duration format: ""`)
	}

	m := durationRegex.FindStringSubmatch(str)
	if m == nil || m[1] == "" {
		return 0, fmt.Errorf("invalid duration format: %q", str)
	}

	num, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration format: %q", str)
	}

	unit := "ms" // default to ms if not provided
	if m[2] != "" {
		unit = strings.ToLower(m[2])
	}

	mult, ok := durationMultipliers[unit]
	if !ok {
		return 0, fmt.Errorf("invalid duration format: %q", str)
	}

	return num * mult, nil
}
