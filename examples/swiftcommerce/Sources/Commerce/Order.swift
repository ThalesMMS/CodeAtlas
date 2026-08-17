import Foundation

/// Lifecycle of an order.
enum OrderState: String {
    case pending
    case confirmed
}

/// Value submitted to the order repository.
struct Order: Sendable {
    let id: String
    var state: OrderState
}

protocol OrderRepository: Sendable {
    func save(_ order: Order) async throws
    func find(id: String) async throws -> Order?
}
