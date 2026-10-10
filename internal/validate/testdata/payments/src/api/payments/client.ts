// Fixture snippet (P0-T17): payments HTTP client wrapper. ASCII only, no I/O.
import type { CaptureInput, Payment, RefundRequest, RefundResult } from "../payments/types.js";

export interface PaymentsClientOptions {
  baseUrl: string;
  apiKey: string;
}

export class PaymentsClient {
  constructor(private readonly options: PaymentsClientOptions) {}

  capturePayment(input: CaptureInput): Promise<Payment> {
    void this.options;
    return Promise.reject(new Error("fixture client never performs I/O"));
  }

  createRefund(request: RefundRequest): Promise<RefundResult> {
    void this.options;
    return Promise.reject(new Error("fixture client never performs I/O"));
  }
}
