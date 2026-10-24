// TypeScript mirrors of the Go JSON types (pkg/model, pkg/history,
// pkg/engine). Field names are the Go `json` tags. Times are RFC 3339.

export type Provenance = "observed" | "derived" | "inferred" | "simulated";
export type Severity = "debug" | "info" | "warn" | "error";
export type Phase = "unknown" | "up" | "gated" | "unreachable" | "gone";
export type ComponentStatus = "active" | "idle" | "degraded" | "failed" | "unobservable" | "unknown";
export type IssueClass = "confirmed" | "suspicious" | "hypothesis";
export type Mode = "live" | "sim" | "replay";

export interface Source {
  adapter: string;
  ref: string;
}

export interface Correlation {
  traceId?: string;
  spanId?: string;
  chunk?: string;
  peer?: string;
  peerNode?: string;
  tagId?: number;
  batchId?: string;
}

export interface BeeEvent {
  seq: number;
  t: string;
  ingestedAt: string;
  node?: string;
  component?: string;
  kind: string;
  sev: Severity;
  prov: Provenance;
  source: Source;
  corr?: Correlation;
  summary: string;
  attrs?: Record<string, string>;
  rawRef?: number;
}

export interface Signal {
  name: string;
  label: string;
  value: number;
  unit?: string;
  kind: string; // rate, gauge, total
  bad?: boolean;
  source: Source;
  prov: Provenance;
}

export interface ComponentState {
  id: string;
  status: ComponentStatus;
  reason?: string;
  activity: number;
  errorRate: number;
  queue?: number;
  logRate: number;
  signals?: Signal[];
  prov: Provenance;
  freshAt: string;
  stale?: boolean;
  observable?: string[];
}

export interface SourceHealth {
  ref: string;
  lastOk: string;
  lastTry: string;
  lastErr?: string;
  intervalS: number;
  stale: boolean;
  latencyMs: number;
}

export interface Status {
  overlay: string;
  beeMode: string;
  reserveSize: number;
  reserveSizeWithinRadius: number;
  pullsyncRate: number;
  storageRadius: number;
  connectedPeers: number;
  neighborhoodSize: number;
  batchCommitment: number;
  isReachable: boolean;
  lastSyncedBlock: number;
  committedDepth: number;
  isWarmingUp: boolean;
}

export interface Stamp {
  batchID: string;
  utilization: number;
  utilizationRatio: number;
  usable: boolean;
  label: string;
  depth: number;
  bucketDepth: number;
  exists: boolean;
  batchTTL: number;
}

export interface ChainState {
  chainTip: number;
  block: number;
  currentPrice: string;
}

export interface Redistribution {
  isFrozen: boolean;
  isFullySynced: boolean;
  isHealthy: boolean;
  hasSufficientFunds: boolean;
  enabled: boolean;
  phase: string;
  round: number;
  lastWonRound: number;
  lastPlayedRound: number;
  lastFrozenRound: number;
  lastSelectedRound: number;
  lastSampleDurationSeconds: number;
  block: number;
  hasCommittedThisRound: boolean;
  hasRevealedThisRound: boolean;
}

export interface NodeState {
  name: string;
  pod?: string;
  group?: string;
  phase: Phase;
  phaseSince: string;
  phaseReason?: string;
  version?: string;
  overlay?: string;
  mode?: string;
  status?: Status;
  depth: number;
  reachability?: string;
  population: number;
  connected: number;
  binConnected: number[];
  unhealthyPeers: number;
  externalPeers: number;
  redistribution?: Redistribution;
  chainState?: ChainState;
  stamps?: Stamp[];
  components: Record<string, ComponentState>;
  flowRates?: Record<string, number>;
  sources: Record<string, SourceHealth>;
  logStream: string; // following, off, error
  logDropped: number;
  issueCount: number;
  worst?: Severity;
}

export interface Edge {
  a: string;
  b: string;
  bin: number;
  latencyMs: number;
  healthy: boolean;
  seenByA: boolean;
  seenByB: boolean;
  prov: Provenance;
  freshAt: string;
}

export interface Flow {
  from: string;
  to: string;
  protocol: string; // pushsync, retrieval, pullsync, hive
  count: number;
  errors: number;
  prov: Provenance;
  source: string; // log, otlp
}

export interface FlowFrame {
  at: string;
  flows: Flow[];
}

