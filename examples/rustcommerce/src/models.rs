/// Order is the aggregate accepted by the checkout flow.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Order {
    pub id: String,
    pub customer_id: String,
}

impl Order {
    pub fn new(id: impl Into<String>, customer_id: impl Into<String>) -> Self {
        Self {
            id: id.into(),
            customer_id: customer_id.into(),
        }
    }
}
