import { OrderInput, submitOrder } from "./api"

export class CheckoutController {
  async completeCheckout(input: OrderInput): Promise<string> {
    validateCheckout(input)
    await submitOrder(input)
    return input.id
  }
}

function validateCheckout(input: OrderInput): void {
  if (!input.id || !input.customerId || input.totalCents <= 0) {
    throw new Error("invalid checkout")
  }
}
