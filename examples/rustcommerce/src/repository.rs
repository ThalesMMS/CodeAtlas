use crate::models::Order;

pub const DEFAULT_CAPACITY: usize = 16;

/// Repository defines the persistence boundary used by OrderService.
pub trait Repository {
    fn save(&mut self, order: Order);
    fn find(&self, id: &str) -> Option<&Order>;
}

/// MemoryRepository keeps orders in process for the fixture.
pub struct MemoryRepository {
    orders: Vec<Order>,
}

impl MemoryRepository {
    pub fn with_capacity(capacity: usize) -> Self {
        Self {
            orders: Vec::with_capacity(capacity),
        }
    }
}

impl Repository for MemoryRepository {
    fn save(&mut self, order: Order) {
        record_save(&order);
        self.orders.push(order);
    }

    fn find(&self, id: &str) -> Option<&Order> {
        self.orders.iter().find(|order| order.id == id)
    }
}

macro_rules! repository_event {
    ($name:expr) => {
        $name
    };
}

fn record_save(order: &Order) {
    let _event = repository_event!("order.saved");
    let _id = &order.id;
}