export interface Evidence {
  text: string;
  eventSeq?: number;
  node?: string;
  signal?: string;
  value?: string;
  at: string;
  source: Source;
  prov: Provenance;
}

export interface Issue {
  id: string;
  rule: string;
  title: string;
  class: IssueClass;
  sev: Severity;
  nodes: string[] | null;
  components: string[] | null;
  startedAt: string;
  lastSeenAt: string;
  resolvedAt?: string;
  what: string;
  where: string;
  evidence: Evidence[] | null;
  hypothesis?: string;
  nextSteps: string[] | null;
  simulated?: boolean;
}

export interface Stage {
  name: string;
  node?: string;
  component?: string;
  state: string; // done, pending, failed, running, stalled, unobservable
  start?: string;
  end?: string;
  detail?: string;
  count?: number;
  total?: number;
  spanId?: string;
  parent?: string;
  attrs?: Record<string, string>;
}

export interface Operation {
  id: string;
  kind: string; // upload-tag, trace, chunk
  title: string;
  node?: string;
  nodes: string[] | null;
  state: string; // running, done, failed, stalled
  correlation: string; // observed, approximate
  start: string;
  updated: string;
  stages: Stage[] | null;
  prov: Provenance;
  note?: string;
}

export interface CollectorStats {
  startedAt: string;
  observations: number;
  events: number;
  eventsStored: number;
  oldestEvent: string;
  keyframes: number;
  subscribers: number;
  logLines: number;
  logDropped: number;
  spans: number;
  otlpListening?: string;
  heapMb: number;
  goroutines: number;
  obsBacklog: number;
}

export interface Snapshot {
  at: string;
  mode: Mode;
  target: string;
  nodes: Record<string, NodeState>;
  edges: Edge[] | null;
  networkRadius?: number;
  issues: Issue[] | null;
  collector: CollectorStats;
}

export interface LogLine {
  time: string;
  level: string;
  v?: number;
  logger: string;
  msg: string;
  error?: string;
  caller?: string;
  fields?: Record<string, string>;
  raw: string;
  partial?: boolean;
}

export interface LogRecord {
  id: number;
  node: string;
  component?: string;
  line: LogLine;
}

export interface Bucket {
  t: string;
  count: Partial<Record<Severity, number>>;
}

export interface KadPeer {
  address: string;
  bin: number;
  direction: string;
  latencyMs: number;
  reachability: string;
  healthy: boolean;
  sessionS: number;
}

export interface PeerView extends KadPeer {
  node?: string;
}

export type Pt = [number, number];

export interface NodeDetail {
  name: string;
  peers: PeerView[] | null;
  rawRefs: Record<string, number>;
  series: Record<string, Pt[]>;
}

export interface Meta {
  mode: Mode;
  target: string;
  version: string;
  startedAt: string;
  otlp?: string;
  logs: boolean;
  actions: boolean;
  intervals: Record<string, number>;
  historyS: number;
}

export interface ComponentSpec {
  id: string;
  label: string;
  lane: string;
  pkg: string;
  description: string;
  metrics: string[] | null;
  loggers: string[] | null;
  spans: string[] | null;
  endpoints: string[] | null;
  blind: string[] | null;
  x: number;
  y: number;
}

export interface FlowSpec {
  from: string;
  to: string;
  path: string;
  label: string;
  signal: string;
}

export interface Registry {
  specs: ComponentSpec[];
  flows: FlowSpec[];
}

export interface HistoryWindow {
  from: string;
  to: string;
  oldestSeq: number;
  lastSeq: number;
}

export interface SearchResult {
  type: "node" | "peer" | "component" | "chunk" | "batch" | "trace" | "tag" | "issue" | "operation";
  id: string;
  label: string;
  detail?: string;
}

export interface ChunkLookup {
  address: string;
  correlation: string;
  events: BeeEvent[] | null;
  logs: LogRecord[] | null;
}

export interface ActionResponse {
  ok: boolean;
  status: number;
  ms: number;
  body: unknown;
  error?: string;
}

export type Selection =
  | { type: "node"; node: string }
  | { type: "component"; node: string; component: string }
  | { type: "edge"; a: string; b: string }
  | { type: "event"; seq: number }
  | { type: "issue"; id: string }
  | { type: "operation"; id: string }
  | { type: "peer"; address: string; node?: string }
  | { type: "corr"; kind: string; id: string }; // chunk, batch, trace, tag
