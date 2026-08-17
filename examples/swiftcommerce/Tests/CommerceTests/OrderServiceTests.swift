import XCTest

final class OrderServiceTests: XCTestCase {
    func testPersistsAnOrder() async throws {
        let repository = MemoryOrderRepository()
        let order = Order(id: "order-1", state: .pending)

        try await repository.save(order)
        let persisted = try await repository.find(id: order.id)

        XCTAssertEqual(persisted?.id, order.id)
    }
}
