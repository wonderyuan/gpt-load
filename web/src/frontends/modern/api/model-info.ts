import type { ApiClient } from '@shared/http/client'
import { InvalidResponseError } from '@shared/http/errors'
import { integer, list, oneOf, record, text } from './response'

export const modelInfoKey = ['modern', 'fork-model-info'] as const
export type ModelInfoStatus = 'ready' | 'stale' | 'unavailable'
export interface ModelInfoSection<T> {
  status: ModelInfoStatus
  fetched_at_ms: number | null
  updated_at_ms: number | null
  data: T | null
}
export interface IntelligenceResult {
  effort: string
  score: number | null
  cost_usd: number | null
  duration_seconds: number | null
}
export interface IntelligenceModel {
  id: string
  name: string
  results: IntelligenceResult[]
}
export interface ParetoPoint {
  id: string
  model_id: string
  model: string
  effort: string
  score: number
  cost_usd: number
  duration_minutes: number
  composite_cost: number
}
export interface ModelIntelligence {
  batch: string
  tested_at: string
  max_score: number
  efforts: string[]
  models: IntelligenceModel[]
  pareto: ParetoPoint[]
}
export interface SpeedModel {
  id: string
  name: string
  tokens_per_second: number | null
  requests: number
  devices: number
  harnesses: { name: string; requests: number }[]
  history: { at_ms: number; value: number | null }[]
}
export interface ModelSpeed {
  window_minutes: number
  models: SpeedModel[]
}
export interface QuotaModel {
  name: string
  weekly_tokens: number | null
  api_value_usd: number | null
}
export interface QuotaPlan {
  id: string
  name: string
  monthly_usd: number
  models: QuotaModel[]
}
export interface ModelQuota {
  recorded_at: string
  plans: QuotaPlan[]
}
export interface ModelInfoReport {
  observed_at_ms: number
  intelligence: ModelInfoSection<ModelIntelligence>
  speed: ModelInfoSection<ModelSpeed>
  quota: ModelInfoSection<ModelQuota>
}

function amount(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0)
    throw new InvalidResponseError()
  return value
}
function nullableAmount(value: unknown): number | null {
  return value === null ? null : amount(value)
}
function timestamp(value: unknown): number | null {
  return value === null ? null : integer(value)
}
function identity(value: unknown): string {
  const result = text(value)
  if (!result.trim()) throw new InvalidResponseError()
  return result
}
function unique<T>(values: T[], key: (value: T) => string): T[] {
  if (new Set(values.map(key)).size !== values.length) throw new InvalidResponseError()
  return values
}
function section<T>(value: unknown, parse: (value: unknown) => T): ModelInfoSection<T> {
  const raw = record(value)
  const status = oneOf(raw.status, ['ready', 'stale', 'unavailable'])
  const data = raw.data === null ? null : parse(raw.data)
  if (status === 'ready' && data === null) throw new InvalidResponseError()
  return {
    status,
    fetched_at_ms: timestamp(raw.fetched_at_ms),
    updated_at_ms: timestamp(raw.updated_at_ms),
    data,
  }
}
function intelligence(value: unknown): ModelIntelligence {
  const raw = record(value)
  const max_score = amount(raw.max_score)
  if (!max_score) throw new InvalidResponseError()
  const score = (value: unknown) => {
    const result = amount(value)
    if (result > max_score) throw new InvalidResponseError()
    return result
  }
  const efforts = unique(list(raw.efforts).map(identity), (value) => value)
  const effort = (value: unknown) => {
    const result = identity(value)
    if (!efforts.includes(result)) throw new InvalidResponseError()
    return result
  }
  return {
    batch: text(raw.batch),
    tested_at: text(raw.tested_at),
    max_score,
    efforts,
    models: unique(
      list(raw.models).map((value) => {
        const model = record(value)
        return {
          id: identity(model.id),
          name: identity(model.name),
          results: unique(
            list(model.results).map((value) => {
              const result = record(value)
              return {
                effort: effort(result.effort),
                score: result.score === null ? null : score(result.score),
                cost_usd: nullableAmount(result.cost_usd),
                duration_seconds: nullableAmount(result.duration_seconds),
              }
            }),
            (result) => result.effort,
          ),
        }
      }),
      (model) => model.id,
    ),
    pareto: unique(
      list(raw.pareto).map((value) => {
        const point = record(value)
        return {
          id: identity(point.id),
          model_id: identity(point.model_id),
          model: identity(point.model),
          effort: effort(point.effort),
          score: score(point.score),
          cost_usd: amount(point.cost_usd),
          duration_minutes: amount(point.duration_minutes),
          composite_cost: amount(point.composite_cost),
        }
      }),
      (point) => point.id,
    ),
  }
}
function speed(value: unknown): ModelSpeed {
  const raw = record(value)
  return {
    window_minutes: integer(raw.window_minutes, 1),
    models: unique(
      list(raw.models).map((value) => {
        const model = record(value)
        return {
          id: identity(model.id),
          name: identity(model.name),
          tokens_per_second: nullableAmount(model.tokens_per_second),
          requests: integer(model.requests),
          devices: integer(model.devices),
          harnesses: unique(
            list(model.harnesses).map((value) => {
              const harness = record(value)
              return { name: identity(harness.name), requests: integer(harness.requests) }
            }),
            (harness) => harness.name,
          ),
          history: unique(
            list(model.history).map((value) => {
              const point = record(value)
              return { at_ms: integer(point.at_ms), value: nullableAmount(point.value) }
            }),
            (point) => String(point.at_ms),
          ).sort((a, b) => a.at_ms - b.at_ms),
        }
      }),
      (model) => model.id,
    ),
  }
}
function quota(value: unknown): ModelQuota {
  const raw = record(value)
  return {
    recorded_at: text(raw.recorded_at),
    plans: unique(
      list(raw.plans).map((value) => {
        const plan = record(value)
        return {
          id: identity(plan.id),
          name: identity(plan.name),
          monthly_usd: amount(plan.monthly_usd),
          models: unique(
            list(plan.models).map((value) => {
              const model = record(value)
              return {
                name: identity(model.name),
                weekly_tokens: nullableAmount(model.weekly_tokens),
                api_value_usd: nullableAmount(model.api_value_usd),
              }
            }),
            (model) => model.name,
          ),
        }
      }),
      (plan) => plan.id,
    ),
  }
}
export function readModelInfo(value: unknown): ModelInfoReport {
  const raw = record(value)
  return {
    observed_at_ms: integer(raw.observed_at_ms),
    intelligence: section(raw.intelligence, intelligence),
    speed: section(raw.speed, speed),
    quota: section(raw.quota, quota),
  }
}
export async function getModelInfo(
  client: ApiClient,
  signal: AbortSignal,
): Promise<ModelInfoReport> {
  return readModelInfo(await client.request('/api/fork/model-info', { signal }))
}
