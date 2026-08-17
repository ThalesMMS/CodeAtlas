export interface OrderInput {
  id: string
  customerId: string
  totalCents: number
}

export async function submitOrder(order: OrderInput): Promise<void> {
  const response = await fetch("/orders", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      ID: order.id,
      CustomerID: order.customerId,
      TotalCents: order.totalCents,
    }),
  })

  if (!response.ok) {
    throw new Error(await response.text())
  }
}
