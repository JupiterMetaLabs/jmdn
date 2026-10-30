package metrics

import "github.com/prometheus/client_golang/prometheus"

// EntropyLastResortTotal counts epochs this node finalised from the last-resort
// seed (messaging.LastResortSeedForEpoch): the fallback reached its deadline
// short of signers. Counted on live decisions only, so a restart's replay of
// the same epoch does not count it twice.
//
// Any increase deserves an alert: the chain was already short of certificates,
// and a last-resort seed is easier for the sequencer to bias than a full fold.
var EntropyLastResortTotal = factory.NewCounter(prometheus.CounterOpts{
	Name: "jmdn_entropy_last_resort_total",
	Help: "Epochs finalised from the last-resort seed after the fallback deadline passed short of signers",
})

// EntropyLastResortConsecutive is the number of consecutive epochs, ending at
// the newest decided one, finalised from the last-resort seed. 0 once an epoch
// finalises any other way. A value above 1 means the shortfall is persistent,
// not a single bad window.
var EntropyLastResortConsecutive = factory.NewGauge(prometheus.GaugeOpts{
	Name: "jmdn_entropy_last_resort_consecutive",
	Help: "Consecutive newest epochs finalised from the last-resort seed (0 when the newest was not)",
})
