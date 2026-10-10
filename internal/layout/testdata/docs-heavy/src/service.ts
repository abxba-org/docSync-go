// Fixture snippet (P0-T17): docs-heavy service core. ASCII only, no I/O.

export interface EventRecord {
  id: string;
  type: string;
  payload: Record<string, string>;
}

export function normalizeEvent(event: EventRecord): EventRecord {
  return { ...event, type: event.type.toLowerCase() };
}
