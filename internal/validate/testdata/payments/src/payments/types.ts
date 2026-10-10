// Fixture snippet (P0-T17): payments domain types. ASCII only, no I/O.

export interface Payment {
  id: string;
  customerId: string;
  amountMinor: number;
  currency: string;
  status: "pending" | "captured" | "failed" | "refunded";
}

export interface CaptureInput {
  paymentId: string;
  amountMinor: number;
}

export type PaymentEvent =
  | { kind: "payment.created"; paymentId: string }
  | { kind: "payment.captured"; paymentId: string; amountMinor: number }
  | { kind: "payment.refund.created"; paymentId: string; refundId: string };
