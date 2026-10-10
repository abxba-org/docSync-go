// Fixture snippet (P0-T17): docs-heavy telemetry counters. ASCII only, no I/O.

export interface CounterSnapshot {
  ingested: number;
  rejected: number;
}

export function emptySnapshot(): CounterSnapshot {
  return { ingested: 0, rejected: 0 };
}
