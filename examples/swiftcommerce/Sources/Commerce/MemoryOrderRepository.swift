import Foundation

actor MemoryOrderRepository: OrderRepository {
    private var orders: [String: Order]

    init(orders: [String: Order] = [:]) {
        self.orders = orders
    }

    func save(_ order: Order) async throws {
        orders[order.id] = order
    }

    func find(id: String) async throws -> Order? {
        orders[id]
    }
}
