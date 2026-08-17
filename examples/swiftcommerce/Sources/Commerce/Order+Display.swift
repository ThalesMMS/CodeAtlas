extension Order {
    func displayName() -> String {
        displayName(prefix: "Order")
    }

    func displayName(prefix: String) -> String {
        "\(prefix) \(id)"
    }
}

infix operator <=>: ComparisonPrecedence

func <=> (lhs: Order, rhs: Order) -> Bool {
    lhs.id == rhs.id
}
