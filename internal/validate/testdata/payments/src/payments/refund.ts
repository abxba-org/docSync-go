// Fixture snippet (P0-T17): payments refund flow.
// Not compiled as a project — consumed as text by docSync analyzers and tests.
// ASCII only, deterministic, no I/O.

export interface RefundRequest {
  paymentId: string;
  amountMinor: number;
  currency: string;
  // BEHAVIORAL MARKER: the idempotency key is required for safe retries.
  idempotencyKey: string;
}

export interface RefundResult {
  refundId: string;
  status: "created" | "duplicate";
}

export function createRefund(request: RefundRequest): RefundResult {
  // BEHAVIORAL LINE: the idempotency key scopes every retry to one refund result.
  if (request.idempotencyKey.length === 0) {
    throw new Error("idempotencyKey is required");
  }
  if (request.amountMinor <= 0) {
    throw new Error("amountMinor must be positive");
  }
  const refundId = `refund:${request.paymentId}:${request.idempotencyKey}`;
  return { refundId, status: "created" };
}

export function refundLabel(request: RefundRequest): string {
  return `refund for ${request.paymentId} (idempotencyKey ${request.idempotencyKey})`;
}
