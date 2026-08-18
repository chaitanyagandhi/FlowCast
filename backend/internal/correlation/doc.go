// Package correlation performs deterministic, non-AI event preprocessing: chronological
// sorting, deduplication, collapsing of repetitive alerts, timestamp normalization, and
// generation of causal candidate links from temporal and service relationships. Its output
// is what the AI pipeline reasons over.
package correlation
