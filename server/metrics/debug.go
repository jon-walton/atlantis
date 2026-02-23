// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"time"

	"github.com/runatlantis/atlantis/server/logging"
	tally "github.com/uber-go/tally/v4"
)

// newLoggingReporter returns a tally reporter that logs to the provided logger at debug level. This is useful for
// local development where the usual sinks are not available.
func newLoggingReporter(logger logging.SimpleLogging) tally.StatsReporter {
	return &debugReporter{log: logger}
}

type debugReporter struct {
	log logging.SimpleLogging
}

// Capabilities interface.

func (r *debugReporter) Reporting() bool {
	return true
}

func (r *debugReporter) Tagging() bool {
	return true
}

func (r *debugReporter) Capabilities() tally.Capabilities {
	return r
}

// Reporter interface.

func (r *debugReporter) Flush() {
	// Silence.
}

func (r *debugReporter) ReportCounter(_ string, _ map[string]string, _ int64) {
}

func (r *debugReporter) ReportGauge(_ string, _ map[string]string, _ float64) {
}

func (r *debugReporter) ReportTimer(_ string, _ map[string]string, _ time.Duration) {
}

func (r *debugReporter) ReportHistogramValueSamples(
	_ string,
	_ map[string]string,
	_ tally.Buckets,
	_,
	_ float64,
	_ int64,
) {
}

func (r *debugReporter) ReportHistogramDurationSamples(
	_ string,
	_ map[string]string,
	_ tally.Buckets,
	_,
	_ time.Duration,
	_ int64,
) {
}
