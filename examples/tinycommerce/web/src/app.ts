import { CheckoutController } from "./checkout"

export async function bootstrapCheckout(): Promise<void> {
  const controller = new CheckoutController()
  await controller.completeCheckout({
    id: "order-1",
    customerId: "customer-1",
    totalCents: 4200,
  })
}
