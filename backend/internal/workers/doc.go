// Package workers runs the bounded background worker pool that consumes queued jobs,
// applies retry limits, routes exhausted jobs to the dead-letter queue, and shuts down
// cleanly on signal.
package workers
