use crate::models::Order;
use crate::repository::Repository as OrderRepository;

/// OrderService coordinates creation and persistence of orders.
pub struct OrderService<R: OrderRepository> {
    repository: R,
}

impl<R: OrderRepository> OrderService<R> {
    pub fn new(repository: R) -> Self {
        Self { repository }
    }

    pub async fn submit(&mut self, id: &str, customer_id: &str) -> Order {
        let order = build_order(id, customer_id);
        self.repository.save(order.clone());
        order
    }

    pub fn find(&self, id: &str) -> Option<&Order> {
        self.repository.find(id)
    }
}

fn build_order(id: &str, customer_id: &str) -> Order {
    Order::new(id, customer_id)
}
