# FlowCast

AI-powered incident intelligence and postmortem generation for software engineering teams.

> **Status:** early development. This README is a skeleton and will be filled in as the
> system is built. Nothing below claims functionality that does not exist yet.

## The Problem

When production software fails, engineers reconstruct what happened by hand, stitching
together PagerDuty incidents, Datadog alerts, application logs, deployment events, Slack
threads, and metrics. Those signals are noisy and heavily correlated: an alert that fires
during an incident is usually a *symptom*, not the cause.

## What FlowCast Does

FlowCast ingests incident telemetry and attempts to reconstruct the causal chain:

```text
Trigger → Root Cause → Propagation → Symptoms → Engineer Response → Mitigation → Recovery
```

It then produces a ranked set of root-cause hypotheses with supporting evidence, and
generates a structured, evidence-grounded postmortem.

## The Research Question

> Can an AI system distinguish the actual root cause of a software incident from the many
> correlated symptoms and noisy events generated during that incident?

To answer that with something better than "the reports look convincing," FlowCast includes
an incident simulator that injects controlled faults into a demo application. The simulator
knows the ground truth; the analysis pipeline never sees it. Predictions are scored against
ground truth after the fact.

## Architecture

Modular monolith. One PostgreSQL database (with pgvector), one Redis instance.

```text
flowcast/
├── backend/     Go 1.22+ · chi · pgx · go-redis · modular monolith
├── frontend/    Next.js · TypeScript · Tailwind · shadcn/ui · TanStack Query
├── simulator/   Dockerized demo app, fault scenarios, telemetry fixtures
└── docs/        architecture, AI pipeline, simulation, evaluation
```

## Tech Stack

| Layer          | Choices                                                        |
| -------------- | -------------------------------------------------------------- |
| Backend        | Go, go-chi/chi v5, pgx/v5, go-redis/v9, JWT, bcrypt, `log/slog` |
| Data           | PostgreSQL 16 + pgvector, Redis 7                               |
| AI             | Provider interface with OpenAI and mock implementations         |
| Frontend       | Next.js, React, TypeScript (strict), Tailwind, shadcn/ui        |
| Infrastructure | Docker, Docker Compose                                          |

## Getting Started

Not available yet — setup instructions land with the Docker Compose and backend steps.
See [docs/local-development.md](docs/local-development.md) once it exists.

## Documentation

- `docs/architecture.md`
- `docs/ai-pipeline.md`
- `docs/simulation.md`
- `docs/evaluation.md`

## Evaluation Results

Not measured yet. Results will appear here only once the evaluation pipeline has actually
produced them.

## Limitations

To be documented as the system takes shape.
