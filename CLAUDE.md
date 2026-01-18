# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Octopus is an LLM API aggregation and load balancing service. It acts as a proxy that accepts requests in OpenAI/Anthropic formats and routes them to various upstream LLM providers (OpenAI, Anthropic, Gemini, Volcengine) with automatic protocol conversion.

## Tech Stack

- **Backend**: Go 1.24.4 (Gin, GORM, Cobra, Zap)
- **Frontend**: Next.js 16 with React 19, TypeScript, TailwindCSS 4, Zustand, TanStack Query

## Development Commands

### Backend
```bash
# Start server (config auto-generated on first run)
go run main.go start

# Start with specific config
go run main.go start --config ./data/config.json
```

### Frontend
```bash
cd web
pnpm install
NEXT_PUBLIC_API_BASE_URL="http://127.0.0.1:8080" pnpm run dev  # Dev server
pnpm run build   # Production build
pnpm run lint    # Lint check
```

### Full Production Build
```bash
cd web && pnpm install && pnpm run build
mv web/out static/
go run main.go start
```

## Architecture

```
Client (OpenAI SDK, Claude Code, etc.)
         |
         v
+------------------+
|   Go Backend     |  Port 8080
|   (Gin Router)   |
|        |         |
|  +-----v------+  |
|  | Inbound    |  |  Converts client request to internal format
|  | Transformer|  |  (OpenAI Chat, OpenAI Responses, Anthropic Messages)
|  +-----+------+  |
|        |         |
|  +-----v------+  |
|  | Load       |  |  Selects channel (Round Robin/Random/Failover/Weighted)
|  | Balancer   |  |
|  +-----+------+  |
|        |         |
|  +-----v------+  |
|  | Outbound   |  |  Converts to target provider format
|  | Transformer|  |  (OpenAI, Anthropic, Gemini, Volcengine)
|  +-----+------+  |
+--------+---------+
         |
         v
   Upstream LLM Provider
```

## Key Directories

| Directory | Purpose |
|-----------|---------|
| `cmd/` | CLI commands (start, version) |
| `internal/model/` | Data models (Channel, Group, APIKey, User, Log, Stats) |
| `internal/op/` | Business operations/CRUD with caching |
| `internal/relay/` | Core API relay/proxy logic with load balancing |
| `internal/transformer/` | Protocol conversion adapters (inbound/outbound) |
| `internal/server/` | HTTP server, handlers, middleware, router |
| `web/src/api/` | Frontend API client |
| `web/src/app/` | Next.js App Router pages |
| `web/src/components/` | React components |

## Core Concepts

### Channel
A configured connection to an LLM provider with base URL, API keys, and proxy settings.

### Group
Aggregates multiple channels under one model name for load balancing. Contains GroupItems that map channels to models.

### Transformer Pattern
- **Inbound**: Accepts `/v1/chat/completions`, `/v1/responses`, `/v1/messages` endpoints
- **Outbound**: Routes to OpenAI Chat, OpenAI Responses, Anthropic Messages, Gemini, Volcengine

### Load Balancing
Supports Round Robin, Random, Failover, and Weighted strategies for selecting channels within a group.

## Configuration

- Config file: `data/config.json` (auto-generated)
- Environment variables: `OCTOPUS_*` prefix
- Database: SQLite (default), MySQL, PostgreSQL supported
