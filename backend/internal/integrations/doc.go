// Package integrations adapts provider webhooks (PagerDuty, Datadog, Slack) into
// FlowCast's canonical event model. Each adapter verifies its payload, parses it, and
// normalizes it while preserving the original raw payload.
package integrations
